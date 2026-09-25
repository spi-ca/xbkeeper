//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// Go 1.24 Root does not yet provide Rename, WriteFile or RemoveAll.
func renameInRoot(r *os.Root, from, to string) error {
	d, err := r.Open(".")
	if err != nil {
		return err
	}
	defer d.Close()
	return syscall.Renameat(int(d.Fd()), from, int(d.Fd()), to)
}
func writeExclusive(r *os.Root, name string, data []byte) error {
	f, err := r.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

// syncTree uses descriptor-relative traversal and rejects links and special files.
// The caller validates the staged tree before syncing it.
func syncTree(r *os.Root, name string) error {
	fi, err := r.Lstat(name)
	if err != nil {
		return err
	}
	uid, ok := owner(fi)
	if !ok || int(uid) != os.Geteuid() || fi.Mode().Perm()&0077 != 0 {
		return errors.New("unsafe sync target")
	}
	if fi.Mode().IsRegular() {
		if fi.Sys().(*syscall.Stat_t).Nlink != 1 {
			return errors.New("unsafe hardlinked sync target")
		}
		f, err := r.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return err
		}
		defer f.Close()
		opened, err := f.Stat()
		if err != nil {
			return err
		}
		if !os.SameFile(fi, opened) || !opened.Mode().IsRegular() {
			return errors.New("sync target changed")
		}
		return f.Sync()
	}
	if !fi.IsDir() {
		return errors.New("unsafe sync target type")
	}
	sub, err := r.OpenRoot(name)
	if err != nil {
		return err
	}
	defer sub.Close()
	dir, err := sub.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	opened, err := dir.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(fi, opened) || !opened.IsDir() {
		return errors.New("sync directory changed")
	}
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return err
	}
	for _, entry := range names {
		if err := syncTree(sub, entry); err != nil {
			return err
		}
	}
	return dir.Sync()
}

func syncRoot(r *os.Root) error {
	f, err := r.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// Only recurse through private regular files/directories; reject links and devices.
// Callers validate the entire tree before invoking this, and the backup root is
// owner-only. Each step checks again before descending.
func removeTree(r *os.Root, name string) error {
	fi, err := r.Lstat(name)
	if err != nil {
		return err
	}
	uid, ok := owner(fi)
	if !ok || int(uid) != os.Geteuid() || fi.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("unsafe removal target: %q", name)
	}
	if fi.Mode().IsRegular() {
		if fi.Sys().(*syscall.Stat_t).Nlink != 1 {
			return errors.New("unsafe hardlinked removal target")
		}
		return r.Remove(name)
	}
	if !fi.IsDir() {
		return errors.New("unsafe removal target type")
	}
	sub, err := r.OpenRoot(name)
	if err != nil {
		return err
	}
	defer sub.Close()
	dir, err := sub.Open(".")
	if err != nil {
		return err
	}
	names, err := dir.Readdirnames(-1)
	dir.Close()
	if err != nil {
		return err
	}
	for _, entry := range names {
		if err := removeTree(sub, entry); err != nil {
			return err
		}
	}
	return r.Remove(name)
}
