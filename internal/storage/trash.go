package storage

import (
	"fmt"
	"io/fs"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/cmerk2021/sysprune/internal/runner"
)

// Trash implements the freedesktop.org Trash specification for the home
// trash (~/.local/share/Trash). Cross-device moves fall back to copy+delete.
type Trash struct {
	FS   runner.FS
	Home string
	// Now is injectable for tests.
	Now func() time.Time
}

func (t *Trash) dir() string {
	dataHome := filepath.Join(t.Home, ".local", "share")
	return filepath.Join(dataHome, "Trash")
}

func (t *Trash) FilesDir() string { return filepath.Join(t.dir(), "files") }
func (t *Trash) InfoDir() string  { return filepath.Join(t.dir(), "info") }

func (t *Trash) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

// Put moves path into the Trash, returning the destination under files/.
func (t *Trash) Put(path string) (string, error) {
	if err := t.FS.MkdirAll(t.FilesDir(), 0o700); err != nil {
		return "", err
	}
	if err := t.FS.MkdirAll(t.InfoDir(), 0o700); err != nil {
		return "", err
	}
	base := filepath.Base(path)
	name := base
	// Resolve name collisions per the spec: append a counter before the
	// extension until both files/ and info/ slots are free.
	for i := 1; ; i++ {
		if !t.exists(filepath.Join(t.FilesDir(), name)) &&
			!t.exists(filepath.Join(t.InfoDir(), name+".trashinfo")) {
			break
		}
		ext := filepath.Ext(base)
		stem := strings.TrimSuffix(base, ext)
		name = fmt.Sprintf("%s.%d%s", stem, i, ext)
		if i > 10000 {
			return "", fmt.Errorf("could not find a free trash slot for %s", base)
		}
	}
	dst := filepath.Join(t.FilesDir(), name)
	infoPath := filepath.Join(t.InfoDir(), name+".trashinfo")

	// Write the .trashinfo FIRST (spec: info file is the authoritative record).
	info := fmt.Sprintf("[Trash Info]\nPath=%s\nDeletionDate=%s\n",
		encodeTrashPath(path), t.now().Format("2006-01-02T15:04:05"))
	if err := t.FS.WriteFile(infoPath, []byte(info), 0o600); err != nil {
		return "", err
	}
	if err := t.FS.Rename(path, dst); err != nil {
		// Cross-device: copy + delete.
		if cerr := t.copyAll(path, dst); cerr != nil {
			_ = t.FS.Remove(infoPath)
			return "", fmt.Errorf("trash move failed: %v (copy fallback: %v)", err, cerr)
		}
		if derr := t.FS.RemoveAll(path); derr != nil {
			return "", derr
		}
	}
	return dst, nil
}

func (t *Trash) exists(p string) bool {
	_, err := t.FS.Lstat(p)
	return err == nil
}

func (t *Trash) copyAll(src, dst string) error {
	info, err := t.FS.Lstat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		if err := t.FS.MkdirAll(dst, info.Mode().Perm()); err != nil {
			return err
		}
		entries, err := t.FS.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := t.copyAll(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		target, err := t.FS.Readlink(src)
		if err != nil {
			return err
		}
		return t.FS.Symlink(target, dst)
	}
	data, err := t.FS.ReadFile(src)
	if err != nil {
		return err
	}
	return t.FS.WriteFile(dst, data, info.Mode().Perm())
}

// encodeTrashPath percent-encodes a path per the spec (RFC 2396, like a URL
// path, keeping '/').
func encodeTrashPath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// DecodeTrashPath reverses encodeTrashPath.
func DecodeTrashPath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if d, err := url.PathUnescape(s); err == nil {
			segs[i] = d
		}
	}
	return strings.Join(segs, "/")
}

// Size returns the total size of the Trash in bytes.
func (t *Trash) Size() int64 {
	var total int64
	_ = t.FS.WalkDir(t.FilesDir(), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// Empty permanently deletes everything in the Trash and returns bytes freed.
func (t *Trash) Empty() (int64, error) {
	freed := t.Size()
	if err := t.FS.RemoveAll(t.FilesDir()); err != nil {
		return 0, err
	}
	if err := t.FS.RemoveAll(t.InfoDir()); err != nil {
		return 0, err
	}
	_ = t.FS.MkdirAll(t.FilesDir(), 0o700)
	_ = t.FS.MkdirAll(t.InfoDir(), 0o700)
	return freed, nil
}

// ParseTrashInfo extracts the original path from a .trashinfo file.
func ParseTrashInfo(data []byte) (origPath string, ok bool) {
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "Path=") {
			return DecodeTrashPath(strings.TrimPrefix(line, "Path=")), true
		}
	}
	return "", false
}
