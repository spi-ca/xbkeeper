//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

type fixture struct {
	c      config
	file   string
	base   string
	socket net.Listener
}

func setup(t *testing.T) fixture {
	t.Helper()
	base := t.TempDir()
	for _, d := range []string{"backups", "data"} {
		if err := os.Mkdir(filepath.Join(base, d), 0700); err != nil {
			t.Fatal(err)
		}
	}
	cred := filepath.Join(base, "credentials")
	if err := os.WriteFile(cred, []byte("[client]\npassword=secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(base, "mysql.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	bin := filepath.Join(base, "fake-xtrabackup")
	script := `#!/bin/sh
set -eu
base=$(dirname "$0")
if [ "$#" -eq 2 ] && [ "$1" = --no-defaults ] && [ "$2" = --version ]; then
  if [ -e "$base/backups/fail-version" ]; then echo 'private fake error password=secret' >&2; exit 49; fi
  echo 'fake xtrabackup 8'; exit 0
fi
if [ "$#" -eq 5 ]; then
  [ "$1" = "--defaults-file=$base/credentials" ] || exit 41
  [ "$2" = --backup ] || exit 42
  case "$3" in --target-dir=*) d=${3#--target-dir=} ;; *) exit 43;; esac
  [ "$4" = "--datadir=$base/data" ] && [ "$5" = "--socket=$base/mysql.sock" ] || exit 50
  if [ -e "$base/backups/prefill-target" ]; then echo payload > "$d/foreign"; fi
  [ -z "$(ls -A "$d")" ] || exit 52
  if [ -e "$base/backups/fail-backup" ]; then echo 'private fake error password=secret' >&2; exit 44; fi
  echo 'backup_type = full-backuped' > "$d/xtrabackup_checkpoints"
  printf 'test real payload\n' > "$d/data file-世界"
  exit 0
fi
if [ "$#" -eq 3 ] && [ "$1" = --no-defaults ] && [ "$2" = --prepare ]; then
  case "$3" in --target-dir=*) d=${3#--target-dir=} ;; *) exit 46;; esac
  if [ -e "$base/backups/sleep" ]; then
    sh -c 'trap "" TERM; echo $$ > "$1"; while :; do sleep 1; done' sh "$base/backups/child.pid" &
    echo ready > "$base/backups/ready"
    wait
  fi
  if [ -e "$base/backups/fail-prepare" ]; then echo 'private fake error password=secret' >&2; exit 47; fi
  if [ -e "$base/backups/bad-checkpoint" ]; then echo 'backup_type = incremental' > "$d/xtrabackup_checkpoints"; exit 0; fi
  echo 'backup_type = full-prepared' > "$d/xtrabackup_checkpoints"
  exit 0
fi
exit 48
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	c := config{BackupDir: filepath.Join(base, "backups"), Datadir: filepath.Join(base, "data"), Socket: sock, DefaultsFile: cred, Keep: 1, MinFreeBytes: 0, Xtrabackup: bin}
	file := filepath.Join(base, "config.json")
	f := fixture{c: c, file: file, base: base, socket: l}
	f.save(t)
	return f
}
func (f fixture) save(t *testing.T) {
	t.Helper()
	b, err := json.Marshal(f.c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.file, b, 0600); err != nil {
		t.Fatal(err)
	}
}
func invoke(t *testing.T, f fixture, cmd string) (string, error) {
	t.Helper()
	var b bytes.Buffer
	err := run(context.Background(), []string{cmd, "--config", f.file}, &b)
	return b.String(), err
}
func mark(t *testing.T, f fixture, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.c.BackupDir, name), nil, 0600); err != nil {
		t.Fatal(err)
	}
}
func TestBackupRetentionAndStatus(t *testing.T) {
	f := setup(t)
	one, err := invoke(t, f, "backup")
	if err != nil {
		t.Fatal(err)
	}
	two, err := invoke(t, f, "backup")
	if err != nil {
		t.Fatal(err)
	}
	if one == two {
		t.Fatal("duplicate name")
	}
	s, err := invoke(t, f, "status")
	if err != nil {
		t.Fatal(err)
	}
	var got status
	if err := json.Unmarshal([]byte(s), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Backups) != 1 || got.Backups[0] != strings.TrimSpace(two) || got.LastSuccess != got.Backups[0] || len(got.Incomplete) != 0 {
		t.Fatalf("status: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(f.c.BackupDir, strings.TrimSpace(one))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old backup retained: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.c.BackupDir, got.LastSuccess, "xbkeeper.json")); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadDir(f.c.BackupDir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = invoke(t, f, "status")
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadDir(f.c.BackupDir)
	if len(before) != len(after) {
		t.Fatal("status wrote files")
	}
}
func TestFailuresDoNotPrune(t *testing.T) {
	for _, failure := range []string{"fail-version", "fail-backup", "fail-prepare", "bad-checkpoint"} {
		t.Run(failure, func(t *testing.T) {
			f := setup(t)
			f.c.Keep = 1
			f.save(t)
			first, err := invoke(t, f, "backup")
			if err != nil {
				t.Fatal(err)
			}
			mark(t, f, failure)
			if _, err = invoke(t, f, "backup"); err == nil {
				t.Fatal("expected failure")
			}
			entries, _ := os.ReadDir(f.c.BackupDir)
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".inprogress-") {
					t.Fatal("staging left behind")
				}
			}
			if _, err := os.Stat(filepath.Join(f.c.BackupDir, strings.TrimSpace(first))); err != nil {
				t.Fatalf("existing backup lost: %v", err)
			}
			if failure != "bad-checkpoint" {
				b, err := os.ReadFile(filepath.Join(f.c.BackupDir, ".last-failure.log"))
				if err != nil {
					t.Fatal(err)
				}
				if failure == "fail-backup" && !bytes.Contains(b, []byte("private fake error")) {
					t.Fatal("missing private log")
				}
			}
		})
	}
}
func TestTamperedBackupFailsClosed(t *testing.T) {
	for _, kind := range []string{"manifest", "checkpoint", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			first, err := invoke(t, f, "backup")
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(f.c.BackupDir, strings.TrimSpace(first))
			switch kind {
			case "manifest":
				os.WriteFile(filepath.Join(dir, "xbkeeper.json"), []byte(`{"format":2}`), 0600)
			case "checkpoint":
				os.WriteFile(filepath.Join(dir, "xtrabackup_checkpoints"), []byte("backup_type = incremental"), 0600)
			case "symlink":
				os.Symlink(f.c.DefaultsFile, filepath.Join(dir, "escape"))
			}
			if _, err := invoke(t, f, "status"); err == nil {
				t.Fatal("status accepted tampering")
			}
			if _, err := invoke(t, f, "backup"); err == nil {
				t.Fatal("backup accepted tampering")
			}
			if _, err := os.Lstat(dir); err != nil {
				t.Fatal("tampered backup deleted")
			}
		})
	}
}
func TestConfigValidationAndSpace(t *testing.T) {
	f := setup(t)
	f.c.MinFreeBytes = 1 << 62
	f.save(t)
	if _, err := invoke(t, f, "backup"); err == nil {
		t.Fatal("low space accepted")
	}
	f.c.MinFreeBytes = 0
	f.c.Keep = 0
	f.save(t)
	if _, err := invoke(t, f, "status"); err == nil {
		t.Fatal("zero keep accepted")
	}
	f.c.Keep = 1
	f.c.Datadir = f.c.BackupDir
	f.save(t)
	if _, err := invoke(t, f, "status"); err == nil {
		t.Fatal("overlap accepted")
	}
	f.c.Datadir = "/"
	f.save(t)
	if _, err := invoke(t, f, "status"); err == nil {
		t.Fatal("root datadir overlap accepted")
	}
	f.c.Datadir = filepath.Join(f.base, "data")
	f.save(t)
	for _, data := range []string{`{"keep":1,"keep":2}`, `{"unknown":true}`, `{"keep":1} {}`} {
		os.WriteFile(f.file, []byte(data), 0600)
		if _, err := invoke(t, f, "status"); err == nil {
			t.Fatalf("accepted JSON %s", data)
		}
	}
	f.save(t)
	os.Chmod(f.file, 0644)
	if _, err := invoke(t, f, "status"); err == nil {
		t.Fatal("public config accepted")
	}
	os.Chmod(f.file, 0600)
}
func TestIncompleteAndUnmanagedAreNotPruned(t *testing.T) {
	f := setup(t)
	orphan := ".inprogress-20200101T000000Z-0000000000000000"
	if err := os.Mkdir(filepath.Join(f.c.BackupDir, orphan), 0700); err != nil {
		t.Fatal(err)
	}
	unmanaged := filepath.Join(f.c.BackupDir, "other-data")
	if err := os.Mkdir(unmanaged, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := invoke(t, f, "backup"); err == nil || !strings.Contains(err.Error(), "incomplete staging") {
		t.Fatalf("orphan did not block backup: %v", err)
	}
	s, err := invoke(t, f, "status")
	if err != nil {
		t.Fatal(err)
	}
	var got status
	if err := json.Unmarshal([]byte(s), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Incomplete) != 1 || got.Incomplete[0] != orphan {
		t.Fatalf("incomplete: %+v", got)
	}
	if _, err := os.Stat(unmanaged); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.c.DefaultsFile, filepath.Join(f.c.BackupDir, orphan, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := invoke(t, f, "status"); err == nil {
		t.Fatal("tampered staging accepted")
	}
}
func TestSymlinkAndLock(t *testing.T) {
	f := setup(t)
	link := filepath.Join(f.base, "linked")
	if err := os.Symlink(f.c.BackupDir, link); err != nil {
		t.Fatal(err)
	}
	f.c.BackupDir = link
	f.save(t)
	if _, err := invoke(t, f, "status"); err == nil {
		t.Fatal("symlink backup root accepted")
	}
	f.c.BackupDir = filepath.Join(f.base, "backups")
	f.save(t)
	r, err := os.OpenRoot(f.c.BackupDir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	lk, err := lock(r)
	if err != nil {
		t.Fatal(err)
	}
	defer lk.Close()
	if _, err := invoke(t, f, "backup"); err == nil {
		t.Fatal("concurrent backup accepted")
	}
}
func TestCancellation(t *testing.T) {
	f := setup(t)
	mark(t, f, "sleep")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { var b bytes.Buffer; done <- run(ctx, []string{"backup", "--config", f.file}, &b) }()
	deadline := time.After(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(f.c.BackupDir, "ready")); err == nil {
			if _, err := os.Stat(filepath.Join(f.c.BackupDir, "child.pid")); err == nil {
				break
			}
		}
		select {
		case <-deadline:
			t.Fatal("child not started")
		case <-time.After(10 * time.Millisecond):
		}
	}
	raw, err := os.ReadFile(filepath.Join(f.c.BackupDir, "child.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("child not alive before cancel: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child did not terminate")
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err == nil {
		pos := bytes.LastIndexByte(stat, ')')
		if pos > 0 && strings.Fields(string(stat[pos+1:]))[0] != "Z" {
			t.Fatal("child still running after cancel")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	r, _ := os.OpenRoot(f.c.BackupDir)
	defer r.Close()
	lk, err := lock(r)
	if err != nil {
		t.Fatalf("lock held after cancellation: %v", err)
	}
	lk.Close()
}
func TestVersion(t *testing.T) {
	var b bytes.Buffer
	if err := run(context.Background(), []string{"version"}, &b); err != nil || strings.TrimSpace(b.String()) != Version {
		t.Fatal(err, b.String())
	}
}
func TestFailureLogSymlink(t *testing.T) {
	f := setup(t)
	mark(t, f, "fail-backup")
	os.Symlink(f.c.DefaultsFile, filepath.Join(f.c.BackupDir, ".last-failure.log"))
	if _, err := invoke(t, f, "backup"); err == nil {
		t.Fatal("expected failure")
	}
	b, _ := os.ReadFile(f.c.DefaultsFile)
	if !bytes.Contains(b, []byte("password=secret")) {
		t.Fatal("credentials overwritten")
	}
}
func TestStatusOffline(t *testing.T) {
	f := setup(t)
	f.socket.Close()
	os.Remove(f.c.Xtrabackup)
	os.Remove(f.c.DefaultsFile)
	os.RemoveAll(f.c.Datadir)
	if _, err := invoke(t, f, "status"); err != nil {
		t.Fatal("offline status:", err)
	}
	if _, err := invoke(t, f, "backup"); err == nil {
		t.Fatal("offline backup accepted")
	}
}
func TestAllocatedEstimate(t *testing.T) {
	f := setup(t)
	os.WriteFile(filepath.Join(f.c.Datadir, "data"), []byte("payload"), 0600)
	if err := freeSpace(f.c); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Unlink(filepath.Join(f.c.Datadir, "data")); err != nil {
		t.Fatal(err)
	}
}
