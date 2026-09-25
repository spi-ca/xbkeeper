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

var managedName = regexp.MustCompile(`^(backup|inprogress|\.inprogress)-[0-9]{8}T[0-9]{6}Z-[0-9a-f]{16}$`)

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
	Backups     []string `json:"backups"`
	Incomplete  []string `json:"incomplete"`
	LastSuccess string   `json:"last_success,omitempty"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "xbkeeper:", err)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string, out io.Writer) error {
	return runWithLog(ctx, args, out, os.Stderr)
}
func runWithLog(ctx context.Context, args []string, out, logOutput io.Writer) error {
	events := slog.New(slog.NewTextHandler(logOutput, nil))
	if len(args) == 1 && args[0] == "version" {
		_, err := fmt.Fprintln(out, Version)
		return err
	}
	cmd, cfgPath, selected, flags, err := parseOptions(args)
	if err != nil {
		return err
	}
	c, err := loadConfig(cfgPath)
	if err != nil {
		return phaseError("config", err)
	}
	if cmd == "status" || cmd == "verify" {
		missing, err := backupRootMissing(c.BackupDir)
		if err != nil {
			return phaseError("config", err)
		}
		if missing {
			return fmt.Errorf("%s: backup directory missing; run backup to initialize it", cmd)
		}
	}
	if cmd == "status" {
		s, err := inspect(c.BackupDir)
		if err != nil {
			return phaseError("validation", err)
		}
		if flags.json {
			return json.NewEncoder(out).Encode(s)
		}
		return printStatus(out, s)
	}
	if cmd == "verify" {
		return verify(ctx, c.BackupDir, selected, out, events)
	}
	return backup(ctx, c, out, events, flags.verbose)
}

type commandFlags struct{ json, verbose bool }

func parseOptions(args []string) (cmd, cfgPath, selected string, flags commandFlags, err error) {
	if len(args) < 1 || (args[0] != "backup" && args[0] != "status" && args[0] != "verify") {
		err = errors.New("usage: xbkeeper backup [--config PATH] [--verbose] | status [--config PATH] [--json] | verify [--config PATH] [--backup NAME] | version")
		return
	}
	cmd = args[0]
	f := flag.NewFlagSet(cmd, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.StringVar(&cfgPath, "config", defaultConfigPath, "absolute config path")
	if cmd == "verify" {
		f.StringVar(&selected, "backup", "", "completed backup basename")
	}
	if cmd == "status" {
		f.BoolVar(&flags.json, "json", false, "machine-readable status")
	}
	if cmd == "backup" {
		f.BoolVar(&flags.verbose, "verbose", false, "stream bounded child output to stderr")
	}
	if err = f.Parse(args[1:]); err != nil || f.NArg() != 0 || cfgPath == "" {
		cmd, cfgPath, selected, flags = "", "", "", commandFlags{}
		err = errors.New("invalid command flags or arguments")
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
	last := "none"
	if s.LastSuccess != "" {
		last = s.LastSuccess
	}
	_, err := fmt.Fprintln(out, "Last success: "+last)
	return err
}
