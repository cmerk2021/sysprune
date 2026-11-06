package runner

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// MemFS is an in-memory FS implementation for tests. Paths are treated as
// absolute slash-separated paths. Parent directories are implied.
type MemFS struct {
	mu    sync.Mutex
	files map[string]*memFile // regular files and symlinks
	dirs  map[string]bool     // explicitly created dirs
}

type memFile struct {
	data    []byte
	mode    fs.FileMode
	modTime time.Time
	link    string // non-empty for symlinks
	uid     int
	gid     int
}

func NewMemFS() *MemFS {
	return &MemFS{files: map[string]*memFile{}, dirs: map[string]bool{"/": true}}
}

func clean(p string) string {
	return path.Clean("/" + strings.TrimPrefix(p, "/"))
}

// Owner returns the uid/gid recorded for a path (for test assertions).
func (m *MemFS) Owner(p string) (int, int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if f, ok := m.files[clean(p)]; ok {
		return f.uid, f.gid, true
	}
	return 0, 0, false
}

// SetOwner sets the uid/gid for a file (for setting up test fixtures).
func (m *MemFS) SetOwner(p string, uid, gid int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if f, ok := m.files[clean(p)]; ok {
		f.uid, f.gid = uid, gid
	}
}

func (m *MemFS) resolveLocked(p string, depth int) (string, error) {
	p = clean(p)
	if depth > 16 {
		return "", errors.New("too many levels of symbolic links")
	}
	if f, ok := m.files[p]; ok && f.link != "" {
		target := f.link
		if !strings.HasPrefix(target, "/") {
			target = path.Join(path.Dir(p), target)
		}
		return m.resolveLocked(target, depth+1)
	}
	return p, nil
}

func (m *MemFS) ReadFile(p string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rp, err := m.resolveLocked(p, 0)
	if err != nil {
		return nil, err
	}
	f, ok := m.files[rp]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
	}
	return append([]byte(nil), f.data...), nil
}

func (m *MemFS) WriteFile(p string, d []byte, perm fs.FileMode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p = clean(p)
	m.mkdirAllLocked(path.Dir(p))
	m.files[p] = &memFile{data: append([]byte(nil), d...), mode: perm, modTime: time.Now()}
	return nil
}

func (m *MemFS) AppendFile(p string, d []byte, perm fs.FileMode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p = clean(p)
	m.mkdirAllLocked(path.Dir(p))
	if f, ok := m.files[p]; ok {
		f.data = append(f.data, d...)
		f.modTime = time.Now()
		return nil
	}
	m.files[p] = &memFile{data: append([]byte(nil), d...), mode: perm, modTime: time.Now()}
	return nil
}

func (m *MemFS) Rename(o, n string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, n = clean(o), clean(n)
	if f, ok := m.files[o]; ok {
		m.mkdirAllLocked(path.Dir(n))
		m.files[n] = f
		delete(m.files, o)
		return nil
	}
	if m.dirs[o] {
		m.mkdirAllLocked(path.Dir(n))
		for p, f := range m.files {
			if strings.HasPrefix(p, o+"/") {
				m.files[n+strings.TrimPrefix(p, o)] = f
				delete(m.files, p)
			}
		}
		for p := range m.dirs {
			if p == o || strings.HasPrefix(p, o+"/") {
				m.dirs[n+strings.TrimPrefix(p, o)] = true
				delete(m.dirs, p)
			}
		}
		return nil
	}
	return &fs.PathError{Op: "rename", Path: o, Err: fs.ErrNotExist}
}

func (m *MemFS) Remove(p string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p = clean(p)
	if _, ok := m.files[p]; ok {
		delete(m.files, p)
		return nil
	}
	if m.dirs[p] {
		for q := range m.files {
			if strings.HasPrefix(q, p+"/") {
				return &fs.PathError{Op: "remove", Path: p, Err: errors.New("directory not empty")}
			}
		}
		delete(m.dirs, p)
		return nil
	}
	return &fs.PathError{Op: "remove", Path: p, Err: fs.ErrNotExist}
}

