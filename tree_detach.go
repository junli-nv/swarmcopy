package main

import (
	"errors"
	"io"
	"os"
	"path"
	"syscall"
)

// A crash between publication's link and unlink may leave two hard links.
// Detach a retained partial before any write so the old final stays immutable.
func detachTreePartial(r *os.Root, p string) error {
	st, err := r.Lstat(p)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return errors.New("partial is not a regular file")
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("partial stat unavailable")
	}
	if sys.Nlink <= 1 {
		return nil
	}
	in, err := r.Open(p)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := path.Join(path.Dir(p), ".swarmcopy-detach-"+randomID())
	out, err := r.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer r.Remove(tmp)
	_, err = io.CopyBuffer(out, in, make([]byte, 256<<10))
	if err == nil {
		err = out.Sync()
	}
	ce := out.Close()
	if err != nil {
		return err
	}
	if ce != nil {
		return ce
	}
	if err = r.Rename(tmp, p); err != nil {
		return err
	}
	return syncRootDir(r, path.Dir(p))
}
