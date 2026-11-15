package main

import (
	"bufio"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

func hiddenPassword(ctx context.Context) ([]byte, error) {
	tty, e := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if e != nil {
		return nil, errors.New("--ask-pass needs an interactive controlling terminal")
	}
	defer tty.Close()
	var old syscall.Termios
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, tty.Fd(), uintptr(syscall.TCGETS), uintptr(unsafe.Pointer(&old))); errno != 0 {
		return nil, errno
	}
	hidden := old
	hidden.Lflag &^= syscall.ECHO
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, tty.Fd(), uintptr(syscall.TCSETS), uintptr(unsafe.Pointer(&hidden))); errno != 0 {
		return nil, errno
	}
	defer syscall.Syscall(syscall.SYS_IOCTL, tty.Fd(), uintptr(syscall.TCSETS), uintptr(unsafe.Pointer(&old)))
	fmt.Fprint(tty, "Shared SSH password (not saved): ")
	type answer struct {
		data string
		err  error
	}
	ready := make(chan answer, 1)
	go func() {
		data, err := bufio.NewReader(io.LimitReader(tty, 4097)).ReadString('\n')
		ready <- answer{data, err}
	}()
	var data string
	select {
	case a := <-ready:
		data = a.data
		e = a.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	fmt.Fprintln(tty)
	if e != nil {
		return nil, e
	}
	data = strings.TrimSuffix(strings.TrimSuffix(data, "\n"), "\r")
	if len(data) == 0 || len(data) > 4096 {
		return nil, errors.New("empty or oversized password")
	}
	return []byte(data), nil
}
func passwordEnvironment(ctx context.Context, enabled bool) ([]string, func(), error) {
	env := os.Environ()
	if !enabled {
		return env, func() {}, nil
	}
	pass, e := hiddenPassword(ctx)
	if e != nil {
		return nil, nil, e
	}
	dir, e := os.MkdirTemp("", "swarmcopy-askpass-")
	if e != nil {
		return nil, nil, e
	}
	socket := filepath.Join(dir, "socket")
	ln, e := net.Listen("unix", socket)
	if e != nil {
		os.RemoveAll(dir)
		return nil, nil, e
	}
	if e = os.Chmod(socket, 0600); e != nil {
		ln.Close()
		os.RemoveAll(dir)
		return nil, nil, e
	}
	cookie := randomID() + randomID()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			c.SetDeadline(time.Now().Add(3 * time.Second))
			b, e := bufio.NewReader(io.LimitReader(c, 256)).ReadString('\n')
			if e == nil && subtle.ConstantTimeCompare([]byte(strings.TrimSuffix(b, "\n")), []byte(cookie)) == 1 {
				c.Write(pass)
				io.WriteString(c, "\n")
			}
			c.Close()
		}
	}()
	exe, e := os.Executable()
	if e != nil {
		ln.Close()
		wg.Wait()
		os.RemoveAll(dir)
		return nil, nil, e
	}
	env = append(env, "SSH_ASKPASS="+exe, "SSH_ASKPASS_REQUIRE=force", "DISPLAY=swarmcopy:0",
		"SWARMCOPY_ASKPASS_MODE=1", "SWARMCOPY_ASKPASS_SOCKET="+socket, "SWARMCOPY_ASKPASS_COOKIE="+cookie)
	cleanup := func() {
		ln.Close()
		wg.Wait()
		for i := range pass {
			pass[i] = 0
		}
		os.RemoveAll(dir)
	}
	return env, cleanup, nil
}
func askpassHelper() error {
	if len(os.Args) != 2 {
		return errors.New("invalid askpass invocation")
	}
	prompt := strings.ToLower(os.Args[1])
	if !strings.Contains(prompt, "password") && !strings.Contains(prompt, "passphrase") {
		return errors.New("host-key and MFA prompts require separate interactive SSH setup")
	}
	c, e := net.DialTimeout("unix", os.Getenv("SWARMCOPY_ASKPASS_SOCKET"), 2*time.Second)
	if e != nil {
		return e
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, e = io.WriteString(c, os.Getenv("SWARMCOPY_ASKPASS_COOKIE")+"\n"); e != nil {
		return e
	}
	b, e := io.ReadAll(io.LimitReader(c, 4098))
	if e != nil {
		return e
	}
	if len(b) == 0 {
		return errors.New("askpass unavailable")
	}
	_, e = os.Stdout.Write(b)
	return e
}
