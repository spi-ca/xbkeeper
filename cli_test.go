//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIFailureResultIsNotDuplicated(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "xbkeeper")
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
	missing := filepath.Join(t.TempDir(), "missing.toml")
	for _, command := range []string{"backup", "verify", "status"} {
		for _, asJSON := range []bool{false, true} {
			args := []string{command, "--config", missing}
			if asJSON {
				args = append(args, "--json")
			}
			var out, stderr bytes.Buffer
			cmd := exec.Command(binary, args...)
			cmd.Stdout, cmd.Stderr = &out, &stderr
			var exit *exec.ExitError
			if err := cmd.Run(); !errors.As(err, &exit) || exit.ExitCode() != 1 {
				t.Fatalf("%v: expected exit 1, got %v", args, err)
			}
			if stderr.Len() != 0 {
				t.Fatalf("%v: duplicate diagnostic: %q", args, stderr.String())
			}
			if asJSON {
				var result commandEnvelope
				if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Command != command || result.OK || result.Error == nil || result.Data != nil {
					t.Fatalf("%v: invalid failure envelope: %s (%v)", args, out.String(), err)
				}
			} else if !strings.HasPrefix(out.String(), "Command: "+command+"\nResult: failed: ") {
				t.Fatalf("%v: invalid human failure: %q", args, out.String())
			}
		}
	}
	var out, stderr bytes.Buffer
	cmd := exec.Command(binary, "status", "--unknown")
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err == nil || out.Len() != 0 || !strings.Contains(stderr.String(), "xbkeeper:") {
		t.Fatalf("parse failure: %v, stdout=%q stderr=%q", err, out.String(), stderr.String())
	}
}
