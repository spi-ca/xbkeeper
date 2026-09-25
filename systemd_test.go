package main

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
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
		"Description":       "Create and prepare a local full MySQL backup with xbkeeper",
		"After":             "mysqld.service", // ordering only: no DB dependency or condition-based skip
		"RequiresMountsFor": "/var/backups/xtrabackup",
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

func TestPackagedTmpfilesContract(t *testing.T) {
	b, err := os.ReadFile("packaging/tmpfiles/xbkeeper.conf")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "#") || lines[1] != "d /var/backups/xtrabackup :0700 :root :root - -" {
		t.Fatalf("tmpfiles must create only absent default root without repairing existing attributes: %q", b)
	}
	makefile, err := os.ReadFile("Makefile")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(makefile), "packaging/tmpfiles/xbkeeper.conf") {
		t.Fatal("source archive omits tmpfiles")
	}
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
	if !strings.Contains(pkg, "pkgver=20260926\npkgrel=6\n") {
		t.Error("package must be version 20260926, release 6")
	}
	if strings.Count(pkg, "backup=(") != 1 || !strings.Contains(pkg, "\n  backup=('etc/xbkeeper/xbkeeper.toml' 'etc/mysql/xbkeeper.cnf')\n") {
		t.Error("package must protect both operator config and MySQL option template with pacman backup")
	}
	if strings.Contains(pkg, "install=") || strings.Contains(pkg, ".install") {
		t.Error("package must not use an install hook")
	}
	_, body, found := strings.Cut(pkg, "package_xbkeeper() {\n")
	if !found {
		t.Fatal("missing package() body")
	}
	// Each payload install is explicit. No active credentials, backup directory,
	// hooks or unit activation are installed.
	want := `  depends=('xtrabackup')
  backup=('etc/xbkeeper/xbkeeper.toml' 'etc/mysql/xbkeeper.cnf')
  cd "${pkgbase}-v${pkgver}-${pkgrel}"
  install -Dm755 xbkeeper "${pkgdir}/usr/bin/xbkeeper"
  install -Dm644 LICENSE "${pkgdir}/usr/share/licenses/${pkgname}/LICENSE"
  install -Dm644 LICENSES/go-toml-MIT.txt "${pkgdir}/usr/share/licenses/${pkgname}/go-toml-MIT.txt"
  install -Dm644 README.md "${pkgdir}/usr/share/doc/${pkgname}/README.md"
  install -Dm644 docs/integrity.md "${pkgdir}/usr/share/doc/${pkgname}/docs/integrity.md"
  install -Dm644 docs/systemd-migration.md "${pkgdir}/usr/share/doc/${pkgname}/docs/systemd-migration.md"
  install -Dm644 docs/toml-migration.md "${pkgdir}/usr/share/doc/${pkgname}/docs/toml-migration.md"
  install -Dm644 examples/xbkeeper.toml "${pkgdir}/usr/share/doc/${pkgname}/examples/xbkeeper.toml"
  install -Dm644 examples/xbkeeper.cnf "${pkgdir}/usr/share/doc/${pkgname}/examples/xbkeeper.cnf"
  install -Dm644 examples/README.md "${pkgdir}/usr/share/doc/${pkgname}/examples/README.md"
  install -dm700 "${pkgdir}/etc/xbkeeper"
  install -m600 examples/xbkeeper.toml "${pkgdir}/etc/xbkeeper/xbkeeper.toml"
  install -dm755 "${pkgdir}/etc/mysql"
  install -m600 examples/xbkeeper.cnf "${pkgdir}/etc/mysql/xbkeeper.cnf"
  install -Dm644 packaging/arch/README.md \
    "${pkgdir}/usr/share/doc/${pkgname}/packaging/arch/README.md"
  install -Dm644 packaging/systemd/xbkeeper.service "${pkgdir}/usr/lib/systemd/system/xbkeeper.service"
  install -Dm644 packaging/systemd/xbkeeper.timer "${pkgdir}/usr/lib/systemd/system/xbkeeper.timer"
  install -Dm644 packaging/tmpfiles/xbkeeper.conf "${pkgdir}/usr/lib/tmpfiles.d/xbkeeper.conf"
}
`
	body, _, _ = strings.Cut(body, "\npackage_xbkeeper-debug() {")
	if body != want {
		t.Errorf("package_xbkeeper() payload differs from explicit binary/license/docs/config/units allowlist:\n%s", body)
	}
	_, debugBody, found := strings.Cut(pkg, "package_xbkeeper-debug() {\n")
	if !found {
		t.Fatal("missing explicit debug split package")
	}
	const debugWant = `  pkgdesc='Detached debugging symbols for xbkeeper'
  depends=("xbkeeper=${pkgver}-${pkgrel}")
  cd "${pkgbase}-v${pkgver}-${pkgrel}"
  install -Dm644 xbkeeper.debug "${pkgdir}/usr/lib/debug/usr/bin/xbkeeper.debug"
  install -Dm644 LICENSE "${pkgdir}/usr/share/licenses/${pkgname}/LICENSE"
  install -Dm644 LICENSES/go-toml-MIT.txt "${pkgdir}/usr/share/licenses/${pkgname}/go-toml-MIT.txt"
}
`
	if debugBody != debugWant {
		t.Errorf("debug payload/dependency differs from symbols/licenses-only allowlist:\n%s", debugBody)
	}
}

