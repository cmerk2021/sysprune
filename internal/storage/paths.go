// Package storage implements the storage audit: read-only scanners, the
// freedesktop Trash spec, a duplicate finder, and safe deletion.
package storage

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/cmerk2021/sysprune/internal/runner"
)

// skipDirNames are project/VCS directories never scanned for personal files.
var skipDirNames = map[string]bool{
	".git": true, "node_modules": true, ".venv": true, "venv": true,
	".tox": true, "__pycache__": true, ".svn": true, ".hg": true,
	"target": false, // too generic to skip blindly
}

// SkipDir reports whether a directory should be skipped during personal scans.
func SkipDir(name string) bool { return skipDirNames[name] }

// tooShallow lists path depths we refuse to delete no matter what.
func tooShallow(p string) bool {
	p = filepath.Clean(p)
	if p == "/" || p == "." {
		return true
	}
	// Refuse anything with fewer than 3 components ("/home/user" has 2).
	return strings.Count(strings.TrimSuffix(p, "/"), "/") < 3
}

// ValidateDeletePath enforces deletion safety: the path (after resolving
// symlinks) must be inside one of the allowed roots, must not be a root
// itself or too shallow, and must never be "/" or $HOME.
func ValidateDeletePath(fsys runner.FS, path, home string, allowedRoots []string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("path %q is not absolute", path)
	}
	// Resolve the parent (the target may be a symlink we want to delete as a
	// link, not follow). Lstat the target itself.
	info, err := fsys.Lstat(path)
	if err != nil {
		return "", err
	}
	resolved := path
	if info.Mode()&fs.ModeSymlink == 0 {
		if rp, err := fsys.EvalSymlinks(path); err == nil {
			resolved = rp
		}
	} else {
		// Deleting a symlink: validate its own location, never its target.
		parent, err := fsys.EvalSymlinks(filepath.Dir(path))
		if err != nil {
			return "", err
		}
		resolved = filepath.Join(parent, filepath.Base(path))
	}
	resolved = filepath.Clean(resolved)
	if resolved == "/" || (home != "" && resolved == filepath.Clean(home)) {
		return "", fmt.Errorf("refusing to delete %q", resolved)
	}
	if tooShallow(resolved) {
		return "", fmt.Errorf("refusing to delete too-shallow path %q", resolved)
	}
	for _, root := range allowedRoots {
		root = filepath.Clean(root)
		if rr, err := fsys.EvalSymlinks(root); err == nil {
			root = filepath.Clean(rr)
		}
		if resolved == root {
			return "", fmt.Errorf("refusing to delete category root %q", resolved)
		}
		if strings.HasPrefix(resolved, root+string(filepath.Separator)) {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("path %q is outside the allowed roots for this category", resolved)
}
