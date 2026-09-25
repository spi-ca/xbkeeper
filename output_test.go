//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStatusFormatsAndStaging(t *testing.T) {
	f := setup(t)
	empty, err := invoke(t, f, "status")
	if err != nil || empty != "Command: status\nResult: ok\nBackups (0):\nIncomplete (0):\nPending deletions (0):\nLast success: none\n" {
		t.Fatalf("empty: %q %v", empty, err)
	}
	for _, name := range []string{"inprogress-20200101T000000Z-0000000000000001", ".inprogress-20200101T000000Z-0000000000000002"} {
		if err := os.Mkdir(filepath.Join(f.c.BackupDir, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	human, err := invoke(t, f, "status")
	if err != nil || !strings.Contains(human, "Incomplete (2):") || !strings.Contains(human, "  inprogress-") || !strings.Contains(human, "  .inprogress-") {
		t.Fatalf("human: %q %v", human, err)
	}
	raw, err := invokeStatusJSON(t, f)
	var s status
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal([]byte(raw), &s); err != nil || len(s.Incomplete) != 2 || len(s.Backups) != 0 || strings.Contains(raw, "last_success") {
		t.Fatalf("JSON: %q %+v %v", raw, s, err)
	}
	if _, err = invoke(t, f, "backup"); err == nil || !strings.Contains(err.Error(), "incomplete staging") {
		t.Fatal(err)
	}
	for _, name := range s.Incomplete {
		if _, err := os.Stat(filepath.Join(f.c.BackupDir, name)); err != nil {
			t.Fatal("stage deleted", err)
		}
	}

}

func TestVerboseSuccessKeepsMachineStdout(t *testing.T) {
	f := setup(t)
	script, err := os.ReadFile(f.c.Xtrabackup)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(script), "  echo 'backup_type = full-prepared'", "  printf 'prepare-partial' >&2\n  echo 'backup_type = full-prepared'", 1)
	if text == string(script) {
		t.Fatal("fixture mismatch")
	}
	if err := os.WriteFile(f.c.Xtrabackup, []byte(text), 0700); err != nil {
		t.Fatal(err)
	}
	var out, logs bytes.Buffer
	if err := runWithLog(context.Background(), []string{"backup", "--verbose", "--config", f.file}, &out, &logs); err != nil {
		t.Fatal(err)
	}
	name := strings.TrimSpace(strings.SplitAfter(out.String(), "Backup: ")[1])
	if !strings.HasPrefix(name, "backup-") || strings.Contains(out.String(), "prepare-partial") || !strings.Contains(logs.String(), "phase=prepare stream=stderr line=prepare-partial") || strings.Contains(logs.String(), f.c.DefaultsFile) {
		t.Fatalf("stdout=%q stderr=%q", out.String(), logs.String())
	}
	var verifyOut bytes.Buffer
	if err := runWithLog(context.Background(), []string{"verify", "--json", "--config", f.file}, &verifyOut, &bytes.Buffer{}); err != nil || !json.Valid(verifyOut.Bytes()) {
		t.Fatalf("verify: %v %q", err, verifyOut.String())
	}
}

func TestVerboseCLIStreamAndTail(t *testing.T) {
	f := setup(t)
	script, err := os.ReadFile(f.c.Xtrabackup)
	if err != nil {
		t.Fatal(err)
	}
	replacement := `printf 'stdout first\npartial'
  printf 'stderr first\n' >&2
  head -c 1100000 /dev/zero | tr '\000' 'F' >&2
  printf '\n' >&2
  i=0; while [ "$i" -lt 300 ]; do printf '%04096d\n' 0 >&2; i=$((i+1)); done
  printf 'END-TAIL\n' >&2
  exit 44`
	text := strings.Replace(string(script), "echo 'private fake error password=secret' >&2; exit 44", replacement, 1)
	if text == string(script) {
		t.Fatal("fixture mismatch")
	}
	if err := os.WriteFile(f.c.Xtrabackup, []byte(text), 0700); err != nil {
		t.Fatal(err)
	}
	mark(t, f, "fail-backup")
	var out, logs bytes.Buffer
	err = runWithLog(context.Background(), []string{"backup", "--verbose", "--config", f.file}, &out, &logs)
	if err == nil || !strings.Contains(err.Error(), "backup: xtrabackup execution failed (exit code 44)") || !strings.Contains(out.String(), "Result: failed: backup: xtrabackup execution failed (exit code 44)") {
		t.Fatalf("result: %q %v", out.String(), err)
	}
	str := logs.String()
	for _, piece := range []string{"phase=backup stream=stdout line=\"stdout first\"", "phase=backup stream=stderr line=\"stderr first\"", "truncated=true", "phase end"} {
		if !strings.Contains(str, piece) {
			t.Errorf("missing %q (prefix %q)", piece, str[:min(len(str), 1000)])
		}
	}
	if strings.Contains(str, "END-TAIL") || len(str) > maxVerboseOutput+30000 {
		t.Fatalf("unbounded streaming: %d bytes", len(str))
	}
	b, err := os.ReadFile(filepath.Join(f.c.BackupDir, ".last-failure.log"))
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != maxLog || !bytes.HasSuffix(b, []byte("END-TAIL\n")) || bytes.Contains(b, []byte("stdout first")) {
		t.Fatalf("tail: %d %q", len(b), b[len(b)-40:])
	}
	// Without --verbose, phase events remain but child content is not emitted.
	logs.Reset()
	err = runWithLog(context.Background(), []string{"backup", "--config", f.file}, &out, &logs)
	if err == nil || strings.Contains(logs.String(), "child output") || strings.Contains(logs.String(), "END-TAIL") {
		t.Fatalf("default logs: %v %s", err, logs.String())
	}
}

func TestLineOutputBoundsStreamsAndPartial(t *testing.T) {
	var logs bytes.Buffer
	b := &verboseBudget{remaining: 4*(maxVerboseLine+4) + 3*256, logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	stdout, stderr, flush := childOutputs(&bytes.Buffer{}, b, "prepare")
	for _, p := range [][]byte{[]byte("a\nb"), bytes.Repeat([]byte("L"), maxVerboseLine+100), []byte("\ntrailing")} {
		if _, err := stdout.Write(p); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := stderr.Write([]byte("secret\n")); err != nil {
		t.Fatal(err)
	}
	flush()
	s := logs.String()
	if !strings.Contains(s, "stream=stdout line=a") || !strings.Contains(s, "stream=stdout line=b") || !strings.Contains(s, "truncated=true") || strings.Contains(s, "secret") || strings.Contains(s, "trailing") {
		t.Fatalf("lines: %s", s)
	}
}

func TestUnterminatedLineFlushed(t *testing.T) {
	var logs bytes.Buffer
	b := &verboseBudget{remaining: maxVerboseOutput, logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	stdout, _, flush := childOutputs(&bytes.Buffer{}, b, "backup")
	_, _ = stdout.Write([]byte("unfinished"))
	if logs.Len() != 0 {
		t.Fatal("partial emitted before completion")
	}
	flush()
	if !strings.Contains(logs.String(), "phase=backup stream=stdout line=unfinished") {
		t.Fatal(logs.String())
	}
}

func TestCommandTailWrap(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "tail")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tail := &commandTail{file: f}
	for _, piece := range [][]byte{bytes.Repeat([]byte("A"), maxLog-2), []byte("123456"), bytes.Repeat([]byte("B"), maxLog+100)} {
		if _, err := tail.Write(piece); err != nil {
			t.Fatal(err)
		}
	}
	b, err := tail.Bytes()
	if err != nil || len(b) != maxLog || b[0] != 'B' || b[len(b)-1] != 'B' {
		t.Fatalf("tail len=%d err=%v", len(b), err)
	}
	// Wrap with a shorter write after a full-buffer write.
	if _, err := tail.Write([]byte("xyz")); err != nil {
		t.Fatal(err)
	}
	b, err = tail.Bytes()
	if err != nil || !bytes.HasSuffix(b, []byte("xyz")) || b[0] != 'B' {
		t.Fatalf("wrapped tail: %v", err)
	}
}

func TestVerboseCanceledChild(t *testing.T) {
	f := setup(t)
	mark(t, f, "sleep")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		var out, logs bytes.Buffer
		done <- runWithLog(ctx, []string{"backup", "--verbose", "--config", f.file}, &out, &logs)
	}()
	// Wait for prepare to start before canceling, as in TestCancellation.
	for i := 0; i < 500; i++ {
		if _, err := os.Stat(filepath.Join(f.c.BackupDir, "ready")); err == nil {
			break
		}
		if i == 499 {
			t.Fatal("prepare did not start")
		}
		select {
		case <-ctx.Done():
			t.Fatal("early cancellation")
		default:
		}
		// Use a short bounded wait; no changes to host services.
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("cancellation did not terminate child")
	}
}
