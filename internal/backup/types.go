//go:build linux

package backup

import "regexp"

const formatVersion = 2
const maxLog = 1 << 20

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
