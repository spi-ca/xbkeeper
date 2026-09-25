//go:build linux

package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

func token() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func lock(root *os.Root) (*os.File, error) {
	f, err := root.OpenFile(".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	if err := checkLock(f); err != nil {
		f.Close()
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("backup already running or lock unavailable: %w", err)
	}
	if err = lockIdentity(root, f); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
func checkLock(f *os.File) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	uid, ok := owner(fi)
	if !ok || int(uid) != os.Geteuid() || !fi.Mode().IsRegular() || fi.Mode().Perm()&0077 != 0 || fi.Sys().(*syscall.Stat_t).Nlink != 1 {
		return errors.New("unsafe lock file")
	}
	return nil
}

// A verify must never create the lock or open a planted FIFO blocking.
func sharedLock(root *os.Root) (*os.File, error) {
	f, err := root.OpenFile(".lock", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	if err = checkLock(f); err == nil {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
	}
	if err == nil {
		err = lockIdentity(root, f)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
func lockIdentity(root *os.Root, f *os.File) error {
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	entry, err := root.Lstat(".lock")
	if err != nil || !safeEntry(entry, false) || !sameSnapshot(opened, entry) {
		return errors.New("lock file replaced or unsafe")
	}
	return nil
}
func freeSpace(c config) error {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(c.BackupDir, &stat); err != nil {
		return err
	}
	available := stat.Bavail * uint64(stat.Bsize)
	needed := uint64(c.MinFreeBytes)
	err := filepath.WalkDir(c.Datadir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 || !(fi.IsDir() || fi.Mode().IsRegular() || fi.Mode()&os.ModeSocket != 0) {
			return fmt.Errorf("unsupported datadir entry: %q", p)
		}
		if fi.Mode().IsRegular() {
			st := fi.Sys().(*syscall.Stat_t)
			size := uint64(st.Blocks) * 512
			if size > available || needed > available-size {
				return errors.New("insufficient free space for datadir allocation plus reserve")
			}
			needed += size
		}
		return nil
	})
	if err != nil {
		return err
	}
	if needed > available {
		return errors.New("insufficient free space for reserve")
	}
	return nil
}
