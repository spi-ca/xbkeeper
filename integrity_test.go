//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func audit(t *testing.T, f fixture, name string) (verifyReport, error) {
	t.Helper()
	args := []string{"verify", "--config", f.file}
	if name != "" {
		args = append(args, "--backup", name)
	}
	var out bytes.Buffer
	err := runWithLog(context.Background(), args, &out, &bytes.Buffer{})
	var report verifyReport
	if out.Len() != 0 {
		if e := json.Unmarshal(out.Bytes(), &report); e != nil {
			t.Fatal(e, out.String())
		}
	}
	return report, err
}
func completed(t *testing.T, f fixture) string {
	t.Helper()
	s, e := invoke(t, f, "backup")
	if e != nil {
		t.Fatal(e)
	}
	return strings.TrimSpace(s)
}
func TestBuiltCLISmoke(t *testing.T) {
	f := setup(t)
	bin := filepath.Join(t.TempDir(), "xbkeeper")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if b, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("build: %v %s", e, b)
	}
	call := func(args ...string) (string, string, error) {
		c := exec.Command(bin, args...)
		var stdout, stderr bytes.Buffer
		c.Stdout = &stdout
		c.Stderr = &stderr
		e := c.Run()
		return stdout.String(), stderr.String(), e
	}
	name, _, e := call("backup", "--config", f.file)
	if e != nil {
		t.Fatal("CLI backup:", e)
	}
	name = strings.TrimSpace(name)
	status, _, e := call("status", "--config", f.file)
	if e != nil || !strings.Contains(status, name) {
		t.Fatalf("CLI status: %v %s", e, status)
	}
	good, _, e := call("verify", "--config", f.file)
	if e != nil || !strings.Contains(good, `"ok":true`) || !strings.Contains(good, `"files":3`) {
		t.Fatalf("CLI verify: %v %s", e, good)
	}
	if e = os.WriteFile(filepath.Join(f.c.BackupDir, name, "data file-世界"), []byte("changed payload"), 0600); e != nil {
		t.Fatal(e)
	}
	bad, _, e := call("verify", "--config", f.file, "--backup", name)
	if e == nil || !strings.Contains(bad, `"ok":false`) {
		t.Fatalf("CLI corrupt verify: %v %s", e, bad)
	}
	t.Logf("built CLI backup=%s status=%s verify=%s corrupt=%s", name, strings.TrimSpace(status), strings.TrimSpace(good), strings.TrimSpace(bad))
}
func TestVerifyTamperAndIsolation(t *testing.T) {
	for _, kind := range []string{"added", "removed", "payload", "metadata", "checkpoint", "manifest duplicate", "manifest traversal", "manifest self", "manifest digest", "manifest missing", "symlink", "hardlink", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			f.c.Keep = 2
			f.save(t)
			good := completed(t, f)
			bad := completed(t, f)
			dir := filepath.Join(f.c.BackupDir, bad)
			payload := filepath.Join(dir, "data file-世界")
			manifest := filepath.Join(dir, manifestName)
			b, e := os.ReadFile(manifest)
			if e != nil {
				t.Fatal(e)
			}
			switch kind {
			case "added":
				e = os.WriteFile(filepath.Join(dir, "extra"), []byte("x"), 0600)
			case "removed":
				e = os.Remove(payload)
			case "payload":
				e = os.WriteFile(payload, []byte("corrupt"), 0600)
			case "metadata":
				meta, readErr := os.ReadFile(filepath.Join(dir, "xbkeeper.json"))
				if readErr != nil {
					t.Fatal(readErr)
				}
				meta = bytes.Replace(meta, []byte("fake xtrabackup 8"), []byte("fake xtrabackup 9"), 1)
				e = os.WriteFile(filepath.Join(dir, "xbkeeper.json"), meta, 0600)
			case "checkpoint":
				e = os.WriteFile(filepath.Join(dir, "xtrabackup_checkpoints"), []byte("backup_type = full-prepared\nextra = changed\n"), 0600)
			case "manifest duplicate":
				e = os.WriteFile(manifest, append(b, b...), 0600)
			case "manifest traversal":
				e = os.WriteFile(manifest, []byte(strings.Repeat("a", 64)+"  ../escape\n"), 0600)
			case "manifest self":
				e = os.WriteFile(manifest, []byte(strings.Repeat("a", 64)+"  SHA256SUMS\n"), 0600)
			case "manifest digest":
				e = os.WriteFile(manifest, []byte("BAD"+string(b[3:])), 0600)
			case "manifest missing":
				e = os.Remove(manifest)
			case "symlink":
				e = os.Symlink(f.c.DefaultsFile, filepath.Join(dir, "extra"))
			case "hardlink":
				e = os.Link(payload, filepath.Join(dir, "extra"))
			case "fifo":
				e = syscall.Mkfifo(filepath.Join(dir, "extra"), 0600)
			}
			if e != nil {
				t.Fatal(e)
			}
			report, e := audit(t, f, good)
			if e != nil || len(report.Backups) != 1 || !report.Backups[0].OK {
				t.Fatalf("isolated selected: %+v %v", report, e)
			}
			report, e = audit(t, f, "")
			if e == nil || len(report.Backups) != 2 || report.Backups[0].OK == report.Backups[1].OK {
				t.Fatalf("all results: %+v %v", report, e)
			}
		})
	}
}
func TestVerifyLockOfflineLegacyAndPaths(t *testing.T) {
	f := setup(t)
	n := completed(t, f)
	dir := filepath.Join(f.c.BackupDir, n)
	// Verify must not touch a live server, credentials or executable.
	f.socket.Close()
	os.Remove(f.c.Socket)
	os.Remove(f.c.DefaultsFile)
	os.Remove(f.c.Xtrabackup)
	os.RemoveAll(f.c.Datadir)
	snapshots := make(map[string]struct {
		data []byte
		mod  time.Time
	})
	for _, name := range []string{"xbkeeper.json", "xtrabackup_checkpoints", "data file-世界", manifestName} {
		p := filepath.Join(dir, name)
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		snapshots[name] = struct {
			data []byte
			mod  time.Time
		}{data, fi.ModTime()}
	}
	r, e := audit(t, f, n)
	if e != nil || !r.Backups[0].OK {
		t.Fatal(r, e)
	}
	for name, before := range snapshots {
		p := filepath.Join(dir, name)
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before.data, data) || !before.mod.Equal(fi.ModTime()) {
			t.Fatal("verify modified", name)
		}
	}
	root, e := os.OpenRoot(f.c.BackupDir)
	if e != nil {
		t.Fatal(e)
	}
	defer root.Close()
	a, e := sharedLock(root)
	if e != nil {
		t.Fatal(e)
	}
	b, e := sharedLock(root)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = lock(root); e == nil {
		t.Fatal("backup acquired shared-held lock")
	}
	a.Close()
	b.Close()
	exclusive, e := lock(root)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = audit(t, f, n); e == nil {
		t.Fatal("verify acquired exclusive-held lock")
	}
	exclusive.Close()
	for _, invalid := range []string{"../" + n, ".inprogress-20200101T000000Z-0000000000000000", "/tmp/escape", n + "/x"} {
		if _, e = audit(t, f, invalid); e == nil {
			t.Fatal("accepted", invalid)
		}
	}
	data, e := os.ReadFile(filepath.Join(dir, "xbkeeper.json"))
	if e != nil {
		t.Fatal(e)
	}
	data = bytes.Replace(data, []byte(`"format": 2`), []byte(`"format": 1`), 1)
	if e = os.WriteFile(filepath.Join(dir, "xbkeeper.json"), data, 0600); e != nil {
		t.Fatal(e)
	}
	r, e = audit(t, f, n)
	if e == nil || r.Backups[0].Reason != reasonLegacy {
		t.Fatal(r, e)
	}
	if _, e = invoke(t, f, "status"); e != nil {
		t.Fatal("legacy status:", e)
	}
}
func TestUnsafeLocksNoHang(t *testing.T) {
	for _, kind := range []string{"absent", "fifo", "symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			p := filepath.Join(f.c.BackupDir, ".lock")
			switch kind {
			case "fifo":
				if e := syscall.Mkfifo(p, 0600); e != nil {
					t.Fatal(e)
				}
			case "symlink":
				if e := os.Symlink(f.file, p); e != nil {
					t.Fatal(e)
				}
			case "hardlink":
				if e := os.WriteFile(p, nil, 0600); e != nil {
					t.Fatal(e)
				}
				if e := os.Link(p, filepath.Join(f.c.BackupDir, "other")); e != nil {
					t.Fatal(e)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { var out bytes.Buffer; done <- run(ctx, []string{"verify", "--config", f.file}, &out) }()
			select {
			case e := <-done:
				if e == nil {
					t.Fatal("unsafe lock accepted")
				}
			case <-ctx.Done():
				t.Fatal("lock open blocked")
			}
			if kind != "absent" {
				if _, e := invoke(t, f, "backup"); e == nil {
					t.Fatal("backup accepted unsafe lock")
				}
			}
		})
	}
}
func TestHashFailurePreservesStage(t *testing.T) {
	for _, failure := range []string{"error", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			f := setup(t)
			old := completed(t, f)
			prev := generateStaged
			defer func() { generateStaged = prev }()
			generateStaged = func(ctx context.Context, r *os.Root) ([]byte, error) {
				if failure == "cancel" {
					return nil, context.Canceled
				}
				return nil, errors.New("private hash error")
			}
			_, e := invoke(t, f, "backup")
			if e == nil || strings.Contains(e.Error(), "private") {
				t.Fatal(e)
			}
			if _, e = os.Stat(filepath.Join(f.c.BackupDir, old)); e != nil {
				t.Fatal("old backup removed", e)
			}
			entries, _ := os.ReadDir(f.c.BackupDir)
			found := false
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".inprogress-") {
					found = true
					if _, e := os.Stat(filepath.Join(f.c.BackupDir, entry.Name(), "xbkeeper.json")); e != nil {
						t.Fatal(e)
					}
				}
			}
			if !found {
				t.Fatal("prepared stage lost")
			}
		})
	}
}
func TestManifestGNUAndNames(t *testing.T) {
	f := setup(t)
	n := completed(t, f)
	dir := filepath.Join(f.c.BackupDir, n)
	cmd := exec.Command("sha256sum", "-c", manifestName)
	cmd.Dir = dir
	if b, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("GNU sha256sum: %v %s", e, b)
	}
	root, e := os.OpenRoot(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer root.Close()
	for _, invalid := range []string{"../x", "/absolute", "a/./b", "a\\b", "a\nb", "a\rb", string([]byte{0xff})} {
		if validRelative(invalid) {
			t.Fatal("accepted", fmt.Sprintf("%q", invalid))
		}
	}
	for _, name := range []string{"fine space", "世界"} {
		if !validRelative(name) {
			t.Fatal(name)
		}
	}
	if _, e = parseManifest(context.Background(), root); e != nil {
		t.Fatal(e)
	}
}
