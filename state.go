//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

func safeTree(path string) error { return safeTreeContext(context.Background(), path) }
func safeTreeContext(ctx context.Context, path string) error {
	return filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		uid, ok := owner(fi)
		if !ok || int(uid) != os.Geteuid() || !(fi.IsDir() || fi.Mode().IsRegular()) || fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe backup entry: %q", p)
		}
		if fi.IsDir() && fi.Mode().Perm()&0077 != 0 || fi.Mode().IsRegular() && fi.Mode().Perm()&0077 != 0 {
			return fmt.Errorf("non-private backup entry: %q", p)
		}
		if fi.Mode().IsRegular() && fi.Sys().(*syscall.Stat_t).Nlink != 1 {
			return fmt.Errorf("hardlinked backup entry: %q", p)
		}
		return nil
	})
}
func checkpoint(path string) error {
	r, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer r.Close()
	return checkpointRoot(context.Background(), r)
}
func checkpointRoot(ctx context.Context, r *os.Root) error {
	b, err := readControl(ctx, r, "xtrabackup_checkpoints", 64*1024)
	if err != nil {
		return fmt.Errorf("checkpoints: %w", err)
	}
	if len(b) > 64*1024 {
		return errors.New("checkpoints too large")
	}
	count := 0
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.SplitN(line, "=", 2)
		if len(fields) == 2 && strings.TrimSpace(fields[0]) == "backup_type" {
			count++
			if strings.TrimSpace(fields[1]) != "full-prepared" {
				return errors.New("backup not full-prepared")
			}
		}
	}
	if count != 1 {
		return errors.New("missing or duplicate backup_type in checkpoints")
	}
	return nil
}
func validBackup(path string) error { return validBackupContext(context.Background(), path) }
func validBackupContext(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !safeEntry(fi, true) {
		return errors.New("unsafe backup directory")
	}
	r, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer r.Close()
	opened, err := r.Stat(".")
	if err != nil || !sameSnapshot(fi, opened) {
		return errors.New("backup directory changed")
	}
	if err := validBackupRoot(ctx, r); err != nil {
		return err
	}
	after, err := os.Lstat(path)
	if err != nil || !sameSnapshot(fi, after) {
		return errors.New("backup directory changed")
	}
	return nil
}
func validBackupRoot(ctx context.Context, r *os.Root) error {
	if _, err := listFiles(ctx, r); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkpointRoot(ctx, r); err != nil {
		return err
	}
	b, err := readControl(ctx, r, "xbkeeper.json", 64*1024)
	if err != nil {
		return err
	}
	if len(b) > 64*1024 {
		return errors.New("metadata too large")
	}
	var m metadata
	if err := strictJSON(b, &m); err != nil {
		return err
	}
	if (m.Format != 1 && m.Format != formatVersion) || m.XtrabackupVersion == "" {
		return errors.New("invalid backup metadata")
	}
	start, e1 := time.Parse(time.RFC3339Nano, m.CreatedAt)
	end, e2 := time.Parse(time.RFC3339Nano, m.PreparedAt)
	if e1 != nil || e2 != nil || end.IsZero() || start.IsZero() {
		return errors.New("invalid backup timestamps")
	}
	if m.Format == 2 {
		if err := structuralManifest(ctx, r); err != nil {
			return err
		}
	}
	return nil
}
func inspect(root string) (status, error) { return inspectContext(context.Background(), root) }
func inspectContext(ctx context.Context, root string) (status, error) {
	s := status{Backups: []string{}, Incomplete: []string{}, PendingDeletions: []string{}}
	if err := ctx.Err(); err != nil {
		return s, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return s, err
	}
	prepared := make(map[string]time.Time)
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return s, err
		}
		name := e.Name()
		if !managedName.MatchString(name) {
			continue
		}
		if strings.HasPrefix(name, ".deleting-") {
			// A rename out of the completed namespace precedes any unlink.
			// A partial deletion cannot be validated as a completed backup.
			s.PendingDeletions = append(s.PendingDeletions, name)
			continue
		}
		path := filepath.Join(root, name)
		if strings.HasPrefix(name, "backup-") {
			if err := validBackupContext(ctx, path); err != nil {
				return s, fmt.Errorf("invalid managed backup %q: %w", name, err)
			}
			if err := ctx.Err(); err != nil {
				return s, err
			}
			b, err := os.ReadFile(filepath.Join(path, "xbkeeper.json"))
			if err != nil {
				return s, err
			}
			var m metadata
			if err := strictJSON(b, &m); err != nil {
				return s, err
			}
			if err := ctx.Err(); err != nil {
				return s, err
			}
			prepared[name], _ = time.Parse(time.RFC3339Nano, m.PreparedAt)
			s.Backups = append(s.Backups, name)
		} else {
			if err := safeTreeContext(ctx, path); err != nil {
				return s, fmt.Errorf("invalid staging %q: %w", name, err)
			}
			s.Incomplete = append(s.Incomplete, name)
		}
	}
	sort.Slice(s.Backups, func(i, j int) bool {
		a, b := s.Backups[i], s.Backups[j]
		if prepared[a].Equal(prepared[b]) {
			return a > b
		}
		return prepared[a].After(prepared[b])
	})
	sort.Strings(s.Incomplete)
	sort.Strings(s.PendingDeletions)
	if err := ctx.Err(); err != nil {
		return s, err
	}
	if len(s.Backups) > 0 {
		s.LastSuccess = s.Backups[0]
	}
	return s, nil
}
