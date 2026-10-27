//go:build linux

package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestCommandConfigSelection(t *testing.T) {
	for _, tc := range []struct {
		name, cmd, path, backup string
		args                    []string
		valid                   bool
	}{
		{"backup default", "backup", defaultConfigPath, "", []string{"backup"}, true},
		{"status default", "status", defaultConfigPath, "", []string{"status"}, true},
		{"status json", "status", defaultConfigPath, "", []string{"status", "--json"}, true},
		{"backup verbose", "backup", defaultConfigPath, "", []string{"backup", "--verbose"}, true},
		{"backup json", "backup", defaultConfigPath, "", []string{"backup", "--json"}, true},
		{"status verbose", "status", defaultConfigPath, "", []string{"status", "--verbose"}, true},
		{"global verbose backup", "backup", defaultConfigPath, "", []string{"--verbose", "backup"}, true},
		{"global verbose status", "status", defaultConfigPath, "", []string{"--verbose", "status"}, true},
		{"global verbose verify", "verify", defaultConfigPath, "", []string{"--verbose", "verify"}, true},
		{"verify json verbose", "verify", defaultConfigPath, "", []string{"verify", "--json", "--verbose"}, true},
		{"verify default", "verify", defaultConfigPath, "", []string{"verify"}, true},
		{"verify selected default", "verify", defaultConfigPath, "backup-20200101T000000Z-0000000000000000", []string{"verify", "--backup", "backup-20200101T000000Z-0000000000000000"}, true},
		{"override", "backup", "/tmp/config.toml", "", []string{"backup", "--config", "/tmp/config.toml"}, true},
		{"verify override", "verify", "/tmp/config.toml", "chosen", []string{"verify", "--config=/tmp/config.toml", "--backup", "chosen"}, true},
		{"explicit empty", "", "", "", []string{"status", "--config="}, false},
		{"missing argument", "", "", "", []string{"backup", "--config"}, false},
		{"unknown flag", "", "", "", []string{"status", "--unknown"}, false},
		{"wrong command flag", "", "", "", []string{"status", "--backup", "name"}, false},
		{"positional", "", "", "", []string{"verify", "name"}, false},
		{"trailing positional", "", "", "", []string{"backup", "--config", "/tmp/config.toml", "extra"}, false},
		{"unknown command", "", "", "", []string{"unknown"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, path, backup, flags, err := parseOptions(tc.args)
			if strings.HasPrefix(tc.name, "global verbose") && !flags.verbose {
				t.Fatal("global verbose not applied")
			}
			if (err == nil) != tc.valid {
				t.Fatalf("parse error = %v, valid = %v", err, tc.valid)
			}
			if tc.valid && (cmd != tc.cmd || path != tc.path || backup != tc.backup) {
				t.Fatalf("parse = %q %q %q, want %q %q %q", cmd, path, backup, tc.cmd, tc.path, tc.backup)
			}
		})
	}
}

func TestVersion(t *testing.T) {
	var b bytes.Buffer
	if err := Run(context.Background(), []string{"version"}, &b, &bytes.Buffer{}, "dev", nil); err != nil || strings.TrimSpace(b.String()) != "dev" {
		t.Fatal(err, b.String())
	}
}