// Execute the actual package() recipe against a disposable source fixture, never
// against a live /etc or systemd installation. This catches mode regressions that
// a string-only allowlist cannot detect.
func TestPackageConfigPayloadModes(t *testing.T) {
	pkg, err := os.ReadFile("packaging/arch/PKGBUILD")
	if os.IsNotExist(err) {
		t.Skip("PKGBUILD is not included in the source distribution")
	}
	if err != nil {
		t.Fatal(err)
	}
	_, body, found := strings.Cut(string(pkg), "package_xbkeeper() {\n")
	if !found {
		t.Fatal("missing package_xbkeeper() body")
	}
	body, _, _ = strings.Cut(body, "\npackage_xbkeeper-debug() {")
	makefile, err := os.ReadFile("Makefile")
	if err != nil {
		t.Fatal(err)
	}
	_, versionLine, found := strings.Cut(string(makefile), "VERSION := v")
	if !found {
		t.Fatal("missing pinned version")
	}
	version, _, _ := strings.Cut(versionLine, "\n")
	pkgver, pkgrel, found := strings.Cut(version, "-")
	if !found {
		t.Fatal("invalid pinned version")
	}
	root := t.TempDir()
	source := filepath.Join(root, "xbkeeper-v"+version)
	for _, name := range []string{
		"xbkeeper", "LICENSE", "LICENSES/go-toml-MIT.txt", "README.md",
		"docs/integrity.md", "docs/systemd-migration.md", "docs/toml-migration.md",
		"examples/xbkeeper.toml", "examples/xbkeeper.cnf", "examples/README.md", "packaging/arch/README.md",
		"packaging/systemd/xbkeeper.service", "packaging/systemd/xbkeeper.timer", "packaging/tmpfiles/xbkeeper.conf",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(source, name)), 0700); err != nil {
			t.Fatal(err)
		}
		contents := []byte("fixture\n")
		if name == "examples/xbkeeper.toml" || name == "examples/xbkeeper.cnf" {
			contents, err = os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(source, name), contents, 0600); err != nil {
			t.Fatal(err)
		}
	}
	pkgdir := filepath.Join(root, "payload")
	cmd := exec.Command("bash", "-e", "-c", "package_xbkeeper() {\n"+body+"\npackage_xbkeeper\n")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "pkgname=xbkeeper", "pkgbase=xbkeeper", "pkgver="+pkgver, "pkgrel="+pkgrel, "pkgdir="+pkgdir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("package() failed: %v: %s", err, output)
	}
	parent := filepath.Join(pkgdir, "etc/xbkeeper")
	config := filepath.Join(parent, "xbkeeper.toml")
	for _, tc := range []struct {
		path string
		mode os.FileMode
	}{
		{parent, 0700},
		{config, 0600},
		{filepath.Join(pkgdir, "etc/mysql"), 0755},
		{filepath.Join(pkgdir, "etc/mysql/xbkeeper.cnf"), 0600},
		{filepath.Join(pkgdir, "usr/share/doc/xbkeeper/examples/xbkeeper.cnf"), 0644},
		{filepath.Join(pkgdir, "usr/share/doc/xbkeeper/examples/xbkeeper.toml"), 0644},
		{filepath.Join(pkgdir, "usr/lib/systemd/system/xbkeeper.service"), 0644},
		{filepath.Join(pkgdir, "usr/lib/systemd/system/xbkeeper.timer"), 0644},
		{filepath.Join(pkgdir, "usr/lib/tmpfiles.d/xbkeeper.conf"), 0644},
	} {
		info, err := os.Stat(tc.path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != tc.mode {
			t.Errorf("%s mode = %04o, want %04o", tc.path, info.Mode().Perm(), tc.mode)
		}
	}
	defaultConfig, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	docConfig, err := os.ReadFile(filepath.Join(pkgdir, "usr/share/doc/xbkeeper/examples/xbkeeper.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(defaultConfig) != string(docConfig) {
		t.Error("packaged default differs from documentation example")
	}
	mysqlTemplate, err := os.ReadFile(filepath.Join(pkgdir, "etc/mysql/xbkeeper.cnf"))
	if err != nil {
		t.Fatal(err)
	}
	mysqlDoc, err := os.ReadFile(filepath.Join(pkgdir, "usr/share/doc/xbkeeper/examples/xbkeeper.cnf"))
	if err != nil {
		t.Fatal(err)
	}
	if string(mysqlTemplate) != string(mysqlDoc) {
		t.Error("packaged MySQL template differs from documentation example")
	}
	mysqlEntries, err := os.ReadDir(filepath.Join(pkgdir, "etc/mysql"))
	if err != nil {
		t.Fatal(err)
	}
	if len(mysqlEntries) != 1 || mysqlEntries[0].Name() != "xbkeeper.cnf" {
		t.Errorf("unexpected /etc/mysql payload: %v", mysqlEntries)
	}
	for _, name := range []string{"var/backups", "etc/systemd", "etc/xbkeeper/xbkeeper.install"} {
		if _, err := os.Lstat(filepath.Join(pkgdir, name)); !os.IsNotExist(err) {
			t.Errorf("unexpected package payload at %s: %v", name, err)
		}
	}
}

func TestPackagedMySQLOptionTemplateHasNoActiveSettings(t *testing.T) {
	content, err := os.ReadFile("examples/xbkeeper.cnf")
	if err != nil {
		t.Fatal(err)
	}
	section := false
	user, password := false, false
	for _, raw := range bytes.Split(content, []byte("\n")) {
		line := strings.TrimSpace(string(raw))
		if strings.HasPrefix(line, "#") || line == "" {
			if strings.HasPrefix(line, "# user=") {
				user = line == "# user=YOUR_BACKUP_USER"
			}
			if strings.HasPrefix(line, "# password=") {
				password = line == "# password=YOUR_BACKUP_PASSWORD"
			}
			continue
		}
		if line == "[xtrabackup]" && !section {
			section = true
			continue
		}
		t.Errorf("unexpected active MySQL option: %q", line)
	}
	if !section || !user || !password {
		t.Error("MySQL template must have [xtrabackup] and commented user/password placeholders")
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
