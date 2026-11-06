package storage

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cmerk2021/sysprune/internal/runner"
)

// Category identifies a storage-audit category.
type Category string

const (
	CatUserCache     Category = "user-cache"
	CatThumbnails    Category = "thumbnails"
	CatTmp           Category = "tmp"
	CatPkgCache      Category = "pkg-cache"
	CatJournalLogs   Category = "journal-logs"
	CatCrashDumps    Category = "crash-dumps"
	CatToolchain     Category = "toolchain-cache"
	CatContainers    Category = "containers"
	CatLargeFiles    Category = "large-files"
	CatDuplicates    Category = "duplicates"
	CatOldDownloads  Category = "old-downloads"
	CatEmptyBroken   Category = "empty-broken"
	CatTrashEmpty    Category = "empty-trash"
)

// Regenerable reports whether permanent deletion is acceptable by default.
func (c Category) Regenerable() bool {
	switch c {
	case CatUserCache, CatThumbnails, CatTmp, CatPkgCache, CatJournalLogs,
		CatCrashDumps, CatToolchain, CatContainers, CatTrashEmpty:
		return true
	}
	return false
}

// Finding is one reclaimable item.
type Finding struct {
	Category Category
	Path     string
	Size     int64
	Note     string
	// SystemOp maps to a privileged clean operation when direct deletion
	// isn't how this finding is handled (e.g. "vacuum-journal").
	SystemOp string
	Param    string
}

// Scanner performs all read-only storage scans.
type Scanner struct {
	FS   runner.FS
	R    runner.Runner
	Home string
	Now  func() time.Time
}

func (s *Scanner) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// DirSize sums a directory tree (best effort, cancellable).
func (s *Scanner) DirSize(ctx context.Context, root string) int64 {
	var total int64
	_ = s.FS.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil || d == nil || d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// ScanUserCache breaks down ~/.cache per app.
func (s *Scanner) ScanUserCache(ctx context.Context) []Finding {
	cache := filepath.Join(s.Home, ".cache")
	entries, err := s.FS.ReadDir(cache)
	if err != nil {
		return nil
	}
	var out []Finding
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return out
		}
		p := filepath.Join(cache, e.Name())
		var size int64
		if e.IsDir() {
			size = s.DirSize(ctx, p)
		} else if info, err := e.Info(); err == nil {
			size = info.Size()
		}
		if size == 0 {
			continue
		}
		note := "Application cache — regenerated automatically when needed."
		if e.Name() == "thumbnails" {
			continue // reported separately
		}
		out = append(out, Finding{Category: CatUserCache, Path: p, Size: size, Note: note})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Size > out[j].Size })
	return out
}

// ScanThumbnails reports the thumbnail cache.
func (s *Scanner) ScanThumbnails(ctx context.Context) []Finding {
	p := filepath.Join(s.Home, ".cache", "thumbnails")
	size := s.DirSize(ctx, p)
	if size == 0 {
		return nil
	}
	return []Finding{{Category: CatThumbnails, Path: p, Size: size,
		Note: "Image/video thumbnails — regenerated as you browse folders."}}
}

// ScanTmp finds user-owned files older than ageDays in /tmp and /var/tmp.
// Ownership and in-use checks are approximated by only listing files whose
// modification time is old enough and that are regular files; in user scope
// we can only delete what we own anyway.
func (s *Scanner) ScanTmp(ctx context.Context, ageDays int) []Finding {
	cutoff := s.now().AddDate(0, 0, -ageDays)
	var out []Finding
	for _, root := range []string{"/tmp", "/var/tmp"} {
		entries, err := s.FS.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if err := ctx.Err(); err != nil {
				return out
			}
			info, err := e.Info()
			if err != nil || info.ModTime().After(cutoff) {
				continue
			}
			p := filepath.Join(root, e.Name())
			size := info.Size()
			if e.IsDir() {
				size = s.DirSize(ctx, p)
			}
			if size == 0 {
				continue
			}
			out = append(out, Finding{Category: CatTmp, Path: p, Size: size,
				Note: fmt.Sprintf("Temp file untouched for over %d days.", ageDays)})
		}
	}
	return out
}

