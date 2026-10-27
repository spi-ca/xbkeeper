package packaging

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func sourceFiles(t *testing.T) []string {
	t.Helper()
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("GNU make unavailable; source-list checks require the archive toolchain")
	}
	output, err := exec.Command("make", "--no-print-directory", "source-list").CombinedOutput()
	if err != nil {
		t.Fatalf("source-list: %v: %s", err, output)
	}
	return strings.Fields(string(output))
}

func TestSourceInventoryIncludesPackagesAndDocs(t *testing.T) {
	sources := sourceFiles(t)
	included := make(map[string]bool)
	for _, name := range sources {
		if included[name] {
			t.Errorf("duplicate source: %s", name)
		}
		included[name] = true
		fi, err := os.Lstat(name)
		if err != nil || !fi.Mode().IsRegular() {
			t.Fatalf("invalid source %s: %v", name, err)
		}
	}
	// Discover new package/test/docs files independently of the Makefile allowlist.
	for _, dir := range []string{"cmd", "internal", "tests", "docs"} {
		err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() && (strings.HasSuffix(path, ".go") || strings.HasSuffix(path, ".md")) && !included[filepath.ToSlash(path)] {
				t.Errorf("source distribution omits %s", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, forbidden := range []string{"main.go", "packaging/arch/PKGBUILD", "packaging/arch/.SRCINFO", "xbkeeper"} {
		if included[forbidden] {
			t.Errorf("unexpected distribution input: %s", forbidden)
		}
	}
}

var markdownLink = regexp.MustCompile(`\[[^\]]*\]\(([^)]+)\)`)
var anchorPunctuation = regexp.MustCompile(`[^a-z0-9_ -]`)

func checkDocLinks(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, match := range markdownLink.FindAllSubmatch(content, -1) {
			target := string(match[1])
			if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			name, anchor, _ := strings.Cut(target, "#")
			resolved := path
			if name != "" {
				resolved = filepath.Join(filepath.Dir(path), name)
			}
			rel, err := filepath.Rel(root, resolved)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				t.Errorf("%s: link escapes documentation root: %s", path, target)
				continue
			}
			fi, err := os.Stat(resolved)
			if err != nil {
				t.Errorf("%s: broken link %s: %v", path, target, err)
				continue
			}
			if anchor == "" || fi.IsDir() {
				continue
			}
			destination, err := os.ReadFile(resolved)
			if err != nil {
				return err
			}
			found := false
			for _, line := range strings.Split(string(destination), "\n") {
				if !strings.HasPrefix(line, "#") {
					continue
				}
				heading := strings.TrimSpace(strings.TrimLeft(line, "#"))
				slug := strings.ReplaceAll(anchorPunctuation.ReplaceAllString(strings.ToLower(heading), ""), " ", "-")
				if slug == anchor {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s: unknown anchor in %s", path, target)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDistributedDocumentationLinks(t *testing.T) {
	// A minimal source/doc-only fixture excludes build artifacts and private files.
	root := t.TempDir()
	names := []string{"README.md", "packaging/arch/README.md"}
	for _, pattern := range []string{"docs/*.md", "examples/*"} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, matches...)
	}
	for _, name := range names {
		target := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			t.Fatal(err)
		}
		contents, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, contents, 0600); err != nil {
			t.Fatal(err)
		}
	}
	checkDocLinks(t, root)
}

func TestBuildCallersUseCommandPackage(t *testing.T) {
	for _, name := range []string{"Makefile", "packaging/arch/PKGBUILD", ".github/scripts/build-release-assets.sh"} {
		data, err := os.ReadFile(name)
		if os.IsNotExist(err) && name != "Makefile" {
			continue
		} // omitted from source archives
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "./cmd/xbkeeper") {
			t.Errorf("%s: missing command package build path", name)
		}
	}
}
