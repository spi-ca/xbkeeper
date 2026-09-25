package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseVersionContract(t *testing.T) {
	workflow, err := os.ReadFile(".github/workflows/release-verify.yml")
	if os.IsNotExist(err) {
		// Source distributions exclude the release workflow and PKGBUILD.
		t.Skip("release workflow is not included in the source distribution")
	}
	if err != nil {
		t.Fatal(err)
	}
	makefile, err := os.ReadFile("Makefile")
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := os.ReadFile("packaging/arch/PKGBUILD")
	if err != nil {
		t.Fatal(err)
	}
	const version = "v20260926-2"
	if !strings.Contains(string(makefile), "VERSION := "+version+"\n") {
		t.Fatal("Makefile must pin the full canonical version")
	}
	if !strings.Contains(string(pkg), "pkgver=20260926\npkgrel=2\n") {
		t.Fatal("Arch pkgver/pkgrel must match the pinned version")
	}
	for _, part := range []string{
		`source=("${pkgname}-v${pkgver}-${pkgrel}.tar.gz")`,
		`-ldflags "-X main.Version=v${pkgver}-${pkgrel}"`,
	} {
		if !strings.Contains(string(pkg), part) {
			t.Errorf("PKGBUILD missing %s", part)
		}
	}
	if got := strings.Count(string(pkg), `cd "${pkgname}-v${pkgver}-${pkgrel}"`); got != 3 {
		t.Errorf("PKGBUILD must use the versioned source directory in build/check/package, got %d", got)
	}
	for _, part := range []string{
		"DIST := dist/xbkeeper-$(VERSION).tar.gz",
		`-ldflags "-X main.Version=$(VERSION)"`,
		`xbkeeper-$(VERSION)/`,
	} {
		if !strings.Contains(string(makefile), part) {
			t.Errorf("Makefile missing %s", part)
		}
	}
	for _, part := range []string{
		`test "$(./build/xbkeeper version)" = "$VERSION"`,
		`dist/xbkeeper-$VERSION.tar.gz`,
		`$unpacked/xbkeeper-$VERSION/build/xbkeeper`,
		`name: xbkeeper-${{ steps.version.outputs.version }}-source`,
	} {
		if !strings.Contains(string(workflow), part) {
			t.Errorf("release workflow missing full-version check: %s", part)
		}
	}

	// Run the actual CI tag-validation shell block against fixture inputs, so
	// changing its regex or pkgver/pkgrel mapping cannot silently drift.
	_, section, found := strings.Cut(string(workflow), "      - name: Validate tag against package versions\n")
	if !found {
		t.Fatal("missing CI version validation step")
	}
	section, _, found = strings.Cut(section, "      - name: Test, race check and vet\n")
	if !found {
		t.Fatal("missing CI validation step boundary")
	}
	_, script, found := strings.Cut(section, "        run: |\n")
	if !found {
		t.Fatal("missing CI version validation script")
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSuffix(script, "\n"), "\n") {
		if !strings.HasPrefix(line, "          ") {
			t.Fatalf("unexpected CI script indentation: %q", line)
		}
		lines = append(lines, strings.TrimPrefix(line, "          "))
	}
	validate := strings.Join(lines, "\n") + "\n"

	for _, tc := range []struct {
		name, tag, makeVersion, pkgver, pkgrel string
		valid                                  bool
	}{
		{"current", version, version, "20260926", "2", true},
		{"same-day-revision", "v20260926-3", "v20260926-3", "20260926", "3", true},
		{"invalid-date", "v20261399-1", "v20261399-1", "20261399", "1", false},
		{"invalid-leap-day", "v20260229-1", "v20260229-1", "20260229", "1", false},
		{"valid-leap-day", "v20280229-1", "v20280229-1", "20280229", "1", true},
		{"old-semver", "v0.3.0", "v0.3.0", "0.3.0", "1", false},
		{"zero-revision", "v20260926-0", "v20260926-0", "20260926", "0", false},
		{"no-v-prefix", "20260926-1", "20260926-1", "20260926", "1", false},
		{"mismatched-make", version, "v20260925-1", "20260926", "2", false},
		{"mismatched-pkgver", version, version, "20260925", "2", false},
		{"mismatched-pkgrel", version, version, "20260926", "1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, "packaging", "arch"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte("VERSION := "+tc.makeVersion+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "packaging", "arch", "PKGBUILD"), []byte("pkgver="+tc.pkgver+"\npkgrel="+tc.pkgrel+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(dir, "output")
			cmd := exec.Command("bash", "-c", validate)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "RELEASE_TAG="+tc.tag, "GITHUB_OUTPUT="+output)
			result, err := cmd.CombinedOutput()
			if (err == nil) != tc.valid {
				t.Fatalf("validation err=%v, want valid=%t: %s", err, tc.valid, result)
			}
			if tc.valid {
				got, err := os.ReadFile(output)
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != "version="+tc.tag+"\n" {
					t.Errorf("CI output = %q, want full tag", got)
				}
			}
		})
	}
}

func TestReleasePinnedChecksum(t *testing.T) {
	workflow, err := os.ReadFile(".github/workflows/release-verify.yml")
	if os.IsNotExist(err) {
		t.Skip("workflow not in source distribution")
	}
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(workflow), "      - name: Verify pinned Arch source checksum\n")
	if !ok {
		t.Fatal("missing pinned checksum step")
	}
	section, _, ok = strings.Cut(section, "      # actions/upload-artifact")
	if !ok {
		t.Fatal("missing checksum step boundary")
	}
	_, script, ok := strings.Cut(section, "        run: |\n")
	if !ok {
		t.Fatal("missing checksum script")
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSuffix(script, "\n"), "\n") {
		if !strings.HasPrefix(line, "          ") {
			t.Fatalf("invalid indentation: %q", line)
		}
		lines = append(lines, strings.TrimPrefix(line, "          "))
	}
	for _, valid := range []bool{true, false} {
		t.Run(fmt.Sprintf("valid=%t", valid), func(t *testing.T) {
			dir := t.TempDir()
			for _, name := range []string{"dist", "packaging/arch"} {
				if err := os.MkdirAll(filepath.Join(dir, name), 0700); err != nil {
					t.Fatal(err)
				}
			}
			payload := []byte("fixture source archive")
			if err := os.WriteFile(filepath.Join(dir, "dist/xbkeeper-v20260926-2.tar.gz"), payload, 0600); err != nil {
				t.Fatal(err)
			}
			sum := fmt.Sprintf("%x", sha256.Sum256(payload))
			if !valid {
				sum = strings.Repeat("0", 64)
			}
			if err := os.WriteFile(filepath.Join(dir, "packaging/arch/PKGBUILD"), []byte("sha256sums=('"+sum+"')\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", "-c", strings.Join(lines, "\n"))
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "VERSION=v20260926-2")
			output, err := cmd.CombinedOutput()
			if (err == nil) != valid {
				t.Fatalf("valid=%t: %v: %s", valid, err, output)
			}
		})
	}
}