// ScanJournalLogs reports disk usage of the systemd journal.
func (s *Scanner) ScanJournalLogs(ctx context.Context) []Finding {
	out, _, code, err := s.R.Run(ctx, "journalctl", "--disk-usage")
	if err != nil || code != 0 {
		return nil
	}
	// "Archived and active journals take up 1.5G in the file system."
	text := string(out)
	size := parseHumanSize(text)
	if size < 256<<20 { // only suggest when >256 MB
		return nil
	}
	return []Finding{{Category: CatJournalLogs, Path: "/var/log/journal", Size: size,
		Note:     "System logs. Vacuuming keeps recent logs and removes old ones.",
		SystemOp: "vacuum-journal", Param: "200M"}}
}

// ScanCrashDumps reports /var/crash contents.
func (s *Scanner) ScanCrashDumps(ctx context.Context) []Finding {
	size := s.DirSize(ctx, "/var/crash")
	if size == 0 {
		return nil
	}
	return []Finding{{Category: CatCrashDumps, Path: "/var/crash", Size: size,
		Note: "Old crash reports.", SystemOp: "crash-clean"}}
}

// ScanPkgCaches detects package-manager caches and maps them to privileged
// clean operations.
func (s *Scanner) ScanPkgCaches(ctx context.Context) []Finding {
	var out []Finding
	add := func(path, note, op string) {
		if size := s.DirSize(ctx, path); size > 0 {
			out = append(out, Finding{Category: CatPkgCache, Path: path, Size: size, Note: note, SystemOp: op})
		}
	}
	add("/var/cache/apt/archives", "Downloaded .deb packages — safe to clean.", "apt-clean")
	add("/var/cache/dnf", "DNF metadata and package cache.", "dnf-clean")
	add("/var/cache/pacman/pkg", "Downloaded pacman packages.", "pacman-clean")
	if _, err := s.FS.Stat("/var/lib/flatpak"); err == nil {
		out = append(out, Finding{Category: CatPkgCache, Path: "/var/lib/flatpak",
			Size: 0, Note: "Flatpak: remove unused runtimes (size computed by flatpak).",
			SystemOp: "flatpak-unused"})
	}
	return out
}

// ScanContainers suggests docker prune when docker has data.
func (s *Scanner) ScanContainers(ctx context.Context) []Finding {
	if _, err := s.FS.Stat("/var/lib/docker"); err != nil {
		return nil
	}
	return []Finding{{Category: CatContainers, Path: "/var/lib/docker", Size: 0,
		Note:     "Docker dangling images and build cache (uses 'docker system prune').",
		SystemOp: "docker-prune"}}
}

// ToolchainCaches lists language toolchain cache dirs under $HOME.
func (s *Scanner) ScanToolchainCaches(ctx context.Context) []Finding {
	dirs := []struct{ rel, note string }{
		{".cache/pip", "pip download cache."},
		{".npm/_cacache", "npm cache."},
		{".cache/yarn", "yarn cache."},
		{".local/share/pnpm/store", "pnpm store."},
		{".cargo/registry", "Cargo registry cache (re-downloaded on demand)."},
		{".cache/go-build", "Go build cache."},
		{"go/pkg/mod/cache/download", "Go module download cache."},
	}
	var out []Finding
	for _, d := range dirs {
		p := filepath.Join(s.Home, d.rel)
		if size := s.DirSize(ctx, p); size > 0 {
			out = append(out, Finding{Category: CatToolchain, Path: p, Size: size, Note: d.note})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Size > out[j].Size })
	return out
}

