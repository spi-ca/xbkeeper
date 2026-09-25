//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
)

func pathInfo(path string) (fs.FileInfo, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("path must be absolute and clean: %q", path)
	}
	cur := "/"
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for _, p := range parts {
		if p == "" {
			continue
		}
		cur = filepath.Join(cur, p)
		fi, err := os.Lstat(cur)
		if err != nil {
			return nil, fmt.Errorf("path %q: %w", cur, err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("symlink path: %q", cur)
		}
		if cur != path && !fi.IsDir() {
			return nil, fmt.Errorf("non-directory parent: %q", cur)
		}
	}
	return os.Lstat(path)
}
func owner(fi fs.FileInfo) (uint32, bool) {
	s, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return s.Uid, true
}
func privateFile(path string) error {
	fi, err := pathInfo(path)
	if err != nil {
		return err
	}
	uid, ok := owner(fi)
	if !ok || int(uid) != os.Geteuid() || !fi.Mode().IsRegular() || fi.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("file must be private, regular and owned by current uid: %q", path)
	}
	return controlledParents(path)
}
func controlledParents(path string) error {
	for p := filepath.Dir(path); ; p = filepath.Dir(p) {
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		uid, ok := owner(fi)
		// A root-owned sticky /tmp is permitted for unprivileged temporary fixtures.
		sticky := fi.Mode()&os.ModeSticky != 0 && uid == 0
		if !ok || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 || (int(uid) != os.Geteuid() && uid != 0) || (fi.Mode().Perm()&0022 != 0 && !sticky) {
			return fmt.Errorf("unsafe parent directory: %q", p)
		}
		if p == "/" {
			break
		}
	}
	return nil
}
func directory(path string, private bool) error {
	fi, err := pathInfo(path)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("not a directory: %q", path)
	}
	if private {
		uid, ok := owner(fi)
		if !ok || int(uid) != os.Geteuid() || fi.Mode().Perm() != 0700 {
			return fmt.Errorf("backup directory must be owned by current uid and mode 0700: %q", path)
		}
		return controlledParents(path)
	}
	return nil
}
func overlaps(a, b string) bool {
	return a == b || a == "/" || b == "/" || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}
func loadConfig(path string) (config, error) {
	var c config
	if err := privateFile(path); err != nil {
		return c, fmt.Errorf("config: %w", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if len(b) > 64*1024 {
		return c, errors.New("config too large")
	}
	if err := strictJSON(b, &c); err != nil {
		return c, fmt.Errorf("config JSON: %w", err)
	}
	if c.Xtrabackup == "" {
		c.Xtrabackup = "/usr/bin/xtrabackup"
	}
	if c.Keep < 1 || c.MinFreeBytes < 0 {
		return c, errors.New("keep must be >= 1 and min_free_bytes >= 0")
	}
	if err := directory(c.BackupDir, true); err != nil {
		return c, err
	}
	if err := cleanAbsolute(c.Datadir); err != nil {
		return c, err
	}
	if overlaps(c.BackupDir, c.Datadir) {
		return c, errors.New("backup_dir and datadir must not overlap")
	}
	for _, p := range []string{c.DefaultsFile, c.Socket, c.Xtrabackup} {
		if err := cleanAbsolute(p); err != nil {
			return c, err
		}
	}
	return c, nil
}
func cleanAbsolute(p string) error {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return errors.New("expected clean absolute path")
	}
	return nil
}
func validateRuntime(c config) error {
	if err := directory(c.Datadir, false); err != nil {
		return fmt.Errorf("datadir unavailable: %w", err)
	}
	if err := privateFile(c.DefaultsFile); err != nil {
		return fmt.Errorf("defaults file unavailable: %w", err)
	}
	fi, err := pathInfo(c.Socket)
	if err != nil {
		return fmt.Errorf("socket unavailable: %w", err)
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return errors.New("socket unavailable: not a Unix socket")
	}
	fi, err = pathInfo(c.Xtrabackup)
	if err != nil {
		return fmt.Errorf("xtrabackup binary unavailable: %w", err)
	}
	uid, ok := owner(fi)
	if !ok || (uid != 0 && int(uid) != os.Geteuid()) || !fi.Mode().IsRegular() || fi.Mode().Perm()&0111 == 0 || fi.Mode().Perm()&0022 != 0 {
		return errors.New("xtrabackup binary unavailable: untrusted executable")
	}
	if err := controlledParents(c.Xtrabackup); err != nil {
		return fmt.Errorf("xtrabackup binary unavailable: %w", err)
	}
	return nil
}

// strictJSON checks duplicate keys before decoding; unknown fields and trailing data are rejected.
func strictJSON(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	t, err := dec.Token()
	if err != nil {
		return err
	}
	if t != json.Delim('{') {
		return errors.New("expected object")
	}
	typ := reflect.TypeOf(v)
	if typ.Kind() != reflect.Pointer || typ.Elem().Kind() != reflect.Struct {
		return errors.New("expected struct destination")
	}
	allowed := make(map[string]bool)
	for i := 0; i < typ.Elem().NumField(); i++ {
		field := typ.Elem().Field(i)
		if field.IsExported() {
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name != "" && name != "-" {
				allowed[name] = true
			}
		}
	}
	seen := map[string]bool{}
	for dec.More() {
		t, err = dec.Token()
		if err != nil {
			return err
		}
		key := t.(string)
		if !allowed[key] {
			return errors.New("unknown or incorrectly cased JSON field")
		}
		if seen[key] {
			return errors.New("duplicate JSON field")
		}
		seen[key] = true
		var raw json.RawMessage
		if err = dec.Decode(&raw); err != nil {
			return err
		}
	}
	if _, err = dec.Token(); err != nil {
		return err
	}
	dec = json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err = dec.Decode(v); err != nil {
		return err
	}
	if err = dec.Decode(new(any)); err != io.EOF {
		return errors.New("trailing JSON data")
	}
	return nil
}
