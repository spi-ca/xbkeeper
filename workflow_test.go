//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRetentionCountsAndRollback(t *testing.T) {
	for _, keep := range []int{1, 2, 3, 5} {
		t.Run(string(rune('0'+keep)), func(t *testing.T) {
			f := setup(t)
			f.c.Keep = keep
			f.save(t)
			for i := 0; i < 6; i++ {
				result, err := invoke(t, f, "backup")
				if err != nil {
					t.Fatal(i, err)
				}
				stateJSON, err := invokeStatusJSON(t, f)
				if err != nil {
					t.Fatal(err)
				}
				var s status
				if err = json.Unmarshal([]byte(stateJSON), &s); err != nil {
					t.Fatal(err)
				}
				want := i + 1
				if want > keep {
					want = keep
				}
				if len(s.Backups) != want {
					t.Fatalf("after %d backups keep %d got %+v", i+1, keep, s)
				}
				found := false
				for _, name := range s.Backups {
					if name == strings.TrimSpace(result) {
						found = true
					}
				}
				if !found {
					t.Fatal("just completed backup pruned")
				}
			}
		})
	}
	f := setup(t)
	f.c.Keep = 1
	f.save(t)
	first, err := invoke(t, f, "backup")
	if err != nil {
		t.Fatal(err)
	}
	previous := now
	now = func() time.Time { return time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC) }
	defer func() { now = previous }()
	second, err := invoke(t, f, "backup")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(f.c.BackupDir, strings.TrimSpace(second))); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(f.c.BackupDir, strings.TrimSpace(first))); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("older backup retained", err)
	}
	var m metadata
	b, err := os.ReadFile(filepath.Join(f.c.BackupDir, strings.TrimSpace(second), "xbkeeper.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m.CreatedAt != m.PreparedAt {
		t.Fatalf("clock injection not applied: %+v", m)
	}
}
func TestClockRollsBackDuringPrepare(t *testing.T) {
	f := setup(t)
	prev := now
	defer func() { now = prev }()
	calls := 0
	now = func() time.Time {
		calls++
		if calls >= 3 {
			return time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
		}
		return time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	result, err := invoke(t, f, "backup")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(f.c.BackupDir, strings.TrimSpace(result), "xbkeeper.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m metadata
	if err = json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	start, _ := time.Parse(time.RFC3339Nano, m.CreatedAt)
	end, _ := time.Parse(time.RFC3339Nano, m.PreparedAt)
	if !end.Before(start) {
		t.Fatal("clock rollback was not recorded honestly")
	}
	if _, err = invokeStatusJSON(t, f); err != nil {
		t.Fatal(err)
	}
}
func TestRetentionErrorPreservesCompleted(t *testing.T) {
	f := setup(t)
	f.c.Keep = 1
	f.save(t)
	first, err := invoke(t, f, "backup")
	if err != nil {
		t.Fatal(err)
	}
	prev := removeRetained
	removeRetained = func(*os.Root, string) error { return errors.New("secret retention failure") }
	defer func() { removeRetained = prev }()
	result, err := invoke(t, f, "backup")
	if err == nil || !strings.Contains(err.Error(), "retention") || strings.Contains(err.Error(), "secret") || result != "" {
		t.Fatalf("result=%q error=%v", result, err)
	}
	s, err := invokeStatusJSON(t, f)
	if err != nil {
		t.Fatal(err)
	}
	var got status
	if err = json.Unmarshal([]byte(s), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Backups) != 2 {
		t.Fatalf("retention error deleted a backup: %+v", got)
	}
	if _, err = os.Stat(filepath.Join(f.c.BackupDir, strings.TrimSpace(first))); err != nil {
		t.Fatal(err)
	}
}
func TestFailureRedactionAndNoCommandLeftovers(t *testing.T) {
	for _, failure := range []string{"fail-version", "fail-backup", "fail-prepare", "prefill-target"} {
		t.Run(failure, func(t *testing.T) {
			f := setup(t)
			mark(t, f, failure)
			var out, logs bytes.Buffer
			err := runWithLog(context.Background(), []string{"backup", "--config", f.file}, &out, &logs)
			if err == nil || out.Len() != 0 || bytes.Contains([]byte(err.Error()), []byte("secret")) || bytes.Contains(logs.Bytes(), []byte("secret")) {
				t.Fatalf("error=%v stdout=%q logs=%q", err, out.String(), logs.String())
			}
			phase := strings.TrimPrefix(failure, "fail-")
			if failure == "prefill-target" {
				phase = "backup"
			}
			if !strings.Contains(err.Error(), phase) || !strings.Contains(logs.String(), "backup_id=") || !strings.Contains(logs.String(), "duration=") {
				t.Fatalf("missing phase events: %v %s", err, logs.String())
			}
			b, err := os.ReadFile(filepath.Join(f.c.BackupDir, ".last-failure.log"))
			if err != nil {
				t.Fatal(err)
			}
			if failure != "prefill-target" && !bytes.Contains(b, []byte("secret")) {
				t.Fatal("private failure output missing")
			}
			entries, _ := os.ReadDir(f.c.BackupDir)
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".command-") || strings.HasPrefix(e.Name(), "inprogress-") || strings.HasPrefix(e.Name(), ".inprogress-") || strings.HasPrefix(e.Name(), ".failure-") {
					t.Fatalf("leftover: %s", e.Name())
				}
			}
		})
	}
}
func TestLimitedOutputConcurrent(t *testing.T) {
	var b bytes.Buffer
	l := &limitedWriter{w: &b, left: 100}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if n, err := l.Write(bytes.Repeat([]byte("x"), 100)); n != 100 || err != nil {
				t.Errorf("write: %d %v", n, err)
			}
		}()
	}
	wg.Wait()
	if b.Len() != 100 {
		t.Fatalf("unbounded output: %d", b.Len())
	}
}
func TestHardlinkFailsClosed(t *testing.T) {
	f := setup(t)
	name, err := invoke(t, f, "backup")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(f.c.BackupDir, strings.TrimSpace(name))
	if err = os.Link(filepath.Join(dir, "xbkeeper.json"), filepath.Join(f.c.BackupDir, "shared")); err != nil {
		t.Fatal(err)
	}
	if _, err = invokeStatusJSON(t, f); err == nil {
		t.Fatal("hardlinked entry accepted")
	}
	if _, err = invoke(t, f, "backup"); err == nil {
		t.Fatal("hardlinked entry pruned")
	}
}