// ScanLargeFiles finds files above thresholdBytes within roots.
func (s *Scanner) ScanLargeFiles(ctx context.Context, roots []string, thresholdBytes int64) []Finding {
	var out []Finding
	for _, root := range roots {
		_ = s.FS.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			if err != nil || d == nil {
				return nil
			}
			if d.IsDir() {
				if SkipDir(d.Name()) || (strings.HasPrefix(d.Name(), ".") && p != root) {
					return fs.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() {
				return nil
			}
			if info, err := d.Info(); err == nil && info.Size() >= thresholdBytes {
				out = append(out, Finding{Category: CatLargeFiles, Path: p, Size: info.Size(),
					Note: "Large file — review before removing."})
			}
			return nil
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Size > out[j].Size })
	return out
}

// ScanOldDownloads finds files in Downloads older than ageDays.
func (s *Scanner) ScanOldDownloads(ctx context.Context, downloads string, ageDays int) []Finding {
	cutoff := s.now().AddDate(0, 0, -ageDays)
	var out []Finding
	_ = s.FS.WalkDir(downloads, func(p string, d fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil || d == nil || d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		if info, err := d.Info(); err == nil && info.ModTime().Before(cutoff) {
			out = append(out, Finding{Category: CatOldDownloads, Path: p, Size: info.Size(),
				Note: fmt.Sprintf("In Downloads and untouched for over %d days.", ageDays)})
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Size > out[j].Size })
	return out
}

// ScanEmptyAndBroken finds empty files, empty dirs, and broken symlinks.
func (s *Scanner) ScanEmptyAndBroken(ctx context.Context, roots []string) []Finding {
	var out []Finding
	for _, root := range roots {
		_ = s.FS.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			if err != nil || d == nil || p == root {
				return nil
			}
			if d.IsDir() {
				if SkipDir(d.Name()) || strings.HasPrefix(d.Name(), ".") {
					return fs.SkipDir
				}
				if entries, err := s.FS.ReadDir(p); err == nil && len(entries) == 0 {
					out = append(out, Finding{Category: CatEmptyBroken, Path: p, Note: "Empty directory."})
					return fs.SkipDir
				}
				return nil
			}
			if d.Type()&fs.ModeSymlink != 0 {
				if _, err := s.FS.Stat(p); err != nil {
					out = append(out, Finding{Category: CatEmptyBroken, Path: p, Note: "Broken symlink."})
				}
				return nil
			}
			if info, err := d.Info(); err == nil && info.Size() == 0 && d.Type().IsRegular() {
				out = append(out, Finding{Category: CatEmptyBroken, Path: p, Note: "Empty file."})
			}
			return nil
		})
	}
	return out
}

// DefaultPersonalRoots suggests folders via XDG user dirs with fallbacks.
func DefaultPersonalRoots(fsys runner.FS, home string) []string {
	defaults := map[string]string{
		"XDG_DOWNLOAD_DIR":  "Downloads",
		"XDG_DOCUMENTS_DIR": "Documents",
		"XDG_PICTURES_DIR":  "Pictures",
		"XDG_VIDEOS_DIR":    "Videos",
		"XDG_MUSIC_DIR":     "Music",
		"XDG_DESKTOP_DIR":   "Desktop",
	}
	resolved := map[string]string{}
	if data, err := fsys.ReadFile(filepath.Join(home, ".config", "user-dirs.dirs")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			for key := range defaults {
				prefix := key + "=\""
				if strings.HasPrefix(line, prefix) {
					val := strings.TrimSuffix(strings.TrimPrefix(line, prefix), "\"")
					val = strings.ReplaceAll(val, "$HOME", home)
					resolved[key] = val
				}
			}
		}
	}
	var roots []string
	keys := []string{"XDG_DOWNLOAD_DIR", "XDG_DOCUMENTS_DIR", "XDG_PICTURES_DIR", "XDG_VIDEOS_DIR", "XDG_MUSIC_DIR", "XDG_DESKTOP_DIR"}
	for _, key := range keys {
		p := resolved[key]
		if p == "" {
			p = filepath.Join(home, defaults[key])
		}
		if info, err := fsys.Stat(p); err == nil && info.IsDir() && filepath.Clean(p) != filepath.Clean(home) {
			roots = append(roots, p)
		}
	}
	return roots
}

// parseHumanSize extracts "1.5G" style sizes from text.
func parseHumanSize(text string) int64 {
	fields := strings.Fields(text)
	for _, f := range fields {
		var v float64
		var unit string
		if n, _ := fmt.Sscanf(f, "%f%s", &v, &unit); n == 2 {
			unit = strings.TrimSuffix(strings.TrimSuffix(unit, "."), "iB")
			unit = strings.TrimSuffix(unit, "B")
			switch unit {
			case "K":
				return int64(v * (1 << 10))
			case "M":
				return int64(v * (1 << 20))
			case "G":
				return int64(v * (1 << 30))
			case "T":
				return int64(v * (1 << 40))
			}
		}
	}
	return 0
}

// HumanSize formats bytes for display.
func HumanSize(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(b)/(1<<10))
	}
	return fmt.Sprintf("%d B", b)
}
