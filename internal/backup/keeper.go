//go:build linux

// Package backup owns configuration validation, backup lifecycle, storage safety,
// integrity and Linux child containment. It does not parse flags or write results.
package backup

import (
	"context"
	"fmt"
	"log/slog"

	"amuz.es/src/spi-ca/xbkeeper/internal/backup/report"
)

// Keeper runs operations with a validated configuration. Backup changes the
// process-wide umask to 0077; this engine is for the single-purpose executable,
// not concurrent embedded filesystem work. Calls must not overlap within a process.
type Keeper struct{ config config }

// Open validates the private config and existing path components without creating
// the backup root or accessing the live database. Errors are safe to present.
func Open(configPath string) (*Keeper, error) {
	c, err := loadConfig(configPath)
	if err != nil {
		return nil, phaseError("config", err)
	}
	return &Keeper{config: c}, nil
}

func (k *Keeper) existingRoot(command string) error {
	missing, err := backupRootMissing(k.config.BackupDir)
	if err != nil {
		return phaseError("config", err)
	}
	if missing {
		return fmt.Errorf("%s: backup directory missing; run backup to initialize it", command)
	}
	return nil
}

// Status inspects structural validity and preparation ordering without mutation.
func (k *Keeper) Status(ctx context.Context) (report.Status, error) {
	if err := k.existingRoot("status"); err != nil {
		return report.Status{}, err
	}
	s, err := inspectContext(ctx, k.config.BackupDir)
	return s, phaseError("validation", err)
}

// Verify audits content under the existing shared lock, retaining partial results.
func (k *Keeper) Verify(ctx context.Context, selected string, events *slog.Logger) (report.Verify, error) {
	if err := k.existingRoot("verify"); err != nil {
		return report.Verify{}, err
	}
	return verify(ctx, k.config.BackupDir, selected, events)
}

// Backup initializes storage if necessary, prepares, hashes, syncs, promotes and
// then retains backups under the exclusive lock. Events must be non-nil.
func (k *Keeper) Backup(ctx context.Context, events *slog.Logger, verbose bool) (report.Backup, error) {
	return backup(ctx, k.config, events, verbose)
}
