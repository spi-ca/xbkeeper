//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTOMLConfigStrictAndPrivate(t *testing.T) {
	f := setup(t)
	original, err := os.ReadFile(f.file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(f.file); err != nil {
		t.Fatalf("baseline config must be valid: %v", err)
	}
	for _, tc := range []struct {
		name, replaceKey, input, want string
	}{
		{"unknown", "", "other = 'secret-value'\n", "unknown or incorrectly cased config field"},
		{"case", "keep", "Keep = 1\n", "unknown or incorrectly cased config field"},
		{"case alias", "", "KEEP = 2\n", "unknown or incorrectly cased config field"},
		{"duplicate", "", "keep = 2\n", "invalid config TOML"},
		{"unknown table", "", "[server]\nkeep = 1\n", "unknown or incorrectly cased config field"},
		{"nested", "keep", "[keep]\nvalue = 1\n", "invalid config TOML"},
		{"dotted", "keep", "keep.value = 1\n", "invalid config TOML"},
		{"wrong int", "keep", "keep = 'secret-value'\n", "invalid config TOML"},
		{"wrong path", "backup_dir", "backup_dir = 1\n", "invalid config TOML"},
		{"negative keep", "keep", "keep = -1\n", "keep must be >= 1 and min_free_bytes >= 0"},
		{"zero keep", "keep", "keep = 0\n", "keep must be >= 1 and min_free_bytes >= 0"},
		{"negative reserve", "min_free_bytes", "min_free_bytes = -1\n", "keep must be >= 1 and min_free_bytes >= 0"},
		{"overflow", "keep", "keep = 9223372036854775808\n", "invalid config TOML"},
		{"float", "keep", "keep = 1.5\n", "invalid config TOML"},
		{"boolean", "keep", "keep = true\n", "invalid config TOML"},
		{"trailing syntax", "", "invalid secret-value\n", "invalid config TOML"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := string(original)
			if tc.replaceKey != "" {
				found := false
				for _, line := range strings.Split(text, "\n") {
					if strings.HasPrefix(line, tc.replaceKey+" = ") {
						text = strings.Replace(text, line+"\n", "", 1)
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("fixture missing field %s", tc.replaceKey)
				}
			}
			// Every negative case changes just one aspect of a complete valid config.
			if err := os.WriteFile(f.file, []byte(text+tc.input), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadConfig(f.file); err == nil || err.Error() != tc.want {
				t.Fatalf("error = %v, want %q (without input content)", err, tc.want)
			}
		})
	}
	for name, text := range map[string]string{
		"legacy JSON": fmt.Sprintf(`{"backup_dir":%q,"datadir":%q,"socket":%q,"defaults_file":%q,"keep":1,"min_free_bytes":0,"xtrabackup":%q}`, f.c.BackupDir, f.c.Datadir, f.c.Socket, f.c.DefaultsFile, f.c.Xtrabackup),
		"size limit":  string(original) + "#" + strings.Repeat("x", 64*1024),
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(f.file, []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			want := "invalid config TOML"
			if name == "size limit" {
				want = "config too large"
			}
			if _, err := loadConfig(f.file); err == nil || err.Error() != want {
				t.Fatalf("error = %v, want %q", err, want)
			}
		})
	}
	if err := os.WriteFile(f.file, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f.file, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(f.file); err == nil {
		t.Fatal("accepted public config")
	}
}

func TestTOMLCommentsEscapesSeparatorsAndDefault(t *testing.T) {
	f := setup(t)
	// Deliberately write basic (double-quoted) strings: Marshal uses literal
	// strings, which would make a replacement looking for double quotes a no-op.
	escapedSocket := strings.Replace(f.c.Socket, "mysql.sock", `mysql\u002esock`, 1)
	if escapedSocket == f.c.Socket {
		t.Fatal("fixture did not insert a TOML escape")
	}
	text := fmt.Sprintf(`# TOML comment
backup_dir = %q
datadir = %q
socket = "%s"
defaults_file = %q
keep = 1 # retained
min_free_bytes = 1_024
`, f.c.BackupDir, f.c.Datadir, escapedSocket, f.c.DefaultsFile)
	if err := os.WriteFile(f.file, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := loadConfig(f.file)
	if err != nil || got.MinFreeBytes != 1024 || got.Keep != 1 || got.Socket != f.c.Socket || got.Xtrabackup != "/usr/bin/xtrabackup" {
		t.Fatalf("comments, escapes, separators or absent executable default: %+v %v", got, err)
	}
}

func TestPackagedTOMLExample(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("examples", "xbkeeper.toml"))
	if err != nil {
		t.Fatal(err)
	}
	f := setup(t)
	// Substitute only host-specific sample paths; parsing and schema stay identical.
	sample := string(b)
	for _, pair := range [][2]string{{"/var/backups/xtrabackup", f.c.BackupDir}, {"/var/lib/mysql", f.c.Datadir}, {"/run/mysqld/mysqld.sock", f.c.Socket}, {"/etc/mysql/xbkeeper.cnf", f.c.DefaultsFile}, {"/usr/bin/xtrabackup", f.c.Xtrabackup}} {
		sample = strings.ReplaceAll(sample, pair[0], pair[1])
	}
	if err := os.WriteFile(f.file, []byte(sample), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := loadConfig(f.file)
	want := f.c
	want.Keep = 3
	want.MinFreeBytes = 10 * 1024 * 1024 * 1024
	if err != nil || got != want {
		t.Fatalf("example = %+v, want %+v: %v", got, want, err)
	}
}
