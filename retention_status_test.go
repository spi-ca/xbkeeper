//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestPartialRetentionIsolatedAndRetried(t *testing.T) {
	f := setup(t)
	old := completed(t, f)
	previous := removeRetained
	defer func() { removeRetained = previous }()
	removeRetained = func(_ context.Context, root *os.Root, name string) error {
		if !strings.HasPrefix(name, ".deleting-") {
			t.Fatalf("unexpected removal target %q", name)
		}
		if err := root.Remove(name + "/" + manifestName); err != nil {
			return err
		}
		return syscall.EIO
	}
	out, err := invoke(t, f, "backup")
	if err == nil || !strings.Contains(err.Error(), "retention") || !strings.Contains(out, "Result: failed:") {
		t.Fatalf("retention failure: %q %v", out, err)
	}
	removeRetained = previous
	sJSON, err := invokeStatusJSON(t, f)
	if err != nil {
		t.Fatal(err)
	}
	var s status
	if err := json.Unmarshal([]byte(sJSON), &s); err != nil {
		t.Fatal(err)
	}
	pending := ".deleting-" + strings.TrimPrefix(old, "backup-")
	if len(s.Backups) != 1 || s.LastSuccess != s.Backups[0] || len(s.Incomplete) != 0 || len(s.PendingDeletions) != 1 || s.PendingDeletions[0] != pending {
		t.Fatalf("partial deletion poisoned inventory: %+v", s)
	}
	if err := validBackup(filepath.Join(f.c.BackupDir, s.Backups[0])); err != nil {
		t.Fatal("new backup invalid", err)
	}
	human, err := invoke(t, f, "status")
	if err != nil || !strings.Contains(human, "Pending deletions (1):\n  "+pending) {
		t.Fatalf("human status: %s %v", human, err)
	}
	newest := s.Backups[0]
	if _, err := invoke(t, f, "backup"); err != nil {
		t.Fatal("checked restart retry:", err)
	}
	sJSON, err = invokeStatusJSON(t, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(sJSON), &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Backups) != 1 || len(s.PendingDeletions) != 0 || len(s.Incomplete) != 0 {
		t.Fatalf("retry state: %+v", s)
	}
	for _, name := range []string{pending, newest} {
		if _, err := os.Lstat(filepath.Join(f.c.BackupDir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stale tree %s: %v", name, err)
		}
	}
}

func TestRetentionRenameAndSyncBoundaries(t *testing.T) {
	for _, boundary := range []string{"rename", "sync"} {
		t.Run(boundary, func(t *testing.T) {
			f := setup(t)
			old := completed(t, f)
			originalRename, originalSync := renameRetained, syncRetainedParent
			defer func() { renameRetained, syncRetainedParent = originalRename, originalSync }()
			if boundary == "rename" {
				renameRetained = func(*os.Root, string, string) error { return syscall.EIO }
			} else {
				syncRetainedParent = func(*os.Root) error { return syscall.EIO }
			}
			if _, err := invoke(t, f, "backup"); err == nil || !strings.Contains(err.Error(), "retention") {
				t.Fatal(err)
			}
			s, err := inspect(f.c.BackupDir)
			if err != nil {
				t.Fatal(err)
			}
			if boundary == "rename" && (len(s.Backups) != 2 || len(s.PendingDeletions) != 0) || boundary == "sync" && (len(s.Backups) != 1 || len(s.PendingDeletions) != 1) {
				t.Fatalf("failure boundary %s: %+v", boundary, s)
			}
			oldPath := filepath.Join(f.c.BackupDir, old)
			if boundary == "sync" {
				oldPath = filepath.Join(f.c.BackupDir, s.PendingDeletions[0])
			}
			if err := validBackup(oldPath); err != nil {
				t.Fatal("unlink ran before durable rename:", err)
			}
		})
	}
}

