package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Job struct {
	Inputs                                                                       []SourceEntry `json:",omitempty"`
	Sources                                                                      []string      `json:",omitempty"`
	Existing                                                                     string
	QUICGSOKiB                                                                   int
	QUICMaxPayload                                                               int
	QUICSockets                                                                  int
	Transport                                                                    string
	VerifyWindowChunks                                                           int
	PipelineVerify                                                               bool
	VerifyBufferMiB                                                              int
	Adaptive, Endgame                                                            bool
	MinWorkers                                                                   int
	MaxWorkers                                                                   int
	HedgeDelay                                                                   time.Duration
	Fanout                                                                       int
	Recheck                                                                      bool
	ID, Created, State, Source, Seed, SeedFile, Dest, BinDir, SSHUser, LastError string
	Hosts                                                                        []string
	Port, Workers, Uploads                                                       int
	UploadMiB, DownloadMiB                                                       int64
	Lifetime                                                                     time.Duration
	Imported, KeepSeeding                                                        bool
	Dir                                                                          string `json:"-"`
}

func stateDir() string {
	if s := os.Getenv("XDG_STATE_HOME"); s != "" {
		return filepath.Join(s, "swarmcopy", "jobs")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".local", "state", "swarmcopy", "jobs")
}
func randomID() string {
	b := make([]byte, 8)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func resolveJob(s string) string {
	if !strings.ContainsRune(s, os.PathSeparator) {
		return filepath.Join(stateDir(), s)
	}
	a, _ := filepath.Abs(s)
	return a
}
func loadJob(path string) (Job, error) {
	var j Job
	if path == "" {
		return j, errors.New("--job is required")
	}
	dir := resolveJob(path)
	b, e := os.ReadFile(filepath.Join(dir, "job.json"))
	if e != nil {
		return j, e
	}
	e = json.Unmarshal(b, &j)
	j.Dir = dir
	if e == nil && (j.ID == "" || len(j.Hosts) == 0 || j.Port < 1 || j.Port > 65535) {
		e = errors.New("invalid job record")
	}
	return j, e
}
func (j Job) save() error {
	b, e := json.MarshalIndent(j, "", "  ")
	if e != nil {
		return e
	}
	return atomicWrite(filepath.Join(j.Dir, "job.json"), b, 0600)
}

func (j Job) remoteDir() string { return filepath.Join(j.Dest, ".swarmcopy", j.ID) }
func (j Job) addresses() []string {
	a := make([]string, len(j.Hosts))
	for i, h := range j.Hosts {
		a[i] = strings.Split(h, "@")[len(strings.Split(h, "@"))-1] + ":" + strconv.Itoa(j.Port)
	}
	return a
}
func hostList(input string) ([]string, error) {
	if strings.HasPrefix(input, "@") {
		b, e := os.ReadFile(input[1:])
		if e != nil {
			return nil, e
		}
		lines := []string{}
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
			if line != "" {
				lines = append(lines, line)
			}
		}
		input = strings.Join(lines, ",")
	}
	tokens := []string{}
	depth, start := 0, 0
	for i, c := range input {
		switch c {
		case '[':
			depth++
		case ']':
			depth--
		case ',':
			if depth == 0 {
				tokens = append(tokens, input[start:i])
				start = i + 1
			}
		}
		if depth < 0 || depth > 1 {
			return nil, errors.New("invalid host brackets")
		}
	}
	if depth != 0 {
		return nil, errors.New("unclosed host brackets")
	}
	tokens = append(tokens, input[start:])
	valid := regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.@-]*$`)
	out := []string{}
	seen := map[string]bool{}
	add := func(s string) error {
		if !valid.MatchString(s) || strings.Count(s, "@") > 1 {
			return fmt.Errorf("invalid host %q", s)
		}
		if !seen[s] {
			out = append(out, s)
			seen[s] = true
		}
		if len(out) > 1024 {
			return errors.New("at most 1024 hosts")
		}
		return nil
	}
	for _, v := range tokens {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		l := strings.IndexByte(v, '[')
		if l < 0 {
			if e := add(v); e != nil {
				return nil, e
			}
			continue
		}
		r := strings.IndexByte(v, ']')
		for _, part := range strings.Split(v[l+1:r], ",") {
			bounds := strings.Split(part, "-")
			if len(bounds) > 2 || len(bounds[0]) == 0 {
				return nil, errors.New("invalid host range")
			}
			a, e := strconv.Atoi(bounds[0])
			if e != nil || a < 0 {
				return nil, errors.New("non-numeric host range")
			}
			b := a
			if len(bounds) == 2 {
				b, e = strconv.Atoi(bounds[1])
				if e != nil || b < a {
					return nil, errors.New("invalid range end")
				}
			}
			if b-a > 1023 {
				return nil, errors.New("host range too large")
			}
			for n := a; n <= b; n++ {
				if e := add(v[:l] + fmt.Sprintf("%0*d", len(bounds[0]), n) + v[r+1:]); e != nil {
					return nil, e
				}
			}
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no hosts")
	}
	return out, nil
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

type sshRunner struct {
	env      []string
	user     string
	password bool
}

func (r sshRunner) run(ctx context.Context, host, reason, command string, in io.Reader) ([]byte, error) {
	target := host
	if r.user != "" && !strings.Contains(host, "@") {
		target = r.user + "@" + host
	}
	batch := "yes"
	if r.password {
		batch = "no"
	}
	args := []string{"-T", "-o", "BatchMode=" + batch, "-o", "ConnectTimeout=8", "-o", "StrictHostKeyChecking=yes", "-o", "NumberOfPasswordPrompts=1", "--", target, command}
	log.Printf("SSH target=%s reason=%s command=%s", target, reason, command)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Env = r.env
	cmd.Stdin = in
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = io.MultiWriter(&stderr, os.Stderr)
	if e := cmd.Run(); e != nil {
		msg := stderr.String()
		if len(msg) > 2048 {
			msg = msg[:2048]
		}
		return nil, fmt.Errorf("SSH %s: %w: %s", target, e, strings.TrimSpace(msg))
	}
	return out.Bytes(), nil
}
func (r sshRunner) write(ctx context.Context, h, path string, data io.Reader, executable bool) error {
	mode := "600"
	if executable {
		mode = "700"
	}
	q := shellQuote(path)
	tmp := shellQuote(path + ".next")
	_, e := r.run(ctx, h, "upload file atomically", "umask 077; cat > "+tmp+" && chmod "+mode+" "+tmp+" && mv -f -- "+tmp+" "+q, data)
	return e
}
func (r sshRunner) bootstrap(ctx context.Context, j Job, h string) error {
	root := j.remoteDir()
	if _, e := r.run(ctx, h, "create job directory", "umask 077; mkdir -p -- "+shellQuote(root), nil); e != nil {
		return e
	}
	arch, e := r.run(ctx, h, "detect executable architecture", "uname -m", nil)
	if e != nil {
		return e
	}
	a := strings.TrimSpace(string(arch))
	name := "swarmcopy-linux-" + a
	switch a {
	case "x86_64", "aarch64":
	default:
		return fmt.Errorf("unsupported remote architecture %q", a)
	}
	path := filepath.Join(j.BinDir, name)
	if _, e = os.Stat(path); e != nil {
		current, _ := os.Executable()
		if a == "x86_64" && runtime.GOARCH == "amd64" || a == "aarch64" && runtime.GOARCH == "arm64" {
			path = current
		} else {
			return fmt.Errorf("need %s in --bin-dir", name)
		}
	}
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	if e = r.write(ctx, h, filepath.Join(root, "swarmcopy"), f, true); e != nil {
		return e
	}
	t, e := os.ReadFile(filepath.Join(j.Dir, "job.token"))
	if e != nil {
		return e
	}
	return r.write(ctx, h, filepath.Join(root, "job.token"), bytes.NewReader(t), false)
}
func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == "--"+name || a == "-"+name || strings.HasPrefix(a, "--"+name+"=") || strings.HasPrefix(a, "-"+name+"=") {
			return true
		}
	}
	return false
}
func jobsCommand(args []string) error {
	fs := flag.NewFlagSet("jobs", flag.ContinueOnError)
	configureUsage(fs, "jobs")
	dir := fs.String("state-dir", stateDir(), "directory containing job directories")
	asJSON := fs.Bool("json", false, "print JSON records")
	if e := fs.Parse(args); e != nil {
		return e
	}
	entries, e := os.ReadDir(*dir)
	if os.IsNotExist(e) {
		fmt.Println("No saved jobs.")
		return nil
	}
	if e != nil {
		return e
	}
	jobs := []Job{}
	for _, entry := range entries {
		if entry.IsDir() {
			if j, e := loadJob(filepath.Join(*dir, entry.Name())); e == nil {
				jobs = append(jobs, j)
			}
		}
	}
	sort.Slice(jobs, func(a, b int) bool { return jobs[a].Created > jobs[b].Created })
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(jobs)
	}
	fmt.Printf("%-33s %-20s %-6s %s\n", "JOB ID", "RECORDED STATE", "NODES", "MEDIA")
	for _, j := range jobs {
		fmt.Printf("%-33s %-20s %-6d %s\n", j.ID, j.State, len(j.Hosts), jobMedia(j))
	}
	fmt.Println("Recorded state may be stale after a controller crash; watch/status query live nodes; inspect shows the record.")
	return nil
}
func jobSelector(args []string) (Job, error) {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	configureUsage(fs, "inspect")
	id := fs.String("job", "", "saved ID or directory")
	if len(args) == 0 {
		fs.Usage()
		return Job{}, errors.New("missing arguments; see examples above")
	}
	if e := fs.Parse(args); e != nil {
		return Job{}, e
	}
	return loadJob(*id)
}
func inspectCommand(args []string) error {
	j, e := jobSelector(args)
	if e != nil {
		return e
	}
	b, e := json.MarshalIndent(j, "", "  ")
	if e == nil {
		fmt.Println(string(b))
		fmt.Println("Record:", j.Dir)
	}
	return e
}

func clusterCommand(mode string, args []string) error {
	fs := flag.NewFlagSet(mode, flag.ContinueOnError)
	configureUsage(fs, mode)
	id := fs.String("job", "", "saved job ID or directory; new jobs default to state directory")
	ask := fs.Bool("ask-pass", false, "prompt once for a shared SSH password (not saved)")
	user := fs.String("ssh-user", "", "SSH username; otherwise use user@host or SSH config")
	var j Job
	var hosts string
	var sources sourceFlags
	var existing string
	var recheck bool
	if mode == "resume" {
		fs.StringVar(&existing, "existing", "", "conflicts: ask, resume, replace or keep; overrides saved policy")
		fs.BoolVar(&recheck, "recheck", false, "force full existing-file verification")
	}
	if mode == "distribute" {
		fs.IntVar(&j.QUICGSOKiB, "quic-gso-kib", 64, "maximum QUIC GSO batch KiB (16..64), congestion/pacing limited")
		fs.IntVar(&j.QUICMaxPayload, "quic-max-payload", 8952, "QUIC UDP payload ceiling (1452..8952), with path MTU discovery")
		fs.IntVar(&j.QUICSockets, "quic-sockets", 4, "QUIC client UDP socket shards (1..32)")
		fs.StringVar(&j.Transport, "transport", "tcp", "chunk data transport: tcp or quic (UDP); control stays TCP")
		fs.IntVar(&j.VerifyWindowChunks, "verify-window-chunks", 256, "upcoming hash chunks to prefer (0..4096); requires pipeline-verify")
		fs.BoolVar(&j.PipelineVerify, "pipeline-verify", true, "overlap ordered whole-file SHA256 with transfer")
		fs.IntVar(&j.VerifyBufferMiB, "verify-buffer-mib", 4, "whole-file verification read buffer (1..64 MiB)")
		fs.StringVar(&hosts, "hosts", "", "host list, bracket ranges, or @host-file")
		fs.Var(&sources, "source", "controller-local file, directory or quoted glob; repeatable")
		fs.StringVar(&j.Existing, "existing", "ask", "different existing files: ask, resume, replace or keep")
		fs.StringVar(&j.Seed, "seed", "", "initial seed host; default first host")
		fs.StringVar(&j.SeedFile, "seed-file", "", "already prepared seed file with .manifest.json sidecar")
		fs.StringVar(&j.Dest, "dest", "/raid/images", "absolute local directory on target nodes")
		exe, _ := os.Executable()
		fs.StringVar(&j.BinDir, "bin-dir", defaultBinaryDir(exe), "directory of both architecture binaries")
		fs.IntVar(&j.Port, "port", 19700, "peer TCP port")
		fs.IntVar(&j.Fanout, "fanout", 8, "maximum concurrent SSH deployment operations")
		fs.IntVar(&j.Workers, "workers", 4, "fixed or initial concurrent downloads")
		fs.IntVar(&j.MinWorkers, "min-workers", 0, "adaptive minimum downloads; 0=initial workers")
		fs.BoolVar(&j.Adaptive, "adaptive", false, "experimental adaptive concurrency and source selection")
		fs.BoolVar(&j.Endgame, "endgame", false, "hedge delayed final chunks from an alternate source")
		fs.IntVar(&j.MaxWorkers, "max-workers", 16, "adaptive maximum downloads (workers..32)")
		fs.DurationVar(&j.HedgeDelay, "hedge-delay", 2*time.Second, "minimum age before an endgame duplicate request")
		fs.IntVar(&j.Uploads, "uploads", 4, "concurrent uploads")
		fs.Int64Var(&j.UploadMiB, "upload-mib", 0, "per-node upload MiB/s; 0=unlimited")
		fs.Int64Var(&j.DownloadMiB, "download-mib", 0, "per-node download MiB/s; 0=unlimited")
		fs.DurationVar(&j.Lifetime, "lifetime", 2*time.Hour, "controller and peer maximum runtime per invocation")
		fs.BoolVar(&j.KeepSeeding, "keep-seeding", false, "leave peers running after every target completes")
		fs.BoolVar(&j.Recheck, "recheck", false, "ignore seed/final verification caches")
	}
	if len(args) == 0 {
		fs.Usage()
		return errors.New("missing arguments; see examples above")
	}
	if mode == "distribute" {
		args = sourceArguments(fs, args)
	}
	if e := fs.Parse(args); e != nil {
		return e
	}
	if mode != "distribute" && fs.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	sources = append(sources, fs.Args()...)
	if mode == "distribute" {
		if j.QUICGSOKiB < 16 || j.QUICGSOKiB > 64 {
			return errors.New("quic-gso-kib must be 16..64")
		}
		if j.QUICMaxPayload < 1452 || j.QUICMaxPayload > 8952 {
			return errors.New("quic-max-payload must be 1452..8952")
		}
		if j.QUICSockets < 1 || j.QUICSockets > 32 {
			return errors.New("quic-sockets must be 1..32")
		}
		if !validTransport(j.Transport) {
			return errors.New("transport must be tcp or quic")
		}
		var e error
		j.Hosts, e = hostList(hosts)
		if e != nil {
			return e
		}
		if (len(sources) == 0) == (j.SeedFile == "") {
			return errors.New("specify exactly one of --source or --seed-file")
		}
		if !filepath.IsAbs(j.Dest) || strings.ContainsAny(j.Dest, "\r\n") {
			return errors.New("--dest must be an absolute path without newlines")
		}
		j.Dest = filepath.Clean(j.Dest)
		if j.Fanout < 1 || j.Fanout > 64 {
			return errors.New("fanout must be 1..64")
		}
		if j.Port < 1 || j.Port > 65535 || j.Workers < 1 || j.Workers > 32 || j.Uploads < 1 || j.Uploads > 32 ||
			j.UploadMiB < 0 || j.DownloadMiB < 0 || j.UploadMiB > 100000 || j.DownloadMiB > 100000 || j.Lifetime <= 0 {
			return errors.New("invalid resource limits")
		}
		normalizeVerifyFlags(fs, j.PipelineVerify, &j.VerifyWindowChunks)
		if j.VerifyWindowChunks < 0 || j.VerifyWindowChunks > 4096 || j.VerifyWindowChunks > 0 && !j.PipelineVerify {
			return errors.New("verify-window-chunks must be 0..4096 and requires pipeline-verify")
		}
		if j.VerifyBufferMiB < 1 || j.VerifyBufferMiB > 64 {
			return errors.New("verify-buffer-mib must be 1..64")
		}
		if j.MinWorkers == 0 {
			j.MinWorkers = j.Workers
		}
		if j.MinWorkers < 1 || j.MinWorkers > j.Workers {
			return errors.New("min-workers must be 1..workers, or 0 for initial workers")
		}
		if j.MaxWorkers < j.Workers && !j.Adaptive {
			j.MaxWorkers = j.Workers
		}
		if j.MaxWorkers < j.Workers || j.MaxWorkers > 32 || j.HedgeDelay <= 0 {
			return errors.New("invalid max-workers or hedge-delay")
		}
		if j.Seed == "" {
			j.Seed = j.Hosts[0]
		}
		found := false
		for _, h := range j.Hosts {
			if h == j.Seed {
				found = true
			}
		}
		if !found {
			return errors.New("--seed must appear in --hosts")
		}
		if len(sources) > 0 {
			j.Inputs, j.Sources, e = buildSourcePlan(sources)
			if e != nil {
				return e
			}
			j.Source = j.Sources[0]
		} else if !filepath.IsAbs(j.SeedFile) {
			return errors.New("--seed-file must be absolute")
		}
		if j.Existing != "ask" && j.Existing != "resume" && j.Existing != "replace" && j.Existing != "keep" {
			return errors.New("existing must be ask, resume, replace or keep")
		}

		j.BinDir, e = filepath.Abs(j.BinDir)
		if e != nil {
			return e
		}
		j.ID = time.Now().UTC().Format("20060102T150405") + "-" + randomID()[:8]
		j.Created = time.Now().UTC().Format(time.RFC3339)
		if len(j.Inputs) > 0 {
			j.SeedFile = filepath.Join(j.remoteDir(), "dataset")
		}
		j.SSHUser = *user
		j.State = "created"
		if *id == "" {
			j.Dir = filepath.Join(stateDir(), j.ID)
		} else {
			j.Dir = resolveJob(*id)
		}
		if e = os.MkdirAll(filepath.Dir(j.Dir), 0700); e != nil {
			return e
		}
		if e = os.Mkdir(j.Dir, 0700); e != nil {
			return e
		}
		if e = os.WriteFile(filepath.Join(j.Dir, "job.token"), []byte(randomID()+randomID()+"\n"), 0600); e != nil {
			return e
		}
		if e = j.save(); e != nil {
			return e
		}
	} else {
		var e error
		j, e = loadJob(*id)
		if e != nil {
			return e
		}
		if *user != "" {
			j.SSHUser = *user
		}
	}
	if mode == "resume" && existing != "" {
		if existing != "ask" && existing != "resume" && existing != "replace" && existing != "keep" {
			return errors.New("invalid existing policy")
		}
		j.Existing = existing
	}
	if mode == "resume" && hasFlag(args, "recheck") {
		j.Recheck = recheck
	}
	fmt.Printf("JOB ID: %s\nJOB DIRECTORY: %s\n", j.ID, j.Dir)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, j.Lifetime)
	defer cancel()
	env, cleanup, e := passwordEnvironment(ctx, *ask)
	if e != nil {
		return e
	}
	defer cleanup()
	r := sshRunner{env: env, user: j.SSHUser, password: *ask}
	if mode == "stop" {
		if e = os.WriteFile(filepath.Join(j.Dir, "stop.request"), []byte(time.Now().String()), 0600); e != nil {
			return e
		}
		e = stopPeers(ctx, r, j)
		if e == nil {
			j.State = "stopped"
			j.LastError = ""
		} else {
			j.State = "stop_failed"
			j.LastError = e.Error()
		}
		se := j.save()
		return errors.Join(e, se)
	}
	return runJob(ctx, r, &j)
}
func stopPeers(ctx context.Context, r sshRunner, j Job) error { return stopRemotePeers(ctx, r, j) }

func runJob(ctx context.Context, r sshRunner, j *Job) (result error) {
	fd, e := syscall.Open(filepath.Join(j.Dir, "controller.lock"), syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if e != nil {
		return e
	}
	defer syscall.Close(fd)
	if e = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return errors.New("another controller is active for this job")
	}
	j.State = "starting"
	j.LastError = ""
	if e = j.save(); e != nil {
		return e
	}
	defer func() {
		if result != nil {
			j.State = "interrupted"
			j.LastError = result.Error()
			j.save()
		}
	}()
	os.Remove(filepath.Join(j.Dir, "stop.request"))
	if len(j.Inputs) > 0 {
		mp := filepath.Join(j.Dir, "manifest.json")
		if _, x := os.Stat(mp); os.IsNotExist(x) {
			j.State = "hashing-source"
			j.save()
			m, x := sourceManifest(ctx, *j)
			if x != nil {
				return x
			}
			if x = saveManifest(mp, m); x != nil {
				return x
			}
		} else if x != nil {
			return x
		}
	}
	if e = forHosts(ctx, j.Hosts, j.Fanout, func(c context.Context, h string) error { return r.bootstrap(c, *j, h) }); e != nil {
		return e
	}
	root := j.remoteDir()
	binary := shellQuote(filepath.Join(root, "swarmcopy"))
	if len(j.Inputs) == 0 && j.Source != "" && !j.Imported {
		f, e := os.Open(j.Source)
		if e != nil {
			return e
		}
		before, e := f.Stat()
		if e != nil {
			f.Close()
			return e
		}
		j.State = "importing"
		j.save()
		b, e := r.run(ctx, j.Seed, "stage source once on seed", binary+" prepare --source - --size "+strconv.FormatInt(before.Size(), 10)+" --out "+shellQuote(j.SeedFile), f)
		after, se := f.Stat()
		f.Close()
		if e != nil {
			return e
		}
		if se != nil {
			return se
		}
		if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
			return errors.New("source changed while staging")
		}
		fmt.Print(string(b))
		j.Imported = true
		if e = j.save(); e != nil {
			return e
		}
	}
	manifestPath := filepath.Join(j.Dir, "manifest.json")
	if _, e = os.Stat(manifestPath); os.IsNotExist(e) {
		b, e := r.run(ctx, j.Seed, "fetch trusted manifest", "cat -- "+shellQuote(j.SeedFile+".manifest.json"), nil)
		if e != nil {
			return e
		}
		if e = atomicWrite(manifestPath, b, 0600); e != nil {
			return e
		}
	} else if e != nil {
		return e
	}
	m, e := loadManifest(manifestPath)
	if e != nil {
		return e
	}
	manifest, e := os.ReadFile(manifestPath)
	if e != nil {
		return e
	}
	if m.Version == 2 {
		ready, x := preflightTree(ctx, r, j, m, manifest)
		if x != nil {
			return x
		}
		if !j.Imported {
			if !ready {
				j.State = "importing"
				j.save()
				if x = importTreeJob(ctx, r, j, binary); x != nil {
					return x
				}
			} else {
				fmt.Println("EXISTING complete replica found; skipping controller-to-seed data transfer")
			}
			j.Imported = true
			if x = j.save(); x != nil {
				return x
			}
		}
		if j.Existing == "" || j.Existing == "ask" {
			j.Existing = "keep"
		}
	}
	if e = forHosts(ctx, j.Hosts, j.Fanout, func(c context.Context, h string) error { return startRemotePeer(c, r, *j, m, manifest, h) }); e != nil {
		return e
	}
	j.State = "running"
	if e = j.save(); e != nil {
		return e
	}
	t, e := token(filepath.Join(j.Dir, "job.token"))
	if e != nil {
		return e
	}
	c := client()
	defer c.CloseIdleConnections()
	previous := map[string]observation{}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	clear := false
	if st, e := os.Stdout.Stat(); e == nil {
		clear = st.Mode()&os.ModeCharDevice != 0
	}
	for {
		if _, e = os.Stat(filepath.Join(j.Dir, "stop.request")); e == nil {
			return errors.New("stop requested; partial files retained")
		}
		rows := observe(ctx, c, t, j.addresses())
		all := render(rows, previous, clear)
		for _, o := range rows {
			if o.err == nil {
				previous[o.addr] = o
			}
		}
		if all {
			report := map[string]any{"Finished": time.Now().UTC().Format(time.RFC3339), "ManifestID": m.id(), "Bytes": m.Size, "Nodes": rowsToStatus(rows)}
			b, _ := json.MarshalIndent(report, "", "  ")
			if e = atomicWrite(filepath.Join(j.Dir, "result.json"), b, 0600); e != nil {
				return e
			}
			if !j.KeepSeeding {
				if e = stopPeers(ctx, r, *j); e != nil {
					return e
				}
			}
			j.State = "complete"
			j.LastError = ""
			if e = j.save(); e != nil {
				return e
			}
			fmt.Printf("JOB COMPLETE: %s; every target passed SHA256\n", j.ID)
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("controller interrupted: %w; use resume --job %s", ctx.Err(), j.ID)
		case <-ticker.C:
		}
	}
}
func rowsToStatus(rows []observation) map[string]Status {
	out := map[string]Status{}
	for _, r := range rows {
		if r.err == nil {
			out[r.addr] = r.st
		}
	}
	return out
}

func jobMedia(j Job) string {
	if len(j.Inputs) > 0 {
		return fmt.Sprintf("%d files / %d entries", treePlanFiles(j.Inputs), len(j.Inputs))
	}
	return filepath.Base(j.SeedFile)
}
func treePlanFiles(p []SourceEntry) int {
	n := 0
	for _, s := range p {
		if s.Entry.Kind == "file" {
			n++
		}
	}
	return n
}

func defaultBinaryDir(exe string) string {
	d := filepath.Dir(exe)
	if st, e := os.Stat(filepath.Join(d, "dist")); e == nil && st.IsDir() {
		return filepath.Join(d, "dist")
	}
	return d
}
