//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// now is overridden only by sequential clock-skew tests; wall timestamps remain honest.
var now = time.Now
var removeRetained = removeTree
var generateStaged = generateManifest
var syncStaged = syncTree
var syncPromotedParent = syncRoot

func phaseError(phase string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", phase, err)
	}
	var uncertain *containmentUncertain
	if errors.As(err, &uncertain) {
		return fmt.Errorf("%s: %w", phase, uncertain)
	}
	reason := "operation failed"
	switch {
	case phase == "config":
		switch {
		case strings.Contains(err.Error(), "config file missing"):
			reason = "config file missing"
		case strings.Contains(err.Error(), "unsafe config file"), strings.Contains(err.Error(), "config file unreadable"):
			reason = "unsafe or unreadable config file permissions or path"
		case strings.Contains(err.Error(), "backup directory"):
			reason = "unsafe backup directory path, ownership or permissions"
		default:
			reason = "invalid config TOML, keys or schema"
		}
	case strings.Contains(err.Error(), "backup directory initialization sync failed"):
		reason = "backup directory initialization sync failed; backup not started"
	case phase == "lock", errors.Is(err, syscall.EWOULDBLOCK), strings.Contains(err.Error(), "lock unavailable"):
		reason = "backup lock unavailable"
	case strings.Contains(err.Error(), "insufficient free space"):
		reason = "insufficient free space"
	case strings.Contains(err.Error(), "incomplete staging"):
		reason = "incomplete staging requires manual recovery"
	case strings.Contains(err.Error(), "invalid managed backup"), strings.Contains(err.Error(), "invalid staging"):
		reason = "invalid managed backup or staging"
	case strings.Contains(err.Error(), "backup_type"), strings.Contains(err.Error(), "checkpoints"), strings.Contains(err.Error(), "backup not full-prepared"):
		reason = "checkpoint not full-prepared"
	case strings.Contains(err.Error(), "datadir unavailable"):
		reason = "datadir missing or unsafe"
	case strings.Contains(err.Error(), "defaults file unavailable"):
		reason = "defaults file missing or unsafe"
	case strings.Contains(err.Error(), "socket unavailable"):
		reason = "socket missing or invalid"
	case strings.Contains(err.Error(), "xtrabackup binary unavailable"):
		reason = "xtrabackup binary missing or untrusted"
	case phase == "retention":
		reason = "retention or removal failed"
	case phase == "hash":
		reason = "checksum generation or manifest validation failed; staging preserved"
	case phase == "durability":
		reason = "file or directory sync failed; retention skipped"
	case phase == "validation":
		reason = "invalid backup or unsafe path"
	case phase == "version", phase == "backup", phase == "prepare":
		reason = "xtrabackup execution failed"
	}
	var exit *childExit
	if errors.As(err, &exit) {
		return fmt.Errorf("%s: %s (exit code %d)", phase, reason, exit.code)
	}
	return fmt.Errorf("%s: %s", phase, reason)
}
func backup(ctx context.Context, c config, events *slog.Logger, verbose bool) (backupReport, error) {
	// Single-purpose CLI: process-global umask also applies to xtrabackup.
	syscall.Umask(0077)
	root, err := openBackupRoot(ctx, c.BackupDir)
	if err != nil {
		return backupReport{}, phaseError("validation", err)
	}
	defer root.Close()
	lk, err := lock(root)
	if err != nil {
		return backupReport{}, phaseError("lock", err)
	}
	defer lk.Close()
	s, err := inspect(c.BackupDir)
	if err != nil {
		return backupReport{}, phaseError("validation", err)
	}
	if len(s.Incomplete) != 0 {
		return backupReport{}, phaseError("validation", errors.New("incomplete staging"))
	}
	if err = validateRuntime(c); err != nil {
		return backupReport{}, phaseError("validation", err)
	}
	if err = freeSpace(c); err != nil {
		return backupReport{}, phaseError("validation", err)
	}
	id, err := token()
	if err != nil {
		return backupReport{}, phaseError("validation", err)
	}
	stamp := now().UTC().Format("20060102T150405Z")
	stage := "inprogress-" + stamp + "-" + id
	completed := "backup-" + stamp + "-" + id
	if err = root.Mkdir(stage, 0700); err != nil {
		return backupReport{}, phaseError("validation", err)
	}
	finished := false
	preserveStage := false
	defer func() {
		if !finished && !preserveStage && safeTree(filepath.Join(c.BackupDir, stage)) == nil {
			_ = removeTree(root, stage)
		}
	}()
	dir := filepath.Join(c.BackupDir, stage)
	// Command output is never placed in the directory controlled by xtrabackup.
	logName := ".command-" + id
	f, err := root.OpenFile(logName, os.O_CREATE|os.O_EXCL|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return backupReport{}, phaseError("validation", err)
	}
	defer func() {
		fi, e1 := f.Stat()
		entry, e2 := root.Lstat(logName)
		_ = f.Close()
		if e1 == nil && e2 == nil && os.SameFile(fi, entry) && entry.Mode().IsRegular() {
			_ = root.Remove(logName)
		}
	}()
	log := &commandTail{file: f}
	var budget *verboseBudget
	if verbose {
		budget = &verboseBudget{remaining: maxVerboseOutput, logger: events}
	}
	started := now().UTC()
	execPhase := func(label string, args []string, output io.Writer) error {
		begin := time.Now()
		events.Info("phase start", "backup_id", completed, "phase", label)
		stdout, stderr, flush := childOutputs(output, budget, label)
		err := runChildStreams(ctx, c.Xtrabackup, args, stdout, stderr)
		// An uncertain inherited pipe may still be copied by os/exec; do not
		// touch its line buffers while that copier could still be running.
		var uncertain *containmentUncertain
		if !errors.As(err, &uncertain) {
			flush()
		}
		events.Info("phase end", "backup_id", completed, "phase", label, "duration", time.Since(begin).String(), "ok", err == nil)
		return phaseError(label, err)
	}
	var version bytes.Buffer
	vLog := &limitedWriter{w: &version, left: 4096}
	// Version output has its own bound, and does not contain option-file data.
	err = execPhase("version", []string{"--no-defaults", "--version"}, io.MultiWriter(vLog, log))
	v := strings.TrimSpace(version.String())
	if err == nil && (v == "" || strings.ContainsAny(v, "\x00\r\n")) {
		err = phaseError("version", errors.New("invalid version"))
	}
	if len(v) > 1024 {
		v = v[:1024]
	}
	if err == nil {
		err = execPhase("backup", []string{"--defaults-file=" + c.DefaultsFile, "--backup", "--target-dir=" + dir, "--datadir=" + c.Datadir, "--socket=" + c.Socket}, log)
	}
	if err == nil {
		err = execPhase("prepare", []string{"--no-defaults", "--prepare", "--target-dir=" + dir}, log)
	}
	if err != nil {
		var uncertain *containmentUncertain
		preserveStage = errors.As(err, &uncertain)
		if saveErr := saveFailure(root, log); saveErr != nil {
			return backupReport{}, fmt.Errorf("%w; failure log unavailable", err)
		}
		return backupReport{}, err
	}
	if err = safeTree(dir); err != nil {
		return backupReport{}, phaseError("validation", err)
	}
	if err = checkpoint(dir); err != nil {
		return backupReport{}, phaseError("validation", err)
	}
	m := metadata{Format: formatVersion, CreatedAt: started.Format(time.RFC3339Nano), PreparedAt: now().UTC().Format(time.RFC3339Nano), XtrabackupVersion: v}
	b, _ := json.MarshalIndent(m, "", "  ")
	if err = writeExclusive(root, stage+"/xbkeeper.json", append(b, '\n')); err != nil {
		return backupReport{}, phaseError("validation", err)
	}
	// Once prepared metadata is present, never discard the stage on a hashing,
	// validation or cancellation failure; no retention runs before promotion.
	preserveStage = true
	beginHash := time.Now()
	events.Info("phase start", "backup_id", completed, "phase", "hash")
	stageRoot, openErr := root.OpenRoot(stage)
	if openErr != nil {
		return backupReport{}, phaseError("hash", openErr)
	}
	manifest, hashErr := generateStaged(ctx, stageRoot)
	_ = stageRoot.Close()
	if hashErr == nil {
		hashErr = writeExclusive(root, stage+"/"+manifestName, manifest)
	}
	events.Info("phase end", "backup_id", completed, "phase", "hash", "duration", time.Since(beginHash).String(), "ok", hashErr == nil)
	if hashErr != nil {
		return backupReport{}, phaseError("hash", hashErr)
	}
	if err = validBackup(dir); err != nil {
		return backupReport{}, phaseError("validation", err)
	}
	if err = ctx.Err(); err != nil {
		return backupReport{}, phaseError("hash", err)
	}
	if _, err = root.Lstat(completed); err == nil {
		return backupReport{}, phaseError("validation", errors.New("destination exists"))
	} else if !errors.Is(err, os.ErrNotExist) {
		return backupReport{}, phaseError("validation", err)
	}
	if err = syncStaged(root, stage); err != nil {
		preserveStage = true
		return backupReport{}, phaseError("durability", err)
	}
	if err = ctx.Err(); err != nil {
		return backupReport{}, phaseError("durability", err)
	}
	if err = renameInRoot(root, stage, completed); err != nil {
		preserveStage = true
		return backupReport{}, phaseError("validation", err)
	}
	finished = true
	if err = syncPromotedParent(root); err != nil {
		return backupReport{}, phaseError("durability", err)
	}
	if err = ctx.Err(); err != nil {
		return backupReport{}, phaseError("retention", err)
	}
	begin := time.Now()
	events.Info("phase start", "backup_id", completed, "phase", "retention")
	defer func() {
		events.Info("phase end", "backup_id", completed, "phase", "retention", "duration", time.Since(begin).String(), "ok", err == nil)
	}()
	s, scanErr := inspect(c.BackupDir)
	if scanErr != nil {
		err = scanErr
		return backupReport{}, phaseError("retention", err)
	}
	// The just-completed backup is protected even when the wall clock rolls back.
	kept := 0
	for _, old := range s.Backups {
		if err = ctx.Err(); err != nil {
			return backupReport{}, phaseError("retention", err)
		}
		if old == completed {
			continue
		}
		if kept < c.Keep-1 {
			kept++
			continue
		}
		if err = validBackup(filepath.Join(c.BackupDir, old)); err != nil {
			return backupReport{}, phaseError("retention", err)
		}
		if err = ctx.Err(); err != nil {
			return backupReport{}, phaseError("retention", err)
		}
		if err = removeRetained(root, old); err != nil {
			return backupReport{}, phaseError("retention", err)
		}
	}
	return backupReport{Name: completed}, nil
}
func saveFailure(root *os.Root, log *commandTail) error {
	b, err := log.Bytes()
	if err != nil {
		return err
	}
	// Never overwrite a symlink or special file, including one planted by an operator.
	fi, err := root.Lstat(".last-failure.log")
	if err == nil {
		uid, ok := owner(fi)
		if !ok || int(uid) != os.Geteuid() || !fi.Mode().IsRegular() || fi.Mode().Perm()&0077 != 0 || fi.Sys().(*syscall.Stat_t).Nlink != 1 {
			return errors.New("unsafe previous failure log")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	id, err := token()
	if err != nil {
		return err
	}
	tmp := ".failure-" + id
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(b)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		_ = root.Remove(tmp)
		return errors.New("cannot write failure log")
	}
	if err = renameInRoot(root, tmp, ".last-failure.log"); err != nil {
		_ = root.Remove(tmp)
		return err
	}
	return nil
}
