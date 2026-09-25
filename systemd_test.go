package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// parseUnit rejects duplicate keys and sections so a later directive cannot silently
// override a contract assertion. It reads only repository files, never systemd state.
func parseUnit(t *testing.T, name string) map[string]map[string]string {
	t.Helper()
	file, err := os.Open(filepath.Join("packaging", "systemd", name))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	sections := make(map[string]map[string]string)
	var current string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			current = strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
			if _, exists := sections[current]; exists {
				t.Fatalf("%s: duplicate section %s", name, current)
			}
			sections[current] = make(map[string]string)
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || current == "" || key == "" {
			t.Fatalf("%s: invalid unit line %q", name, line)
		}
		if _, exists := sections[current][key]; exists {
			t.Fatalf("%s: duplicate directive %s in %s", name, key, current)
		}
		sections[current][key] = value
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return sections
}

func assertUnitSection(t *testing.T, sections map[string]map[string]string, section string, want map[string]string) {
	t.Helper()
	got, ok := sections[section]
	if !ok {
		t.Fatalf("missing [%s] section", section)
	}
	if len(got) != len(want) {
		t.Errorf("[%s] directives = %v; want %v", section, got, want)
	}
	for key, expected := range want {
		if got[key] != expected {
			t.Errorf("[%s] %s = %q; want %q", section, key, got[key], expected)
		}
	}
}

func TestPackagedServiceContract(t *testing.T) {
	unit := parseUnit(t, "xbkeeper.service")
	if len(unit) != 2 {
		t.Fatalf("service sections = %v; want only Unit and Service", unit)
	}
	assertUnitSection(t, unit, "Unit", map[string]string{
		"Description": "Create and prepare a local full MySQL backup with xbkeeper",
		"After":       "mysqld.service", // ordering only: no Wants/Requires or condition-based skip
	})
	assertUnitSection(t, unit, "Service", map[string]string{
		"Type": "oneshot",
		"User": "root", "Group": "root", "UMask": "0077",
		"ExecStart":       "/usr/bin/xbkeeper backup --config /etc/xbkeeper/xbkeeper.toml",
		"TimeoutStartSec": "6h", "TimeoutStopSec": "2min",
		"KillMode": "control-group", "ProtectSystem": "strict",
		"ReadWritePaths": "/var/backups/xtrabackup",
		"ProtectHome":    "true", "PrivateTmp": "true", "NoNewPrivileges": "true",
		"RestrictAddressFamilies": "AF_UNIX",
	})
}

func TestPackagedTimerContract(t *testing.T) {
	unit := parseUnit(t, "xbkeeper.timer")
	if len(unit) != 3 {
		t.Fatalf("timer sections = %v; want only Unit, Timer and Install", unit)
	}
	assertUnitSection(t, unit, "Unit", map[string]string{
		"Description": "Run xbkeeper daily at 03:00 host local time",
	})
	assertUnitSection(t, unit, "Timer", map[string]string{
		"OnCalendar": "*-*-* 03:00:00", "Persistent": "true", "Unit": "xbkeeper.service",
	})
	assertUnitSection(t, unit, "Install", map[string]string{"WantedBy": "timers.target"})
}

func TestPackageInstallsUnitsWithoutActivationOrProvisioning(t *testing.T) {
	content, err := os.ReadFile("packaging/arch/PKGBUILD")
	if os.IsNotExist(err) {
		// The source tarball cannot include its own checksum-bearing PKGBUILD.
		// The repository test checks the recipe; the package build checks its payload.
		t.Skip("PKGBUILD is not included in the source distribution")
	}
	if err != nil {
		t.Fatal(err)
	}
	pkg := string(content)
	if !strings.Contains(pkg, "pkgver=20260926\npkgrel=1\n") {
		t.Error("package must be version 20260926, release 1")
	}
	if strings.Contains(pkg, "install=") || strings.Contains(pkg, ".install") {
		t.Error("package must not use an install hook")
	}
	_, body, found := strings.Cut(pkg, "package() {\n")
	if !found {
		t.Fatal("missing package() body")
	}
	// Each payload install is explicit. Nothing creates config, credential or
	// backup directories, starts/enables units, or installs active host config.
	want := `  cd "${pkgname}-v${pkgver}-${pkgrel}"
  install -Dm755 xbkeeper "${pkgdir}/usr/bin/xbkeeper"
  install -Dm644 LICENSE "${pkgdir}/usr/share/licenses/${pkgname}/LICENSE"
  install -Dm644 LICENSES/go-toml-MIT.txt "${pkgdir}/usr/share/licenses/${pkgname}/go-toml-MIT.txt"
  install -Dm644 README.md "${pkgdir}/usr/share/doc/${pkgname}/README.md"
  install -Dm644 docs/integrity.md "${pkgdir}/usr/share/doc/${pkgname}/docs/integrity.md"
  install -Dm644 docs/systemd-migration.md "${pkgdir}/usr/share/doc/${pkgname}/docs/systemd-migration.md"
  install -Dm644 docs/toml-migration.md "${pkgdir}/usr/share/doc/${pkgname}/docs/toml-migration.md"
  install -Dm644 examples/xbkeeper.toml "${pkgdir}/usr/share/doc/${pkgname}/examples/xbkeeper.toml"
  install -Dm644 examples/README.md "${pkgdir}/usr/share/doc/${pkgname}/examples/README.md"
  install -Dm644 packaging/arch/README.md \
    "${pkgdir}/usr/share/doc/${pkgname}/packaging/arch/README.md"
  install -Dm644 packaging/systemd/xbkeeper.service "${pkgdir}/usr/lib/systemd/system/xbkeeper.service"
  install -Dm644 packaging/systemd/xbkeeper.timer "${pkgdir}/usr/lib/systemd/system/xbkeeper.timer"
}
`
	if body != want {
		t.Errorf("package() payload differs from explicit binary/license/docs/units allowlist:\n%s", body)
	}
}

func TestSourceArchiveNormalizesFileModes(t *testing.T) {
	content, err := os.ReadFile("Makefile")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "--numeric-owner --mode=0644") {
		t.Error("source tar must normalize regular-file modes independently of checkout permissions")
	}
}
