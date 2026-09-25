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
	"testing"
)

func resultJSON(t *testing.T, args []string) (commandEnvelope, error) {
	t.Helper()
	var out, logs bytes.Buffer
	err := runWithLog(context.Background(), args, &out, &logs)
	dec := json.NewDecoder(&out)
	var result commandEnvelope
	if e := dec.Decode(&result); e != nil {
		t.Fatalf("json result: %v %q", e, out.String())
	}
	var extra any
	if e := dec.Decode(&extra); e != io.EOF {
		t.Fatalf("extra stdout JSON: %v %q", e, out.String())
	}
	if result.OK != (err == nil) || (result.Error == nil) != result.OK || strings.Contains(out.String(), "password=secret") {
		t.Fatalf("result=%+v err=%v logs=%q", result, err, logs.String())
	}
	if err != nil && *result.Error != err.Error() {
		t.Fatalf("error mismatch: %+v %v", result, err)
	}
	return result, err
}

func TestUnifiedResults(t *testing.T) {
	f := setup(t)
	empty, err := resultJSON(t, []string{"status", "--json", "--config", f.file})
	if err != nil || empty.Data == nil {
		t.Fatal(empty, err)
	}
	state := empty.Data.(map[string]any)
	if len(state["backups"].([]any)) != 0 || len(state["incomplete"].([]any)) != 0 {
		t.Fatal(state)
	}
	if _, ok := state["last_success"]; ok {
		t.Fatal(state)
	}
	for _, cmd := range []string{"verify", "status", "backup"} {
		result, err := resultJSON(t, []string{cmd, "--json", "--config", f.file})
		if cmd == "verify" {
			if err == nil || result.Data != nil || !strings.Contains(*result.Error, "shared lock") {
				t.Fatal(result, err)
			}
		} else if err != nil || result.Data == nil {
			t.Fatal(result, err)
		}
	}
	nameResult, err := resultJSON(t, []string{"backup", "--json", "--config", f.file})
	if err != nil {
		t.Fatal(err)
	}
	name := nameResult.Data.(map[string]any)["name"].(string)
	if !strings.HasPrefix(name, "backup-") {
		t.Fatal(name)
	}
	for _, cmd := range []string{"status", "verify"} {
		result, err := resultJSON(t, []string{cmd, "--json", "--config", f.file})
		if err != nil || result.Data == nil {
			t.Fatal(result, err)
		}
		data := result.Data.(map[string]any)
		if len(data["backups"].([]any)) != 1 {
			t.Fatal(data)
		}
		if cmd == "status" && (data["last_success"] != name || len(data["incomplete"].([]any)) != 0) {
			t.Fatal(data)
		}
		if cmd == "verify" && (data["backups"].([]any)[0].(map[string]any)["files"] != float64(3)) {
			t.Fatal(data)
		}
	}
	var human bytes.Buffer
	if err := runWithLog(context.Background(), []string{"verify", "--config", f.file}, &human, io.Discard); err != nil || !strings.Contains(human.String(), "Command: verify\nResult: ok\nBackups (1):") || !strings.Contains(human.String(), name+": ok (files: 3)") {
		t.Fatal(err, human.String())
	}
	if err := os.WriteFile(filepath.Join(f.c.BackupDir, name, "data file-世界"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	bad, err := resultJSON(t, []string{"verify", "--json", "--config", f.file})
	if err == nil || bad.Data.(map[string]any)["backups"].([]any)[0].(map[string]any)["ok"] != false {
		t.Fatal(bad, err)
	}
	human.Reset()
	if err := runWithLog(context.Background(), []string{"verify", "--config", f.file}, &human, io.Discard); err == nil || !strings.Contains(human.String(), "Result: failed: verify:") || !strings.Contains(human.String(), name+": failed (files:") {
		t.Fatal(err, human.String())
	}
}

func TestJSONLockValidationAndChildFailures(t *testing.T) {
	f := setup(t)
	mark(t, f, "fail-backup")
	child, err := resultJSON(t, []string{"backup", "--json", "--config", f.file})
	if err == nil || child.Data != nil || !strings.Contains(*child.Error, "exit code 44") {
		t.Fatal(child, err)
	}
	if err := os.Remove(filepath.Join(f.c.BackupDir, "fail-backup")); err != nil {
		t.Fatal(err)
	}
	empty, err := resultJSON(t, []string{"verify", "--json", "--config", f.file})
	if err == nil || empty.Data != nil || !strings.Contains(*empty.Error, "no completed backups") {
		t.Fatal(empty, err)
	}
	name := completed(t, f)
	root, err := os.OpenRoot(f.c.BackupDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	lk, err := lock(root)
	if err != nil {
		t.Fatal(err)
	}
	locked, err := resultJSON(t, []string{"verify", "--json", "--config", f.file})
	lk.Close()
	if err == nil || locked.Data != nil || !strings.Contains(*locked.Error, "shared lock unavailable") {
		t.Fatal(locked, err)
	}
	if err := os.WriteFile(filepath.Join(f.c.BackupDir, name, "xbkeeper.json"), []byte(`{"format":2,"secret":"password=secret"}`), 0600); err != nil {
		t.Fatal(err)
	}
	invalid, err := resultJSON(t, []string{"status", "--json", "--config", f.file})
	if err == nil || invalid.Data != nil || !strings.Contains(*invalid.Error, "invalid managed backup") {
		t.Fatal(invalid, err)
	}
}

func TestResultFailuresAndParseAmbiguity(t *testing.T) {
	f := setup(t)
	for _, cmd := range []string{"backup", "verify", "status"} {
		for _, jsonMode := range []bool{false, true} {
			args := []string{cmd, "--config", filepath.Join(f.base, "missing.toml")}
			if jsonMode {
				args = append(args, "--json")
				r, e := resultJSON(t, args)
				if e == nil || r.Data != nil || r.Command != cmd {
					t.Fatal(r, e)
				}
			} else {
				var out bytes.Buffer
				if e := runWithLog(context.Background(), args, &out, io.Discard); e == nil || !strings.Contains(out.String(), "Command: "+cmd+"\nResult: failed: config: config file missing\n") {
					t.Fatal(e, out.String())
				}
			}
		}
	}
	if e := os.Remove(f.c.BackupDir); e != nil {
		t.Fatal(e)
	}
	for _, cmd := range []string{"verify", "status"} {
		r, e := resultJSON(t, []string{cmd, "--config", f.file, "--json"})
		if e == nil || r.Data != nil || !strings.Contains(*r.Error, "backup directory missing") {
			t.Fatal(r, e)
		}
	}
	var out bytes.Buffer
	if e := runWithLog(context.Background(), []string{"status", "--unknown", "--json"}, &out, io.Discard); e == nil || out.Len() != 0 {
		t.Fatal(e, out.String())
	}
}

type brokenOutput struct{}

func (brokenOutput) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestResultWriterFailure(t *testing.T) {
	f := setup(t)
	for _, args := range [][]string{{"status", "--config", f.file}, {"status", "--json", "--config", f.file}} {
		if err := runWithLog(context.Background(), args, brokenOutput{}, io.Discard); !errors.Is(err, io.ErrClosedPipe) {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runWithLog(ctx, []string{"verify", "--json", "--config", f.file}, brokenOutput{}, io.Discard); !errors.Is(err, io.ErrClosedPipe) || !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation or writer failure: %v", err)
	}
}

func TestVerboseLevelsAndFlagScope(t *testing.T) {
	f := setup(t)
	for _, asJSON := range []bool{false, true} {
		for _, verbose := range []bool{false, true} {
			args := []string{"backup", "--config", f.file}
			if asJSON {
				args = append(args, "--json")
			}
			if verbose {
				args = append(args, "--verbose")
			}
			var out, logs bytes.Buffer
			if err := runWithLog(context.Background(), args, &out, &logs); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(logs.String(), "level=INFO") || strings.Contains(out.String(), "child output") || strings.Contains(out.String(), "level=") || (strings.Contains(logs.String(), "level=DEBUG") != verbose) {
				t.Fatalf("out=%q logs=%q", out.String(), logs.String())
			}
		}
	}
	for _, cmd := range []string{"status", "verify"} {
		for _, args := range [][]string{{cmd, "--verbose", "--config", f.file}, {"--verbose", cmd, "--config", f.file}} {
			var out bytes.Buffer
			if err := runWithLog(context.Background(), args, &out, io.Discard); err != nil {
				t.Fatal(args, err)
			}
		}
	}
	var out, logs bytes.Buffer
	if err := runWithLog(context.Background(), []string{"--verbose", "backup", "--config", f.file}, &out, &logs); err != nil || !strings.Contains(logs.String(), "level=DEBUG") || !strings.Contains(logs.String(), "level=INFO") {
		t.Fatal(err, logs.String())
	}
}

type cancelAfterVerify struct {
	cancel context.CancelFunc
	starts int
}

func (w *cancelAfterVerify) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte(`msg="phase start"`)) && bytes.Contains(p, []byte("phase=verify")) {
		w.starts++
		if w.starts == 2 {
			w.cancel()
		}
	}
	return len(p), nil
}
func TestVerifyPartialCancellationResult(t *testing.T) {
	f := setup(t)
	f.c.Keep = 2
	f.save(t)
	completed(t, f)
	completed(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out bytes.Buffer
	logs := &cancelAfterVerify{cancel: cancel}
	err := runWithLog(ctx, []string{"verify", "--json", "--config", f.file}, &out, logs)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	var result struct {
		Command string       `json:"command"`
		OK      bool         `json:"ok"`
		Data    verifyReport `json:"data"`
		Error   string       `json:"error"`
	}
	if e := json.Unmarshal(out.Bytes(), &result); e != nil || result.Command != "verify" || result.OK || len(result.Data.Backups) != 1 || !result.Data.Backups[0].OK || result.Error != "verify: context canceled" {
		t.Fatalf("partial=%q err=%v decode=%v", out.String(), err, e)
	}
}
