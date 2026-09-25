//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestMissingBackupRootCLI(t *testing.T) {
	f := setup(t)
	if err := os.Remove(f.c.BackupDir); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"status", "verify", "status --json"} {
		var out bytes.Buffer
		args := []string{command, "--config", f.file}
		if command == "status --json" {
			args = []string{"status", "--json", "--config", f.file}
		}
		err := run(context.Background(), args, &out)
		if err == nil || !strings.Contains(err.Error(), "backup directory missing; run backup to initialize it") || out.Len() != 0 {
			t.Fatalf("%s: %q %v", command, out.String(), err)
		}
		if _, err := os.Lstat(f.c.BackupDir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("readonly %s mutated root: %v", command, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	if err := run(ctx, []string{"backup", "--config", f.file}, &out); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled backup: %v", err)
	}
	if _, err := os.Lstat(f.c.BackupDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled backup mutated root: %v", err)
	}
	if _, err := invoke(t, f, "backup"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(f.c.BackupDir)
	if err != nil || fi.Mode().Perm() != 0700 {
		t.Fatalf("created root: %v %v", fi, err)
	}
	if uid, ok := owner(fi); !ok || int(uid) != os.Geteuid() {
		t.Fatalf("created root owner: %v", fi)
	}
}

func TestBackupInitializesMissingParentsThroughCLI(t *testing.T) {
	f := setup(t)
	bin, err := os.ReadFile(f.c.Xtrabackup)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.ReplaceAll(string(bin), "$base/backups", "$base/new-parent/backups")
	if updated == string(bin) {
		t.Fatal("fixture command did not reference root")
	}
	if err := os.WriteFile(f.c.Xtrabackup, []byte(updated), 0700); err != nil {
		t.Fatal(err)
	}
	f.c.BackupDir = filepath.Join(f.base, "new-parent", "backups")
	f.save(t)
	if _, err := invoke(t, f, "backup"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"new-parent", "new-parent/backups"} {
		fi, err := os.Lstat(filepath.Join(f.base, name))
		if err != nil || fi.Mode().Perm() != 0700 {
			t.Fatalf("%s: %v %v", name, fi, err)
		}
	}
}

func TestMissingBackupParentsAndUnsafeEntries(t *testing.T) {
	f := setup(t)
	base := filepath.Join(f.base, "missing")
	path := filepath.Join(base, "backups")
	for _, kind := range []string{"missing", "symlink parent", "file parent", "symlink leaf", "wrong leaf mode"} {
		t.Run(kind, func(t *testing.T) {
			_ = os.RemoveAll(base)
			switch kind {
			case "symlink parent":
				if err := os.Symlink(f.c.BackupDir, base); err != nil {
					t.Fatal(err)
				}
			case "file parent":
				if err := os.WriteFile(base, nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink leaf":
				if err := os.Mkdir(base, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(f.c.BackupDir, path); err != nil {
					t.Fatal(err)
				}
			case "wrong leaf mode":
				if err := os.Mkdir(base, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0750); err != nil {
					t.Fatal(err)
				}
			}
			f.c.BackupDir = path
			f.save(t)
			_, err := loadConfig(f.file)
			if kind == "missing" {
				if err != nil {
					t.Fatal(err)
				}
				r, err := openBackupRoot(context.Background(), path)
				if err != nil {
					t.Fatal(err)
				}
				r.Close()
				for _, p := range []string{base, path} {
					fi, err := os.Lstat(p)
					if err != nil || fi.Mode().Perm() != 0700 {
						t.Fatalf("%s: %v %v", p, fi, err)
					}
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), "unsafe backup directory") {
					t.Fatalf("accepted %s: %v", kind, err)
				}
				if _, err := openBackupRoot(context.Background(), path); err == nil {
					t.Fatal("created under unsafe entry")
				}
			}
		})
	}
}

func TestExistingAncestorIsNotRepaired(t *testing.T) {
	f := setup(t)
	parent := filepath.Join(f.base, "existing-parent")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0750); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "backups")
	r, err := openBackupRoot(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	fi, err := os.Stat(parent)
	if err != nil || fi.Mode().Perm() != 0750 {
		t.Fatalf("existing parent repaired: %v %v", fi, err)
	}
	fi, err = os.Stat(root)
	if err != nil || fi.Mode().Perm() != 0700 {
		t.Fatalf("new leaf: %v %v", fi, err)
	}
}

func TestConfigDiagnosticCategoriesAreFixed(t *testing.T) {
	f := setup(t)
	missing := filepath.Join(f.base, "sensitive-config.toml")
	for _, tc := range []struct {
		name   string
		change func()
		want   string
	}{
		{"missing file", func() { f.file = missing }, "config file missing"},
		{"invalid TOML", func() { _ = os.WriteFile(f.file, []byte("keep = 'password-secret'\n"), 0600) }, "invalid config TOML, keys or schema"},
		{"unsafe permissions", func() { _ = os.Chmod(f.file, 0644) }, "unsafe or unreadable config file permissions or path"},
		{"unsafe root", func() {
			f.c.BackupDir = filepath.Join(f.base, "unsafe-sentinel")
			_ = os.Symlink(f.c.Datadir, f.c.BackupDir)
			f.save(t)
		}, "unsafe backup directory path, ownership or permissions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f = setup(t)
			tc.change()
			var out bytes.Buffer
			err := run(context.Background(), []string{"status", "--config", f.file}, &out)
			if err == nil || err.Error() != "config: "+tc.want || out.Len() != 0 || strings.Contains(err.Error(), "sensitive-config") || strings.Contains(err.Error(), "password-secret") || strings.Contains(err.Error(), "unsafe-sentinel") {
				t.Fatalf("diagnostic: %v, output %q", err, out.String())
			}
		})
	}
}

func TestExistingBackupRootAttributesAndConcurrentInitialization(t *testing.T) {
	f := setup(t)
	root := f.c.BackupDir
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := openBackupRoot(context.Background(), root); err == nil {
		t.Fatal("accepted public root")
	}
	fi, _ := os.Stat(root)
	if fi.Mode().Perm() != 0755 {
		t.Fatal("changed existing mode")
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errorsSeen := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := openBackupRoot(context.Background(), root)
			if err == nil {
				err = r.Close()
			}
			errorsSeen <- err
		}()
	}
	wg.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	fi, err := os.Lstat(root)
	if err != nil || fi.Mode().Perm() != 0700 {
		t.Fatalf("concurrent root: %v %v", fi, err)
	}
}

