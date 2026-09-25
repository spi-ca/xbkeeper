//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStrictTOMLCaseAndNoLeakedInput(t *testing.T) {
	f := setup(t)
	original, err := os.ReadFile(f.file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := invoke(t, f, "status"); err != nil {
		t.Fatalf("baseline status: %v", err)
	}
	if !strings.Contains(string(original), "keep = 1\n") {
		t.Fatal("fixture missing keep field")
	}
	for _, data := range []string{
		string(original) + "Keep = 'secret-value'\n",
		string(original) + "password = 'secret-value'\n",
		strings.Replace(string(original), "keep = 1\n", "keep = 'secret-value'\n", 1),
	} {
		if err := os.WriteFile(f.file, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := invoke(t, f, "status")
		if err == nil || strings.Contains(err.Error(), "secret-value") || !strings.Contains(err.Error(), "config") {
			t.Fatalf("%s: %v", data, err)
		}
	}
}

func TestStrictJSONMetadataUnchanged(t *testing.T) {
	for _, data := range []string{`{"format":2,"format":1}`, `{"Format":2}`, `{"unknown":"secret-value"}`, `{"format":"secret-value"}`} {
		var m metadata
		if err := strictJSON([]byte(data), &m); err == nil {
			t.Fatalf("metadata JSON accepted: %s", data)
		}
	}
}

func TestSafeDiagnosticReasons(t *testing.T) {
	f := setup(t)
	f.c.MinFreeBytes = 1 << 62
	f.save(t)
	_, err := invoke(t, f, "backup")
	if err == nil || !strings.Contains(err.Error(), "insufficient free space") {
		t.Fatal(err)
	}
	f.c.MinFreeBytes = 0
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
	_, err = invoke(t, f, "backup")
	if err == nil || !strings.Contains(err.Error(), "lock unavailable") {
		t.Fatal(err)
	}
}

func TestDurabilityBoundary(t *testing.T) {
	for _, point := range []string{"stage", "parent", "success"} {
		t.Run(point, func(t *testing.T) {
			f := setup(t)
			first, err := invoke(t, f, "backup")
			if err != nil {
				t.Fatal(err)
			}
			prevStage, prevParent := syncStaged, syncPromotedParent
			defer func() { syncStaged, syncPromotedParent = prevStage, prevParent }()
			var order []string
			syncStaged = func(r *os.Root, name string) error {
				order = append(order, "stage")
				if point == "stage" {
					return errors.New("secret sync failure")
				}
				return syncTree(r, name)
			}
			syncPromotedParent = func(r *os.Root) error {
				order = append(order, "parent")
				if point == "parent" {
					return errors.New("secret sync failure")
				}
				return syncRoot(r)
			}
			result, err := invoke(t, f, "backup")
			if point != "success" && (err == nil || !strings.Contains(err.Error(), "sync failed") || strings.Contains(err.Error(), "secret") || result != "") {
				t.Fatalf("%s result=%q err=%v", point, result, err)
			}
			if point == "success" && (err != nil || strings.Join(order, ",") != "stage,parent") {
				t.Fatalf("order=%v err=%v", order, err)
			}
			if _, err := os.Stat(filepath.Join(f.c.BackupDir, strings.TrimSpace(first))); point != "success" && err != nil {
				t.Fatalf("old backup pruned: %v", err)
			}
			s, err := invoke(t, f, "status")
			if err != nil {
				t.Fatal(err)
			}
			if point == "stage" {
				if !strings.Contains(s, ".inprogress-") {
					t.Fatal("stage not preserved", s)
				}
				if _, err := invoke(t, f, "backup"); err == nil || !strings.Contains(err.Error(), "incomplete staging") {
					t.Fatal("next backup not blocked", err)
				}
			} else if point == "parent" && !strings.Contains(s, "backup-") {
				t.Fatal("promotion lost", s)
			}
		})
	}
}

func TestInspectionFailureQuarantinesAndReleasesLock(t *testing.T) {
	f := setup(t)
	mark(t, f, "sleep")
	prev := inspectGroup
	// Force an unverifiable group after the prepare child starts. Cancellation
	// still drives TERM and KILL despite the persistent inspection error.
	var fail atomic.Bool
	inspectGroup = func(pgid int) (bool, error) {
		if fail.Load() {
			return false, errors.New("private proc error")
		}
		return groupActive(pgid)
	}
	defer func() { inspectGroup = prev }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { var b bytes.Buffer; done <- run(ctx, []string{"backup", "--config", f.file}, &b) }()
	deadline := time.After(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(f.c.BackupDir, "ready")); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("child not ready")
		case <-time.After(10 * time.Millisecond):
		}
	}
	fail.Store(true)
	cancel()
	select {
	case err := <-done:
		var uncertain *containmentUncertain
		if !errors.As(err, &uncertain) || strings.Contains(err.Error(), "private") {
			t.Fatalf("uncertainty: %v", err)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("inspection failure held lock indefinitely")
	}
	r, err := os.OpenRoot(f.c.BackupDir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	lk, err := lock(r)
	if err != nil {
		t.Fatal("lock not released", err)
	}
	lk.Close()
	s, err := invoke(t, f, "status")
	if err != nil || !strings.Contains(s, ".inprogress-") {
		t.Fatalf("stage not quarantined: %v %s", err, s)
	}
	_, err = invoke(t, f, "backup")
	if err == nil || !strings.Contains(err.Error(), "incomplete staging") {
		t.Fatal("followup not blocked", err)
	}
	// A failed inspection cannot justify asserting descendant containment.
}
