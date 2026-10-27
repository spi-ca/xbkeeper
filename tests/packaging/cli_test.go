//go:build linux

package packaging

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
	if output, err := exec.Command("go", "build", "-ldflags", "-X main.Version=package-layout-test", "-o", binary, "./cmd/xbkeeper").CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
	if output, err := exec.Command(binary, "version").CombinedOutput(); err != nil || string(output) != "package-layout-test\n" {
		t.Fatalf("linked version: %v %q", err, output)
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
				var result struct {
					Command string  `json:"command"`
					OK      bool    `json:"ok"`
					Error   *string `json:"error"`
					Data    any     `json:"data"`
				}
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