func TestInitializationSyncFailureAndRetry(t *testing.T) {
	f := setup(t)
	if err := os.Remove(f.c.BackupDir); err != nil {
		t.Fatal(err)
	}
	parentInfo, err := os.Stat(f.base)
	if err != nil {
		t.Fatal(err)
	}
	original := syncInitializationParent
	defer func() { syncInitializationParent = original }()
	fail := true
	parentSyncs := 0
	syncInitializationParent = func(r *os.Root) error {
		fi, err := r.Stat(".")
		if err != nil {
			return err
		}
		if os.SameFile(fi, parentInfo) {
			parentSyncs++
			if fail {
				return errors.New("private-sync-detail")
			}
		}
		return original(r)
	}
	for attempt := 0; attempt < 2; attempt++ {
		// Second attempt must sync an already-existing entry left by the first one.
		out, err := invoke(t, f, "backup")
		if err == nil || err.Error() != "validation: backup directory initialization sync failed; backup not started" || out != "" {
			t.Fatalf("attempt %d: %q %v", attempt, out, err)
		}
		entries, err := os.ReadDir(f.c.BackupDir)
		if err != nil || len(entries) != 0 {
			t.Fatalf("backup started after sync failure: %v %v", entries, err)
		}
	}
	if parentSyncs != 2 {
		t.Fatalf("parent synced %d times, want 2", parentSyncs)
	}
	fail = false
	if _, err := invoke(t, f, "backup"); err != nil {
		t.Fatal(err)
	}
	if parentSyncs != 3 {
		t.Fatal("successful retry did not sync the parent")
	}
}

func TestInitializationSyncFailurePreservesExistingBackup(t *testing.T) {
	f := setup(t)
	old := completed(t, f)
	original := syncInitializationParent
	defer func() { syncInitializationParent = original }()
	syncInitializationParent = func(*os.Root) error { return errors.New("private-sync-detail") }
	if _, err := invoke(t, f, "backup"); err == nil || strings.Contains(err.Error(), "private-sync-detail") {
		t.Fatalf("unexpected initialization result: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.c.BackupDir, old)); err != nil {
		t.Fatal(err)
	}
	s, err := inspect(f.c.BackupDir)
	if err != nil || len(s.Backups) != 1 || len(s.Incomplete) != 0 {
		t.Fatalf("backup state changed: %+v %v", s, err)
	}
}

func TestCancellationDuringInitializationSync(t *testing.T) {
	f := setup(t)
	if err := os.Remove(f.c.BackupDir); err != nil {
		t.Fatal(err)
	}
	parentInfo, err := os.Stat(f.base)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	original := syncInitializationParent
	defer func() { syncInitializationParent = original }()
	syncInitializationParent = func(r *os.Root) error {
		fi, err := r.Stat(".")
		if err != nil {
			return err
		}
		if os.SameFile(fi, parentInfo) {
			cancel()
		}
		return original(r)
	}
	var out bytes.Buffer
	if err := run(ctx, []string{"backup", "--config", f.file}, &out); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(f.c.BackupDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("canceled initialization started backup: %v %v", entries, err)
	}
}
