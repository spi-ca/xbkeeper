//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"amuz.es/src/spi-ca/xbkeeper/internal/backup"
	"amuz.es/src/spi-ca/xbkeeper/internal/cli"
)

// Version can be set with -ldflags '-X main.Version=v1.2.3'.
var Version = "dev"

func openKeeper(path string) (cli.Operations, error) { return backup.Open(path) }

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr, Version, openKeeper); err != nil {
		var presented *cli.PresentedError
		if !errors.As(err, &presented) {
			fmt.Fprintln(os.Stderr, "xbkeeper:", err)
		}
		os.Exit(1)
	}
}