func TestUnsafePendingAndLegacyStageNotPruned(t *testing.T) {
	for _, kind := range []string{"regular-file", "symlink", "unowned-mode", "nested-symlink"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			pending := ".deleting-20200101T000000Z-0000000000000000"
			path := filepath.Join(f.c.BackupDir, pending)
			if kind == "regular-file" {
				if err := os.WriteFile(path, []byte("must not be removed"), 0600); err != nil {
					t.Fatal(err)
				}
			} else if kind == "symlink" {
				if err := os.Symlink(f.c.Datadir, path); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				if kind == "unowned-mode" {
					if err := os.Chmod(path, 0755); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Symlink(f.c.Datadir, filepath.Join(path, "escape")); err != nil {
					t.Fatal(err)
				}
			}
			legacy := ".inprogress-20200101T000000Z-0000000000000001"
			if err := os.Mkdir(filepath.Join(f.c.BackupDir, legacy), 0700); err != nil {
				t.Fatal(err)
			}
			s, err := inspect(f.c.BackupDir)
			if err != nil || len(s.PendingDeletions) != 1 || len(s.Incomplete) != 1 {
				t.Fatalf("status: %+v %v", s, err)
			}
			if _, err := invoke(t, f, "backup"); err == nil || !strings.Contains(err.Error(), "incomplete staging") {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(f.c.BackupDir, legacy)); err != nil {
				t.Fatal(err)
			}
			if _, err := invoke(t, f, "backup"); err == nil || !strings.Contains(err.Error(), "retention") {
				t.Fatalf("unsafe pending accepted: %v", err)
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatal("pending tree removed:", err)
			}
		})
	}
}

func TestRetentionCollisionDoesNotOverwrite(t *testing.T) {
	f := setup(t)
	old := completed(t, f)
	pending := ".deleting-" + strings.TrimPrefix(old, "backup-")
	original := renameRetained
	defer func() { renameRetained = original }()
	renameRetained = func(r *os.Root, from, to string) error {
		if to != pending || from != old {
			t.Fatalf("rename %s -> %s", from, to)
		}
		if err := r.Mkdir(to, 0700); err != nil {
			return err
		}
		return original(r, from, to)
	}
	if _, err := invoke(t, f, "backup"); err == nil || !strings.Contains(err.Error(), "retention") {
		t.Fatal(err)
	}
	if err := validBackup(filepath.Join(f.c.BackupDir, old)); err != nil {
		t.Fatal("old overwritten", err)
	}
	if fi, err := os.Lstat(filepath.Join(f.c.BackupDir, pending)); err != nil || !fi.IsDir() {
		t.Fatalf("collision overwritten: %v %v", fi, err)
	}
}

type cancelAfterChecks struct {
	context.Context
	remaining int
}

func (c *cancelAfterChecks) Err() error {
	c.remaining--
	if c.remaining <= 0 {
		return context.Canceled
	}
	return nil
}

func TestStatusCancellationEnvelopeAndTraversal(t *testing.T) {
	f := setup(t)
	completed(t, f)
	for _, jsonMode := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		args := []string{"status", "--config", f.file}
		if jsonMode {
			args = append(args, "--json")
		}
		var out bytes.Buffer
		err := runWithLog(ctx, args, &out, io.Discard)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("pre-canceled: %v", err)
		}
		if jsonMode {
			var result commandEnvelope
			if e := json.Unmarshal(out.Bytes(), &result); e != nil || result.OK || result.Data != nil || result.Error == nil {
				t.Fatalf("envelope: %q %v", out.String(), e)
			}
		} else if !strings.Contains(out.String(), "Result: failed:") {
			t.Fatal(out.String())
		}
	}
	for _, checks := range []int{3, 12, 30} {
		ctx := &cancelAfterChecks{Context: context.Background(), remaining: checks}
		var out bytes.Buffer
		err := runWithLog(ctx, []string{"status", "--json", "--config", f.file}, &out, io.Discard)
		var result commandEnvelope
		if !errors.Is(err, context.Canceled) || json.Unmarshal(out.Bytes(), &result) != nil || result.OK || result.Data != nil {
			t.Fatalf("inspection after %d checks: %v %s", checks, err, out.String())
		}
	}
	ctx := &cancelAfterChecks{Context: context.Background(), remaining: 5}
	if err := validBackupContext(ctx, filepath.Join(f.c.BackupDir, completedName(t, f.c.BackupDir))); !errors.Is(err, context.Canceled) {
		t.Fatalf("validation cancellation: %v", err)
	}
}

func TestPendingDeletionInsufficientSpaceFailsBeforeRetry(t *testing.T) {
	f := setup(t)
	pending := ".deleting-20200101T000000Z-0000000000000000"
	path := filepath.Join(f.c.BackupDir, pending)
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "payload"), []byte("retain"), 0600); err != nil {
		t.Fatal(err)
	}
	f.c.MinFreeBytes = 1 << 62
	f.save(t)
	if _, err := invoke(t, f, "backup"); err == nil || !strings.Contains(err.Error(), "insufficient free space") {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(path, "payload")); err != nil {
		t.Fatal("preflight deleted pending tree:", err)
	}
}

func completedName(t *testing.T, root string) string {
	t.Helper()
	s, err := inspect(root)
	if err != nil || len(s.Backups) != 1 {
		t.Fatalf("inventory: %+v %v", s, err)
	}
	return s.Backups[0]
}
