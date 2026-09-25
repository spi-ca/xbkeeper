//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Sequential tests inject parent-sync failures without changing filesystem policy.
var syncInitializationParent = syncRoot

// openBackupRoot walks from / through descriptor-relative roots. It creates only
// missing backup-directory components, never adjusts existing attributes, and
// returns the already-validated root descriptor to the backup caller.
func openBackupRoot(ctx context.Context, path string) (*os.Root, error) {
	if _, err := backupRootMissing(path); err != nil {
		return nil, err
	}
	r, err := os.OpenRoot("/")
	if err != nil {
		return nil, err
	}
	cur := "/"
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if part == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			r.Close()
			return nil, err
		}
		fi, err := r.Lstat(part)
		if errors.Is(err, os.ErrNotExist) {
			// Recheck the ancestor's ownership and write policy before mutation.
			if err := controlledParents(filepath.Join(cur, part)); err != nil {
				r.Close()
				return nil, errors.New("unsafe backup directory ancestor")
			}
			if err = ctx.Err(); err != nil {
				r.Close()
				return nil, err
			}
			if err = r.Mkdir(part, 0700); err != nil && !errors.Is(err, os.ErrExist) {
				r.Close()
				return nil, errors.New("cannot create backup directory")
			}
			fi, err = r.Lstat(part)
		}
		if err != nil || fi == nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
			r.Close()
			return nil, errors.New("unsafe backup directory component")
		}
		// Existing ancestors retain their permissions. A concurrent Mkdir
		// winner is subject to exactly the same checks as an existing entry.
		uid, ok := owner(fi)
		if !ok || (int(uid) != os.Geteuid() && uid != 0) ||
			(fi.Mode().Perm()&0022 != 0 && !(uid == 0 && fi.Mode()&os.ModeSticky != 0)) {
			r.Close()
			return nil, errors.New("unsafe backup directory ancestor")
		}
		if filepath.Join(cur, part) == path && (int(uid) != os.Geteuid() || fi.Mode().Perm() != 0700) {
			r.Close()
			return nil, errors.New("unsafe backup directory ownership or mode")
		}
		next, err := r.OpenRoot(part)
		if err == nil {
			var opened os.FileInfo
			opened, err = next.Stat(".")
			if err == nil && !os.SameFile(fi, opened) {
				err = errors.New("backup directory changed")
			}
			if err == nil {
				entry, e := r.Lstat(part)
				if e != nil || !entry.IsDir() || entry.Mode()&os.ModeSymlink != 0 || !os.SameFile(fi, entry) {
					err = errors.New("backup directory changed")
				}
			}
		}
		if err != nil {
			r.Close()
			if next != nil {
				next.Close()
			}
			return nil, errors.New("unsafe backup directory traversal")
		}
		// Persist each parent entry before backup can proceed. Sync even when
		// the child already exists: an earlier interrupted initializer may have
		// left the entry visible but not durable. Never repair its attributes.
		if err := syncInitializationParent(r); err != nil {
			r.Close()
			next.Close()
			return nil, errors.New("backup directory initialization sync failed")
		}
		r.Close()
		r = next
		cur = filepath.Join(cur, part)
	}
	if err := ctx.Err(); err != nil {
		r.Close()
		return nil, err
	}
	return r, nil
}

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
