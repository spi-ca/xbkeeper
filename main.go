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

var managedName = regexp.MustCompile(`^(backup|\.inprogress)-[0-9]{8}T[0-9]{6}Z-[0-9a-f]{16}$`)

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
	if len(args) < 1 || (args[0] != "backup" && args[0] != "status" && args[0] != "verify") {
		return errors.New("usage: xbkeeper {backup|status|verify} --config PATH | xbkeeper verify --config PATH --backup NAME | xbkeeper version")
	}
	cmd := args[0]
	f := flag.NewFlagSet(cmd, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	cfgPath := f.String("config", "", "absolute config path")
	var selected *string
	if cmd == "verify" {
		selected = f.String("backup", "", "completed backup basename")
	}
	if err := f.Parse(args[1:]); err != nil || f.NArg() != 0 || *cfgPath == "" {
		return errors.New("expected --config ABSOLUTE_PATH")
	}
	c, err := loadConfig(*cfgPath)
	if err != nil {
		return phaseError("config", err)
	}
	if cmd == "status" {
		s, err := inspect(c.BackupDir)
		if err != nil {
			return phaseError("validation", err)
		}
		return json.NewEncoder(out).Encode(s)
	}
	if cmd == "verify" {
		return verify(ctx, c.BackupDir, *selected, out, events)
	}
	return backup(ctx, c, out, events)
}
