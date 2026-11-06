package storage

import (
	"fmt"
	"path/filepath"

	"github.com/cmerk2021/sysprune/internal/journal"
	"github.com/cmerk2021/sysprune/internal/runner"
)

// Cleaner performs user-scope storage actions with journaling. All paths are
// validated against allowed roots before anything is removed.
type Cleaner struct {
	FS    runner.FS
	J     *journal.Writer
	Trash *Trash
	Home  string
}

// AllowedRoots returns the permitted deletion roots for a category.
func (c *Cleaner) AllowedRoots(cat Category, personalRoots []string) []string {
	switch cat {
	case CatUserCache:
		return []string{filepath.Join(c.Home, ".cache")}
	case CatThumbnails:
		return []string{filepath.Join(c.Home, ".cache", "thumbnails")}
	case CatToolchain:
		return []string{
			filepath.Join(c.Home, ".cache"), filepath.Join(c.Home, ".npm"),
			filepath.Join(c.Home, ".cargo", "registry"),
			filepath.Join(c.Home, ".local", "share", "pnpm"),
			filepath.Join(c.Home, "go", "pkg", "mod", "cache"),
		}
	case CatTmp:
		return []string{"/tmp", "/var/tmp"}
	case CatLargeFiles, CatDuplicates, CatOldDownloads, CatEmptyBroken:
		return personalRoots
	}
	return nil
}

// MoveToTrash trashes a personal file (default action), journaled.
func (c *Cleaner) MoveToTrash(f Finding, personalRoots []string) error {
	resolved, err := ValidateDeletePath(c.FS, f.Path, c.Home, c.AllowedRoots(f.Category, personalRoots))
	if err != nil {
		return err
	}
	op := journal.Operation{
		ID: "storage/" + string(f.Category) + "/" + resolved, Kind: "trash",
		Scope: "user", Op: "trash", SrcPath: resolved, Bytes: f.Size,
	}
	if err := c.J.Intent(op); err != nil {
		return fmt.Errorf("journal write failed; refusing: %w", err)
	}
	dst, err := c.Trash.Put(resolved)
	if err != nil {
		_ = c.J.Failed(op, err)
		return err
	}
	op.DstPath = dst
	_ = c.J.Done(op)
	return nil
}

// DeletePermanently removes a path forever. Only for regenerable categories
// or an explicit user choice; the caller is responsible for the confirmation
// UI showing the exact path and size.
func (c *Cleaner) DeletePermanently(f Finding, personalRoots []string, userConfirmed bool) error {
	if !f.Category.Regenerable() && !userConfirmed {
		return fmt.Errorf("category %s requires explicit confirmation for permanent deletion", f.Category)
	}
	resolved, err := ValidateDeletePath(c.FS, f.Path, c.Home, c.AllowedRoots(f.Category, personalRoots))
	if err != nil {
		return err
	}
	op := journal.Operation{
		ID: "storage/" + string(f.Category) + "/" + resolved, Kind: "delete",
		Scope: "user", Op: "delete", SrcPath: resolved, Bytes: f.Size,
	}
	if err := c.J.Intent(op); err != nil {
		return fmt.Errorf("journal write failed; refusing: %w", err)
	}
	if err := c.FS.RemoveAll(resolved); err != nil {
		_ = c.J.Failed(op, err)
		return err
	}
	_ = c.J.Done(op)
	return nil
}
