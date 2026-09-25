//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUnsupportedStageNamesPreserved(t *testing.T) {
	for _, name := range []string{"bad\\name", "bad\nname", "bad\rname", string([]byte{0xff})} {
		t.Run("name", func(t *testing.T) {
			f := setup(t)
			old := completed(t, f)
			previous := generateStaged
			generateStaged = func(ctx context.Context, r *os.Root) ([]byte, error) {
				if err := os.WriteFile(filepath.Join(r.Name(), name), []byte("data"), 0600); err != nil {
					return nil, err
				}
				return generateManifest(ctx, r)
			}
			defer func() { generateStaged = previous }()
			_, err := invoke(t, f, "backup")
			if err == nil {
				t.Fatal("unsupported path accepted")
			}
			if _, err = os.Stat(filepath.Join(f.c.BackupDir, old)); err != nil {
				t.Fatal(err)
			}
			entries, _ := os.ReadDir(f.c.BackupDir)
			found := false
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), "inprogress-") || strings.HasPrefix(entry.Name(), ".inprogress-") {
					found = true
				}
			}
			if !found {
				t.Fatal("hash failure removed prepared stage")
			}
		})
	}
}
func TestStatusStructuralNotContentAndLegacyRetention(t *testing.T) {
	f := setup(t)
	f.c.Keep = 2
	f.save(t)
	legacy := completed(t, f)
	dir := filepath.Join(f.c.BackupDir, legacy)
	metadataPath := filepath.Join(dir, "xbkeeper.json")
	raw, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.Replace(raw, []byte(`"format": 2`), []byte(`"format": 1`), 1)
	if err = os.WriteFile(metadataPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = invoke(t, f, "status"); err != nil {
		t.Fatal("format1 status:", err)
	}
	newer := completed(t, f)
	if _, err = os.Stat(dir); err != nil {
		t.Fatal("legacy retention:", err)
	}
	content := filepath.Join(f.c.BackupDir, newer, "data file-世界")
	if err = os.WriteFile(content, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = invoke(t, f, "status"); err != nil {
		t.Fatal("status should not hash:", err)
	}
	report, err := audit(t, f, "")
	if err == nil || len(report.Backups) != 2 {
		t.Fatal(report, err)
	}
	for _, result := range report.Backups {
		if result.OK {
			t.Fatal("tamper accepted", result)
		}
	}
}
func TestVerifyEmptyAndOptionScope(t *testing.T) {
	f := setup(t)
	if _, err := audit(t, f, ""); err == nil {
		t.Fatal("empty audit succeeded")
	}
	var out bytes.Buffer
	if err := run(context.Background(), []string{"status", "--config", f.file, "--backup", "backup-20200101T000000Z-0000000000000000"}, &out); err == nil {
		t.Fatal("status accepted --backup")
	}
}
func TestCancellationAtPromotionBoundaries(t *testing.T) {
	for _, point := range []string{"stage-sync", "parent-sync"} {
		t.Run(point, func(t *testing.T) {
			f := setup(t)
			old := completed(t, f)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			previousStage, previousParent := syncStaged, syncPromotedParent
			t.Cleanup(func() { syncStaged, syncPromotedParent = previousStage, previousParent })
			if point == "stage-sync" {
				syncStaged = func(r *os.Root, name string) error {
					err := syncTree(r, name)
					cancel()
					return err
				}
			} else {
				syncPromotedParent = func(r *os.Root) error {
					err := syncRoot(r)
					cancel()
					return err
				}
			}
			var out, logs bytes.Buffer
			err := runWithLog(ctx, []string{"backup", "--config", f.file}, &out, &logs)
			if !errors.Is(err, context.Canceled) || out.Len() != 0 {
				t.Fatalf("cancelled backup: %v stdout=%q", err, out.String())
			}
			if _, err := os.Stat(filepath.Join(f.c.BackupDir, old)); err != nil {
				t.Fatalf("old backup pruned after cancellation: %v", err)
			}
			state, err := inspect(f.c.BackupDir)
			if err != nil {
				t.Fatal(err)
			}
			if point == "stage-sync" && (len(state.Backups) != 1 || len(state.Incomplete) != 1) {
				t.Fatalf("cancelled before promotion: %+v", state)
			}
			if point == "parent-sync" && (len(state.Backups) != 2 || len(state.Incomplete) != 0) {
				t.Fatalf("cancelled after promotion: %+v", state)
			}
		})
	}
}

func TestManifestBoundsAndCancellation(t *testing.T) {
	f := setup(t)
	n := completed(t, f)
	dir := filepath.Join(f.c.BackupDir, n)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	manifest := filepath.Join(dir, manifestName)
	if err := os.WriteFile(manifest, bytes.Repeat([]byte{'x'}, maxManifest+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = parseManifest(context.Background(), root); err == nil {
		t.Fatal("unbounded manifest accepted")
	}
	if err := os.WriteFile(manifest, bytes.Repeat([]byte{'\n'}, maxManifest), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = parseManifest(context.Background(), root); err == nil {
		t.Fatal("dense empty manifest lines accepted")
	}
	if err := os.Remove(manifest); err != nil {
		t.Fatal(err)
	}
	// Sparse large file gives a real streaming read without filling memory/disk.
	fpath := filepath.Join(dir, "large")
	file, err := os.OpenFile(fpath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Truncate(256 << 20); err != nil {
		t.Fatal(err)
	}
	file.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := hashFile(ctx, root, "large"); done <- err }()
	time.Sleep(5 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("hash cancellation: %v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("hash did not cancel")
	}
	data, err := generateManifest(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, data, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	var out bytes.Buffer
	done = make(chan error, 1)
	go func() {
		done <- runWithLog(ctx, []string{"verify", "--config", f.file, "--backup", n}, &out, &bytes.Buffer{})
	}()
	time.Sleep(5 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || out.Len() != 0 {
			t.Fatalf("verify cancellation: %v %q", err, out.String())
		}
	case <-time.After(4 * time.Second):
		t.Fatal("verify did not cancel")
	}
}
