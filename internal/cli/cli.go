//go:build linux

// Package cli parses commands and presents results without depending on the
// backup implementation. Operations are supplied by the executable.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"

	"amuz.es/src/spi-ca/xbkeeper/internal/backup/report"
)

const defaultConfigPath = "/etc/xbkeeper/xbkeeper.toml"

// Operations is the command-facing boundary of a configured backup engine.
type Operations interface {
	Status(context.Context) (report.Status, error)
	Verify(context.Context, string, *slog.Logger) (report.Verify, error)
	Backup(context.Context, *slog.Logger, bool) (report.Backup, error)
}

// Opener loads and validates a private configuration without creating storage.
type Opener func(configPath string) (Operations, error)

// commandEnvelope is the only JSON value written to stdout for a parsed command.
// Data is nil for pre-result failures, but may contain partial verification results.
type commandEnvelope struct {
	Command string  `json:"command"`
	OK      bool    `json:"ok"`
	Data    any     `json:"data"`
	Error   *string `json:"error"`
}

// PresentedError marks an operational error successfully written to the result
// stream. Parse/writer errors still need stderr diagnostics. It unwraps to the
// original cause for cancellation/error matching.
type PresentedError struct{ cause error }

func (e *PresentedError) Error() string { return e.cause.Error() }
func (e *PresentedError) Unwrap() error { return e.cause }

// Run executes one invocation, keeping results and diagnostic events separate.
func Run(ctx context.Context, args []string, out, logOutput io.Writer, version string, open Opener) error {
	if len(args) == 1 && args[0] == "version" {
		_, err := fmt.Fprintln(out, version)
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
	keeper, operationErr := open(cfgPath)
	if operationErr == nil {
		switch cmd {
		case "status":
			var s report.Status
			s, operationErr = keeper.Status(ctx)
			if operationErr == nil {
				data = s
			}
		case "verify":
			var result report.Verify
			result, operationErr = keeper.Verify(ctx, selected, events)
			if len(result.Backups) != 0 {
				data = result
			}
		case "backup":
			var result report.Backup
			result, operationErr = keeper.Backup(ctx, events, flags.verbose)
			if operationErr == nil {
				data = result
			}
		}
	}
	if err := presentResult(out, cmd, flags.json, data, operationErr); err != nil {
		return errors.Join(operationErr, err) // Preserve cancellation and the writer failure.
	}
	if operationErr != nil {
		return &PresentedError{cause: operationErr}
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
	case report.Backup:
		_, err := fmt.Fprintln(out, "Backup: "+value.Name)
		return err
	case report.Status:
		return printStatus(out, value)
	case report.Verify:
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

func printStatus(out io.Writer, s report.Status) error {
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
