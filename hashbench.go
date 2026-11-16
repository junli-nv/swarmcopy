package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"os/signal"
	"runtime"
	"runtime/pprof"
	"strings"
	"syscall"
	"time"
)

func hashbenchCommand(args []string) error {
	fs := flag.NewFlagSet("hashbench", flag.ContinueOnError)
	configureUsage(fs, "hashbench")
	memory := fs.Int("memory-mib", 64, "bounded in-memory input buffer (1..512 MiB)")
	total := fs.Int("bytes-mib", 2048, "total in-memory SHA256 input (1..65536 MiB)")
	file := fs.String("file", "", "optional existing regular file to hash without modifying it")
	buffer := fs.Int("buffer-mib", 4, "file read buffer (1..64 MiB)")
	profile := fs.String("cpu-profile", "", "optional NEW CPU profile path; never overwrite")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *memory < 1 || *memory > 512 || *total < 1 || *total > 65536 || *buffer < 1 || *buffer > 64 {
		return errors.New("invalid hashbench arguments")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *profile != "" {
		f, err := os.OpenFile(*profile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		if err = pprof.StartCPUProfile(f); err != nil {
			f.Close()
			return err
		}
		defer f.Close()
		defer pprof.StopCPUProfile()
	}
	data := make([]byte, *memory<<20)
	for i := range data {
		data[i] = byte(i*31 + i/251)
	}
	h := sha256.New()
	left := int64(*total) << 20
	start := time.Now()
	for left > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		size := min(left, int64(len(data)))
		h.Write(data[:size])
		left -= size
	}
	elapsed := time.Since(start).Seconds()
	result := map[string]any{"Version": version, "GoVersion": runtime.Version(), "Architecture": runtime.GOARCH,
		"MemoryBytes": int64(*total) << 20, "MemorySeconds": elapsed, "MemoryMiBPerSecond": float64(*total) / elapsed,
		"MemorySHA256": hex.EncodeToString(h.Sum(nil)), "FileBufferMiB": *buffer,
		"CPUProfile": *profile, "GODEBUG": os.Getenv("GODEBUG")}
	if cpu, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		features := map[string]bool{}
		for _, line := range strings.Split(string(cpu), "\n") {
			key, value, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			key = strings.TrimSpace(key)
			if key == "model name" {
				result["CPUModel"] = strings.TrimSpace(value)
			}
			if key == "flags" || key == "Features" {
				for _, name := range strings.Fields(value) {
					switch name {
					case "sha_ni", "avx2", "bmi2", "sha2", "sha256":
						features[name] = true
					}
				}
				break
			}
		}
		result["AdvertisedCPUFeatures"] = features
	}
	if *file != "" {
		f, err := os.Open(*file)
		if err != nil {
			return err
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("hashbench --file requires a regular file")
		}
		h = sha256.New()
		start = time.Now()
		work, err := hashRange(ctx, f, h, 0, info.Size(), make([]byte, *buffer<<20), nil)
		if err != nil {
			return err
		}
		after, err := f.Stat()
		if err != nil {
			return err
		}
		if after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
			return errors.New("hashbench input changed")
		}
		result["FileBytes"] = work.Bytes
		result["FileSeconds"] = time.Since(start).Seconds()
		result["FileReadSeconds"] = work.ReadSeconds
		result["FileComputeSeconds"] = work.ComputeSeconds
		result["FileSHA256"] = hex.EncodeToString(h.Sum(nil))
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}