func (m *MemFS) RemoveAll(p string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p = clean(p)
	delete(m.files, p)
	for q := range m.files {
		if strings.HasPrefix(q, p+"/") {
			delete(m.files, q)
		}
	}
	for q := range m.dirs {
		if q == p || strings.HasPrefix(q, p+"/") {
			delete(m.dirs, q)
		}
	}
	return nil
}

func (m *MemFS) mkdirAllLocked(p string) {
	p = clean(p)
	for p != "/" {
		m.dirs[p] = true
		p = path.Dir(p)
	}
}

func (m *MemFS) MkdirAll(p string, perm fs.FileMode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mkdirAllLocked(p)
	return nil
}

type memInfo struct {
	name    string
	size    int64
	mode    fs.FileMode
	modTime time.Time
	isDir   bool
	uid     int
	gid     int
}

func (i memInfo) Name() string       { return i.name }
func (i memInfo) Size() int64        { return i.size }
func (i memInfo) Mode() fs.FileMode  { return i.mode }
func (i memInfo) ModTime() time.Time { return i.modTime }
func (i memInfo) IsDir() bool        { return i.isDir }
func (i memInfo) Sys() any {
	return &syscall.Stat_t{Uid: uint32(i.uid), Gid: uint32(i.gid)}
}

func (m *MemFS) statLocked(p string, follow bool) (fs.FileInfo, error) {
	p = clean(p)
	if follow {
		rp, err := m.resolveLocked(p, 0)
		if err != nil {
			return nil, err
		}
		p = rp
	}
	if f, ok := m.files[p]; ok {
		mode := f.mode
		if f.link != "" && !follow {
			mode |= fs.ModeSymlink
		}
		return memInfo{name: path.Base(p), size: int64(len(f.data)), mode: mode, modTime: f.modTime, uid: f.uid, gid: f.gid}, nil
	}
	if m.dirs[p] {
		return memInfo{name: path.Base(p), mode: fs.ModeDir | 0o755, modTime: time.Time{}, isDir: true}, nil
	}
	return nil, &fs.PathError{Op: "stat", Path: p, Err: fs.ErrNotExist}
}

func (m *MemFS) Stat(p string) (fs.FileInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statLocked(p, true)
}

func (m *MemFS) Lstat(p string) (fs.FileInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statLocked(p, false)
}

type memDirEntry struct{ info memInfo }

func (d memDirEntry) Name() string               { return d.info.name }
func (d memDirEntry) IsDir() bool                { return d.info.isDir }
func (d memDirEntry) Type() fs.FileMode          { return d.info.mode.Type() }
func (d memDirEntry) Info() (fs.FileInfo, error) { return d.info, nil }

func (m *MemFS) ReadDir(p string) ([]fs.DirEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p = clean(p)
	if !m.dirs[p] {
		return nil, &fs.PathError{Op: "readdir", Path: p, Err: fs.ErrNotExist}
	}
	seen := map[string]fs.DirEntry{}
	prefix := p + "/"
	if p == "/" {
		prefix = "/"
	}
	for q, f := range m.files {
		if !strings.HasPrefix(q, prefix) {
			continue
		}
		rest := strings.TrimPrefix(q, prefix)
		if i := strings.Index(rest, "/"); i >= 0 {
			continue
		}
		mode := f.mode
		if f.link != "" {
			mode |= fs.ModeSymlink
		}
		seen[rest] = memDirEntry{memInfo{name: rest, size: int64(len(f.data)), mode: mode, modTime: f.modTime}}
	}
	for q := range m.dirs {
		if !strings.HasPrefix(q, prefix) || q == p {
			continue
		}
		rest := strings.TrimPrefix(q, prefix)
		if i := strings.Index(rest, "/"); i >= 0 {
			rest = rest[:i]
		}
		if _, ok := seen[rest]; !ok {
			seen[rest] = memDirEntry{memInfo{name: rest, mode: fs.ModeDir | 0o755, isDir: true}}
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]fs.DirEntry, len(names))
	for i, n := range names {
		out[i] = seen[n]
	}
	return out, nil
}

