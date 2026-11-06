package runner

import (
	"io/fs"
	"os"
	"path/filepath"
)

// FS is the filesystem boundary used by all packages that touch disk.
type FS interface {
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte, perm fs.FileMode) error
	AppendFile(path string, data []byte, perm fs.FileMode) error
	Rename(oldpath, newpath string) error
	Remove(path string) error
	RemoveAll(path string) error
	MkdirAll(path string, perm fs.FileMode) error
	Stat(path string) (fs.FileInfo, error)
	Lstat(path string) (fs.FileInfo, error)
	ReadDir(path string) ([]fs.DirEntry, error)
	Symlink(oldname, newname string) error
	Readlink(path string) (string, error)
	EvalSymlinks(path string) (string, error)
	Chown(path string, uid, gid int) error
	Chmod(path string, mode fs.FileMode) error
	WalkDir(root string, fn fs.WalkDirFunc) error
	Open(path string) (fs.File, error)
}

// OSFS is the production FS backed by the os package.
type OSFS struct{}

func (OSFS) ReadFile(p string) ([]byte, error) { return os.ReadFile(p) }
func (OSFS) WriteFile(p string, d []byte, m fs.FileMode) error {
	return os.WriteFile(p, d, m)
}
func (OSFS) AppendFile(p string, d []byte, m fs.FileMode) error {
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, m)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(d); err != nil {
		return err
	}
	return f.Sync()
}
func (OSFS) Rename(o, n string) error                     { return os.Rename(o, n) }
func (OSFS) Remove(p string) error                        { return os.Remove(p) }
func (OSFS) RemoveAll(p string) error                     { return os.RemoveAll(p) }
func (OSFS) MkdirAll(p string, m fs.FileMode) error       { return os.MkdirAll(p, m) }
func (OSFS) Stat(p string) (fs.FileInfo, error)           { return os.Stat(p) }
func (OSFS) Lstat(p string) (fs.FileInfo, error)          { return os.Lstat(p) }
func (OSFS) ReadDir(p string) ([]fs.DirEntry, error)      { return os.ReadDir(p) }
func (OSFS) Symlink(o, n string) error                    { return os.Symlink(o, n) }
func (OSFS) Readlink(p string) (string, error)            { return os.Readlink(p) }
func (OSFS) EvalSymlinks(p string) (string, error)        { return filepath.EvalSymlinks(p) }
func (OSFS) Chown(p string, uid, gid int) error           { return os.Chown(p, uid, gid) }
func (OSFS) Chmod(p string, m fs.FileMode) error          { return os.Chmod(p, m) }
func (OSFS) WalkDir(root string, fn fs.WalkDirFunc) error { return filepath.WalkDir(root, fn) }
func (OSFS) Open(p string) (fs.File, error)               { return os.Open(p) }
