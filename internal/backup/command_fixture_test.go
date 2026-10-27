//go:build linux

package backup

import (
	"context"
	"io"
	"os"

	"amuz.es/src/spi-ca/xbkeeper/internal/backup/report"
	"amuz.es/src/spi-ca/xbkeeper/internal/cli"
)

// These aliases are fixture vocabulary, not exported engine test hooks. Tests
// exercise the real parser/presenter with the same engine wiring as cmd/xbkeeper,
// while fault-injection variables and filesystem helpers remain private here.
type status = report.Status
type verifyReport = report.Verify
type commandEnvelope struct {
	Command string  `json:"command"`
	OK      bool    `json:"ok"`
	Data    any     `json:"data"`
	Error   *string `json:"error"`
}

func run(ctx context.Context, args []string, out io.Writer) error {
	return runWithLog(ctx, args, out, os.Stderr)
}

func runWithLog(ctx context.Context, args []string, out, logs io.Writer) error {
	return cli.Run(ctx, args, out, logs, "dev", func(path string) (cli.Operations, error) { return Open(path) })
}
