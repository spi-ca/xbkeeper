//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"
)

const manifestName = "SHA256SUMS"
const maxManifest = 16 << 20
const maxEntries = 100000

// No filename escaping is used: every accepted path is also an unambiguous
// GNU sha256sum filename and a safe JSON string.
func validRelative(n string) bool {
	if n == "" || strings.HasPrefix(n, "/") || strings.ContainsAny(n, "\\\r\n\x00") || !utf8.ValidString(n) || path.Clean(n) != n {
		return false
	}
	for _, part := range strings.Split(n, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}
func safeEntry(fi fs.FileInfo, dir bool) bool {
	uid, ok := owner(fi)
	if !ok || int(uid) != os.Geteuid() || fi.Mode().Perm()&0077 != 0 {
		return false
	}
	if dir {
		return fi.IsDir()
	}
	return fi.Mode().IsRegular() && fi.Sys().(*syscall.Stat_t).Nlink == 1
}
func sameSnapshot(a, b fs.FileInfo) bool {
	if !os.SameFile(a, b) || a.Size() != b.Size() || a.Mode() != b.Mode() {
		return false
	}
	x, y := a.Sys().(*syscall.Stat_t), b.Sys().(*syscall.Stat_t)
	return x.Mtim == y.Mtim && x.Ctim == y.Ctim && x.Nlink == y.Nlink
}
func checkedFile(r *os.Root, name string) (*os.File, fs.FileInfo, error) {
	before, err := r.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if !safeEntry(before, false) {
		return nil, nil, errors.New("unsafe file")
	}
	f, err := r.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	opened, err := f.Stat()
	if err != nil || !safeEntry(opened, false) || !sameSnapshot(before, opened) {
		f.Close()
		return nil, nil, errors.New("file changed or unsafe")
	}
	return f, opened, nil
}
func hashFile(ctx context.Context, r *os.Root, name string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	f, before, err := checkedFile(r, name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, e := f.Read(buf)
		if n > 0 {
			_, _ = h.Write(buf[:n])
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return "", e
		}
		if n == 0 {
			return "", io.ErrNoProgress
		}
	}
	after, e1 := f.Stat()
	entry, e2 := r.Lstat(name)
	if e1 != nil || e2 != nil || !safeEntry(after, false) || !sameSnapshot(before, after) || !sameSnapshot(before, entry) {
		return "", errors.New("file changed during hashing")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Walk using opened directory descriptors, checking directory identity before
// and after traversal. Every file is checked again at the hash/open boundary.
func listFiles(ctx context.Context, r *os.Root) ([]string, error) {
	var files []string
	count := 0
	var walk func(*os.Root, string) error
	walk = func(cur *os.Root, prefix string) error {
		if strings.Count(prefix, "/") > 256 {
			return errors.New("backup directory depth exceeded")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		d, err := cur.Open(".")
		if err != nil {
			return err
		}
		var names []string
		for {
			if err := ctx.Err(); err != nil {
				d.Close()
				return err
			}
			batch, readErr := d.Readdirnames(256)
			names = append(names, batch...)
			count += len(batch)
			if count > maxEntries+1 {
				d.Close()
				return errors.New("too many backup entries")
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				d.Close()
				return readErr
			}
		}
		d.Close()
		sort.Strings(names)
		for _, n := range names {
			if err := ctx.Err(); err != nil {
				return err
			}
			full := prefix + n
			if !validRelative(full) {
				return errors.New("unsupported backup filename")
			}
			fi, err := cur.Lstat(n)
			if err != nil {
				return err
			}
			if fi.IsDir() {
				if !safeEntry(fi, true) {
					return errors.New("unsafe backup directory")
				}
				sub, err := cur.OpenRoot(n)
				if err != nil {
					return err
				}
				opened, err := sub.Stat(".")
				if err != nil || !sameSnapshot(fi, opened) {
					sub.Close()
					return errors.New("backup directory changed")
				}
				err = walk(sub, full+"/")
				after, e := cur.Lstat(n)
				sub.Close()
				if err != nil {
					return err
				}
				if e != nil || !sameSnapshot(fi, after) {
					return errors.New("backup directory changed")
				}
			} else {
				if !safeEntry(fi, false) {
					return errors.New("unsafe backup file")
				}
				files = append(files, full)
			}
		}
		return nil
	}
	if err := walk(r, ""); err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}
func readControl(ctx context.Context, r *os.Root, name string, limit int64) ([]byte, error) {
	f, before, err := checkedFile(r, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if before.Size() > limit {
		return nil, errors.New("control file too large")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	after, e1 := f.Stat()
	entry, e2 := r.Lstat(name)
	if e1 != nil || e2 != nil || !sameSnapshot(before, after) || !sameSnapshot(before, entry) || int64(len(data)) > limit {
		return nil, errors.New("control file changed or too large")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}
func parseManifest(ctx context.Context, r *os.Root) (map[string]string, error) {
	f, before, err := checkedFile(r, manifestName)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if before.Size() > maxManifest {
		return nil, errors.New("manifest too large")
	}
	var body bytes.Buffer
	block := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, e := f.Read(block)
		if n > 0 {
			body.Write(block[:n])
			if body.Len() > maxManifest {
				return nil, errors.New("manifest too large")
			}
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		if n == 0 {
			return nil, io.ErrNoProgress
		}
	}
	data := body.Bytes()
	after, e1 := f.Stat()
	entry, e2 := r.Lstat(manifestName)
	if e1 != nil || e2 != nil || !sameSnapshot(before, after) || !sameSnapshot(before, entry) {
		return nil, errors.New("manifest changed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(data) > maxManifest || len(data) == 0 || data[len(data)-1] != '\n' {
		return nil, errors.New("invalid manifest size or termination")
	}
	result := make(map[string]string)
	prev := ""
	// Consume one line at a time: splitting a corrupted all-newline manifest
	// would allocate a slice for every byte before the entry limit is checked.
	for remaining := data; len(remaining) > 0; {
		line, rest, _ := bytes.Cut(remaining, []byte{'\n'})
		remaining = rest
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(result) >= maxEntries || len(line) < 67 || line[64] != ' ' || line[65] != ' ' {
			return nil, errors.New("invalid manifest line or entry limit")
		}
		digest := string(line[:64])
		name := string(line[66:])
		if !validRelative(name) || name == manifestName || (prev != "" && name <= prev) {
			return nil, errors.New("invalid manifest path or ordering")
		}
		for _, c := range digest {
			if c < '0' || c > '9' && c < 'a' || c > 'f' {
				return nil, errors.New("invalid manifest digest")
			}
		}
		result[name] = digest
		prev = name
	}
	return result, nil
}
func generateManifest(ctx context.Context, r *os.Root) ([]byte, error) {
	files, err := listFiles(ctx, r)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	for _, name := range files {
		if name == manifestName {
			return nil, errors.New("manifest already exists")
		}
		digest, err := hashFile(ctx, r, name)
		if err != nil {
			return nil, err
		}
		if _, err = fmt.Fprintf(&b, "%s  %s\n", digest, name); err != nil {
			return nil, err
		}
		if b.Len() > maxManifest {
			return nil, errors.New("manifest too large")
		}
	}
	if len(files) == 0 {
		return nil, errors.New("empty backup")
	}
	return b.Bytes(), nil
}
func structuralManifest(ctx context.Context, r *os.Root) error {
	files, err := listFiles(ctx, r)
	if err != nil {
		return err
	}
	expected, err := parseManifest(ctx, r)
	if err != nil {
		return err
	}
	if len(files) != len(expected)+1 {
		return errors.New("manifest file set differs")
	}
	for _, n := range files {
		if n != manifestName {
			if _, ok := expected[n]; !ok {
				return errors.New("manifest file set differs")
			}
		}
	}
	return nil
}
