package main

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"syscall"
)

type fileFingerprint struct {
	Device, Inode                                            uint64
	Size, ModifiedSec, ModifiedNsec, ChangedSec, ChangedNsec int64
}
type verifiedRecord struct {
	Version     int
	ManifestID  string
	Fingerprint fileFingerprint
}

func fingerprint(info os.FileInfo) (fileFingerprint, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() {
		return fileFingerprint{}, false
	}
	return fileFingerprint{Device: uint64(st.Dev), Inode: st.Ino, Size: info.Size(),
		ModifiedSec: st.Mtim.Sec, ModifiedNsec: st.Mtim.Nsec, ChangedSec: st.Ctim.Sec, ChangedNsec: st.Ctim.Nsec}, true
}
func cacheMatches(path string, m Manifest, info os.FileInfo) bool {
	current, ok := fingerprint(info)
	if !ok || info.Size() != m.Size {
		return false
	}
	f, e := os.Open(path + ".swarmcopy.verified.json")
	if e != nil {
		return false
	}
	defer f.Close()
	var record verifiedRecord
	if json.NewDecoder(io.LimitReader(f, 16384)).Decode(&record) != nil {
		return false
	}
	return record.Version == 1 && record.ManifestID == m.id() && record.Fingerprint == current
}
func saveVerified(path string, m Manifest, info os.FileInfo) error {
	fp, ok := fingerprint(info)
	if !ok {
		return syscall.EINVAL
	}
	b, e := json.Marshal(verifiedRecord{Version: 1, ManifestID: m.id(), Fingerprint: fp})
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".swarmcopy-verified-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	if e = os.Rename(f.Name(), path+".swarmcopy.verified.json"); e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func rememberVerified(path string, m Manifest, info os.FileInfo) {
	if e := saveVerified(path, m, info); e != nil {
		log.Printf("verified cache not saved: %v", e)
	}
}
