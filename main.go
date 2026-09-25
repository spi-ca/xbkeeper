//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"regexp"
	"syscall"
)

// Version can be set with -ldflags '-X main.Version=v1.2.3'.
var Version = "dev"

const formatVersion = 2
const maxLog = 1 << 20
const defaultConfigPath = "/etc/xbkeeper/xbkeeper.toml"

var managedName = regexp.MustCompile(`^(backup|inprogress|\.inprogress|\.deleting)-[0-9]{8}T[0-9]{6}Z-[0-9a-f]{16}$`)

type config struct {
	BackupDir    string `toml:"backup_dir"`
	Datadir      string `toml:"datadir"`
	Socket       string `toml:"socket"`
	DefaultsFile string `toml:"defaults_file"`
	Keep         int    `toml:"keep"`
	MinFreeBytes int64  `toml:"min_free_bytes"`
	Xtrabackup   string `toml:"xtrabackup"`
}
type metadata struct {
	Format            int    `json:"format"`
	CreatedAt         string `json:"created_at"`
	PreparedAt        string `json:"prepared_at"`
	XtrabackupVersion string `json:"xtrabackup_version"`
}
type status struct {
	Backups          []string `json:"backups"`
	Incomplete       []string `json:"incomplete"`
	PendingDeletions []string `json:"pending_deletions"`
	LastSuccess      string   `json:"last_success,omitempty"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		var presented *presentedError
		if !errors.As(err, &presented) {
			fmt.Fprintln(os.Stderr, "xbkeeper:", err)
		}
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string, out io.Writer) error {
	return runWithLog(ctx, args, out, os.Stderr)
}

// commandEnvelope is the only JSON value written to stdout for a parsed command.
// Data is nil for pre-result failures, but may contain partial verification results.
type commandEnvelope struct {
	Command string  `json:"command"`
	OK      bool    `json:"ok"`
	Data    any     `json:"data"`
	Error   *string `json:"error"`
}
type backupReport struct {
	Name string `json:"name"`
}

// Mark only errors whose result was successfully written. Parse and writer
// failures still need stderr diagnostics; errors.Is/As retain the original cause.
type presentedError struct{ cause error }

func (e *presentedError) Error() string { return e.cause.Error() }
func (e *presentedError) Unwrap() error { return e.cause }

func runWithLog(ctx context.Context, args []string, out, logOutput io.Writer) error {
	if len(args) == 1 && args[0] == "version" {
		_, err := fmt.Fprintln(out, Version)
		return err
	}
	cmd, cfgPath, selected, flags, err := parseOptions(args)
	if err != nil {
		return err // Format is ambiguous for parse errors; stderr only.
	}
	level := slog.LevelInfo
	if flags.verbose {
		level = slog.LevelDebug
	}
	events := slog.New(slog.NewTextHandler(logOutput, &slog.HandlerOptions{Level: level}))
	var data any
	c, operationErr := loadConfig(cfgPath)
	if operationErr != nil {
		operationErr = phaseError("config", operationErr)
	} else if cmd == "status" || cmd == "verify" {
		var missing bool
		missing, operationErr = backupRootMissing(c.BackupDir)
		if operationErr != nil {
			operationErr = phaseError("config", operationErr)
		} else if missing {
			operationErr = fmt.Errorf("%s: backup directory missing; run backup to initialize it", cmd)
		}
	}
	if operationErr == nil {
		switch cmd {
		case "status":
			var s status
			s, operationErr = inspectContext(ctx, c.BackupDir)
			if operationErr != nil {
				operationErr = phaseError("validation", operationErr)
			} else {
				data = s
			}
		case "verify":
			var report verifyReport
			report, operationErr = verify(ctx, c.BackupDir, selected, events)
			if len(report.Backups) != 0 {
				data = report
			}
		case "backup":
			var report backupReport
			report, operationErr = backup(ctx, c, events, flags.verbose)
			if operationErr == nil {
				data = report
			}
		}
	}
	if err := presentResult(out, cmd, flags.json, data, operationErr); err != nil {
		return errors.Join(operationErr, err) // Preserve cancellation and the writer failure.
	}
	if operationErr != nil {
		return &presentedError{cause: operationErr}
	}
	return nil
}

func presentResult(out io.Writer, cmd string, asJSON bool, data any, operationErr error) error {
	if asJSON {
		envelope := commandEnvelope{Command: cmd, OK: operationErr == nil, Data: data}
		if operationErr != nil {
			message := operationErr.Error()
			envelope.Error = &message
		}
		return json.NewEncoder(out).Encode(envelope)
	}
	result := "ok"
	if operationErr != nil {
		result = "failed: " + operationErr.Error()
	}
	if _, err := fmt.Fprintf(out, "Command: %s\nResult: %s\n", cmd, result); err != nil {
		return err
	}
	if data == nil {
		return nil
	}
	switch value := data.(type) {
	case backupReport:
		_, err := fmt.Fprintln(out, "Backup: "+value.Name)
		return err
	case status:
		return printStatus(out, value)
	case verifyReport:
		if _, err := fmt.Fprintf(out, "Backups (%d):\n", len(value.Backups)); err != nil {
			return err
		}
		for _, b := range value.Backups {
			state := "failed"
			if b.OK {
				state = "ok"
			}
			if _, err := fmt.Fprintf(out, "  %s: %s (files: %d)", b.Name, state, b.Files); err != nil {
				return err
			}
			if b.Reason != "" {
				if _, err := fmt.Fprint(out, " - "+b.Reason); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintln(out); err != nil {
				return err
			}
		}
	}
	return nil
}

type commandFlags struct{ json, verbose bool }

func parseOptions(args []string) (cmd, cfgPath, selected string, flags commandFlags, err error) {
	globalVerbose := len(args) > 0 && args[0] == "--verbose"
	if globalVerbose {
		args = args[1:]
	}
	if len(args) < 1 || (args[0] != "backup" && args[0] != "status" && args[0] != "verify") {
		err = errors.New("usage: xbkeeper backup|status|verify [--config PATH] [--json] [--verbose] [verify: --backup NAME] | version")
		return
	}
	cmd = args[0]
	f := flag.NewFlagSet(cmd, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.StringVar(&cfgPath, "config", defaultConfigPath, "absolute config path")
	if cmd == "verify" {
		f.StringVar(&selected, "backup", "", "completed backup basename")
	}
	f.BoolVar(&flags.json, "json", false, "machine-readable result")
	f.BoolVar(&flags.verbose, "verbose", false, "debug logging (sensitive child output during backup)")
	if err = f.Parse(args[1:]); err != nil || f.NArg() != 0 || cfgPath == "" {
		cmd, cfgPath, selected, flags = "", "", "", commandFlags{}
		err = errors.New("invalid command flags or arguments")
	} else if globalVerbose {
		flags.verbose = true
	}
	return
}

func printStatus(out io.Writer, s status) error {
	if _, err := fmt.Fprintf(out, "Backups (%d):\n", len(s.Backups)); err != nil {
		return err
	}
	for _, name := range s.Backups {
		if _, err := fmt.Fprintln(out, "  "+name); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(out, "Incomplete (%d):\n", len(s.Incomplete)); err != nil {
		return err
	}
	for _, name := range s.Incomplete {
		if _, err := fmt.Fprintln(out, "  "+name); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(out, "Pending deletions (%d):\n", len(s.PendingDeletions)); err != nil {
		return err
	}
	for _, name := range s.PendingDeletions {
		if _, err := fmt.Fprintln(out, "  "+name); err != nil {
			return err
		}
	}
	last := "none"
	if s.LastSuccess != "" {
		last = s.LastSuccess
	}
	_, err := fmt.Fprintln(out, "Last success: "+last)
	return err
}
