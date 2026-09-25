//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"
)

type verifyResult struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Files  int    `json:"files"` // successfully hashed files; zero if none checked
	Reason string `json:"reason,omitempty"`
}
type verifyReport struct {
	Backups []verifyResult `json:"backups"`
}

const (
	reasonLegacy   = "legacy format 1 is unverifiable"
	reasonInvalid  = "invalid or unsafe backup"
	reasonMismatch = "checksum or file set mismatch"
)

func verify(ctx context.Context, dir, selected string, out io.Writer, events *slog.Logger) error {
	if selected != "" && (!managedName.MatchString(selected) || !strings.HasPrefix(selected, "backup-")) {
		return errors.New("verify: invalid completed backup basename")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return errors.New("verify: backup root unavailable")
	}
	defer root.Close()
	lk, err := sharedLock(root)
	if err != nil {
		return errors.New("verify: shared lock unavailable or unsafe")
	}
	defer lk.Close()
	var names []string
	if selected != "" {
		names = []string{selected}
	} else {
		entries, e := os.ReadDir(dir)
		if e != nil {
			return errors.New("verify: cannot list backups")
		}
		for _, entry := range entries {
			n := entry.Name()
			if managedName.MatchString(n) && strings.HasPrefix(n, "backup-") {
				names = append(names, n)
			}
		}
		sort.Strings(names)
	}
	if len(names) == 0 {
		return errors.New("verify: no completed backups")
	}
	report := verifyReport{Backups: make([]verifyResult, 0, len(names))}
	failed := false
	for _, n := range names {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("verify: %w", err)
		}
		begin := time.Now()
		events.Info("phase start", "phase", "verify", "backup_id", n)
		result, e := verifyOne(ctx, root, n)
		if e != nil {
			return fmt.Errorf("verify: %w", e)
		}
		events.Info("phase end", "phase", "verify", "backup_id", n, "duration", time.Since(begin).String(), "ok", result.OK)
		report.Backups = append(report.Backups, result)
		if !result.OK {
			failed = true
		}
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	if err := json.NewEncoder(out).Encode(report); err != nil {
		return err
	}
	if failed {
		return errors.New("verify: one or more backups failed or are unverifiable")
	}
	return nil
}
func matchingFileSet(files []string, expected map[string]string) bool {
	if len(files) != len(expected)+1 {
		return false
	}
	for _, name := range files {
		if name != manifestName {
			if _, ok := expected[name]; !ok {
				return false
			}
		}
	}
	return true
}
func verifyOne(ctx context.Context, root *os.Root, name string) (verifyResult, error) {
	result := verifyResult{Name: name, Reason: reasonInvalid}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	fi, err := root.Lstat(name)
	if err != nil || !safeEntry(fi, true) {
		return result, nil
	}
	sub, err := root.OpenRoot(name)
	if err != nil {
		return result, nil
	}
	defer sub.Close()
	opened, err := sub.Stat(".")
	if err != nil || !sameSnapshot(fi, opened) {
		return result, nil
	}
	b, err := readControl(ctx, sub, "xbkeeper.json", 64*1024)
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, nil
	}
	var m metadata
	if err := strictJSON(b, &m); err != nil {
		return result, nil
	}
	// Never bless hindsight hashes on format-1 backups.
	if m.Format == 1 {
		result.Reason = reasonLegacy
		return result, nil
	}
	if m.Format != 2 {
		return result, nil
	}
	if err := validBackupRoot(ctx, sub); err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		// Distinguish a safe regular-file set mismatch from malformed controls,
		// manifests, links and unsafe files without exposing arbitrary paths.
		if expected, parseErr := parseManifest(ctx, sub); parseErr == nil {
			if files, walkErr := listFiles(ctx, sub); walkErr == nil {
				if !matchingFileSet(files, expected) {
					result.Reason = reasonMismatch
				}
			}
		}
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, nil
	}
	expected, err := parseManifest(ctx, sub)
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, nil
	}
	files, err := listFiles(ctx, sub)
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, nil
	}
	if !matchingFileSet(files, expected) {
		result.Reason = reasonMismatch
		return result, nil
	}
	for _, n := range files {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if n == manifestName {
			continue
		}
		want := expected[n]
		digest, e := hashFile(ctx, sub, n)
		if e != nil {
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			return result, nil
		}
		result.Files++
		if digest != want {
			result.Reason = reasonMismatch
			return result, nil
		}
	}
	after, e := root.Lstat(name)
	if e != nil || !sameSnapshot(fi, after) {
		result.Reason = reasonInvalid
		return result, nil
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	result.OK = true
	result.Reason = ""
	return result, nil
}