func (m *MemFS) Symlink(old, new string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	new = clean(new)
	m.mkdirAllLocked(path.Dir(new))
	m.files[new] = &memFile{link: old, mode: 0o777, modTime: time.Now()}
	return nil
}

func (m *MemFS) Readlink(p string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if f, ok := m.files[clean(p)]; ok && f.link != "" {
		return f.link, nil
	}
	return "", &fs.PathError{Op: "readlink", Path: p, Err: errors.New("not a symlink")}
}

func (m *MemFS) EvalSymlinks(p string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rp, err := m.resolveLocked(p, 0)
	if err != nil {
		return "", err
	}
	if _, ok := m.files[rp]; !ok && !m.dirs[rp] {
		return "", &fs.PathError{Op: "lstat", Path: p, Err: fs.ErrNotExist}
	}
	return rp, nil
}

func (m *MemFS) Chown(p string, uid, gid int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if f, ok := m.files[clean(p)]; ok {
		f.uid, f.gid = uid, gid
		return nil
	}
	if m.dirs[clean(p)] {
		return nil
	}
	return &fs.PathError{Op: "chown", Path: p, Err: fs.ErrNotExist}
}

func (m *MemFS) Chmod(p string, mode fs.FileMode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if f, ok := m.files[clean(p)]; ok {
		f.mode = mode
		return nil
	}
	if m.dirs[clean(p)] {
		return nil
	}
	return &fs.PathError{Op: "chmod", Path: p, Err: fs.ErrNotExist}
}

func (m *MemFS) WalkDir(root string, fn fs.WalkDirFunc) error {
	root = clean(root)
	info, err := m.Lstat(root)
	if err != nil {
		return fn(root, nil, err)
	}
	de := memDirEntry{memInfo{name: path.Base(root), mode: info.Mode(), isDir: info.IsDir(), size: info.Size(), modTime: info.ModTime()}}
	if err := fn(root, de, nil); err != nil {
		if errors.Is(err, fs.SkipDir) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return nil
	}
	entries, err := m.ReadDir(root)
	if err != nil {
		return fn(root, de, err)
	}
	for _, e := range entries {
		child := path.Join(root, e.Name())
		if e.IsDir() {
			if err := m.WalkDir(child, fn); err != nil {
				return err
			}
		} else {
			if err := fn(child, e, nil); err != nil {
				if errors.Is(err, fs.SkipDir) {
					return nil
				}
				return err
			}
		}
	}
	return nil
}

type memOpenFile struct {
	info memInfo
	r    *bytes.Reader
}

func (f *memOpenFile) Stat() (fs.FileInfo, error) { return f.info, nil }
func (f *memOpenFile) Read(p []byte) (int, error) { return f.r.Read(p) }
func (f *memOpenFile) Close() error               { return nil }
func (f *memOpenFile) Seek(offset int64, whence int) (int64, error) {
	return f.r.Seek(offset, whence)
}

var _ io.Seeker = (*memOpenFile)(nil)

func (m *MemFS) Open(p string) (fs.File, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rp, err := m.resolveLocked(p, 0)
	if err != nil {
		return nil, err
	}
	f, ok := m.files[rp]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
	}
	return &memOpenFile{
		info: memInfo{name: path.Base(rp), size: int64(len(f.data)), mode: f.mode, modTime: f.modTime},
		r:    bytes.NewReader(f.data),
	}, nil
}

// SetModTime adjusts a file's modification time (for age-based test fixtures).
func (m *MemFS) SetModTime(p string, t time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if f, ok := m.files[clean(p)]; ok {
		f.modTime = t
	}
}
