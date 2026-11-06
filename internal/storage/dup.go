package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"

	"github.com/cmerk2021/sysprune/internal/runner"
)

// DupFile is one file inside a duplicate group.
type DupFile struct {
	Path string
	Size int64
	ModTimeUnix int64
	// Keeper is the suggested file to keep. Never auto-selected for deletion.
	Keeper bool
}

// DupGroup is a set of identical files.
type DupGroup struct {
	Size  int64
	Hash  string
	Files []DupFile
}

// Wasted is the reclaimable size if all non-keepers were removed.
func (g DupGroup) Wasted() int64 { return g.Size * int64(len(g.Files)-1) }

// permanenceRank scores how "permanent" a location is: Documents beats
// Downloads; shallower known dirs beat temp-ish ones.
func permanenceRank(home, path string) int {
	rel, err := filepath.Rel(home, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return 0
	}
	top := strings.SplitN(rel, string(filepath.Separator), 2)[0]
	switch top {
	case "Documents":
		return 5
	case "Pictures", "Music", "Videos":
		return 4
	case "Desktop":
		return 3
	case "Downloads":
		return 1
	}
	return 2
}

// FindDuplicates groups identical files: size → partial hash (first+last
// 64 KB) → full SHA-256. Symlinks are skipped; hardlinks to the same inode
// count as one file. Workers bounds hashing concurrency.
func FindDuplicates(ctx context.Context, fsys runner.FS, home string, roots []string, workers int, progress func(scanned int)) ([]DupGroup, error) {
	if workers < 1 {
		workers = 4
	}
	type fileRef struct {
		path string
		size int64
		mod  int64
	}
	bySize := map[int64][]fileRef{}
	seenInode := map[[2]uint64]bool{}
	scanned := 0

	for _, root := range roots {
		err := fsys.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil //nolint:nilerr // unreadable entries are skipped
			}
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			if d.IsDir() {
				if SkipDir(d.Name()) || (strings.HasPrefix(d.Name(), ".") && p != root) {
					return fs.SkipDir
				}
				return nil
			}
			if d.Type()&fs.ModeSymlink != 0 || !d.Type().IsRegular() {
				return nil
			}
			info, ierr := d.Info()
			if ierr != nil || info.Size() == 0 {
				return nil
			}
			if st, ok := info.Sys().(*syscall.Stat_t); ok {
				key := [2]uint64{uint64(st.Dev), st.Ino}
				if st.Nlink > 1 {
					if seenInode[key] {
						return nil
					}
					seenInode[key] = true
				}
			}
			bySize[info.Size()] = append(bySize[info.Size()], fileRef{p, info.Size(), info.ModTime().Unix()})
			scanned++
			if progress != nil && scanned%256 == 0 {
				progress(scanned)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	// Candidates: size buckets with >1 file.
	var jobs []fileRef
	for _, refs := range bySize {
		if len(refs) > 1 {
			jobs = append(jobs, refs...)
		}
	}
	jobPaths := make([]string, len(jobs))
	for i, j := range jobs {
		jobPaths[i] = j.path
	}

	partial := func(p string) (string, error) { return hashFile(fsys, p, true) }
	full := func(p string) (string, error) { return hashFile(fsys, p, false) }

	// Stage 1: partial hash with bounded workers.
	partialKeys := hashAll(ctx, jobPaths, workers, partial)
	byPartial := map[string][]fileRef{}
	for _, j := range jobs {
		if h, ok := partialKeys[j.path]; ok {
			k := sizeKey(j.size) + h
			byPartial[k] = append(byPartial[k], j)
		}
	}

	// Stage 2: full hash for buckets still >1.
	var fullJobs []string
	bucketOf := map[string]fileRef{}
	for _, refs := range byPartial {
		if len(refs) > 1 {
			for _, r := range refs {
				fullJobs = append(fullJobs, r.path)
				bucketOf[r.path] = r
			}
		}
	}
	fullKeys := hashAll(ctx, fullJobs, workers, full)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	byFull := map[string][]fileRef{}
	for p, h := range fullKeys {
		r := bucketOf[p]
		byFull[sizeKey(r.size)+h] = append(byFull[sizeKey(r.size)+h], r)
	}

	var groups []DupGroup
	for key, refs := range byFull {
		if len(refs) < 2 {
			continue
		}
		g := DupGroup{Size: refs[0].size, Hash: key[16:]}
		for _, r := range refs {
			g.Files = append(g.Files, DupFile{Path: r.path, Size: r.size, ModTimeUnix: r.mod})
		}
		// Suggest a keeper: most permanent location, then oldest.
		sort.SliceStable(g.Files, func(i, j int) bool {
			ri, rj := permanenceRank(home, g.Files[i].Path), permanenceRank(home, g.Files[j].Path)
			if ri != rj {
				return ri > rj
			}
			if g.Files[i].ModTimeUnix != g.Files[j].ModTimeUnix {
				return g.Files[i].ModTimeUnix < g.Files[j].ModTimeUnix
			}
			return g.Files[i].Path < g.Files[j].Path
		})
		g.Files[0].Keeper = true
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Wasted() > groups[j].Wasted() })
	return groups, nil
}

func sizeKey(size int64) string {
	b := make([]byte, 8)
	for i := 0; i < 8; i++ {
		b[i] = byte(size >> (8 * i))
	}
	return hex.EncodeToString(b)
}

func hashAll(ctx context.Context, paths []string, workers int, h func(string) (string, error)) map[string]string {
	out := make(map[string]string, len(paths))
	var mu sync.Mutex
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for _, p := range paths {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(p string) {
			defer wg.Done()
			defer func() { <-sem }()
			if hash, err := h(p); err == nil {
				mu.Lock()
				out[p] = hash
				mu.Unlock()
			}
		}(p)
	}
	wg.Wait()
	return out
}

// hashFile hashes a file fully, or just its first and last 64 KB when
// partial is true.
func hashFile(fsys runner.FS, path string, partial bool) (string, error) {
	f, err := fsys.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if !partial {
		if _, err := io.Copy(h, f); err != nil {
			return "", err
		}
		return hex.EncodeToString(h.Sum(nil)), nil
	}
	const chunk = 64 * 1024
	buf := make([]byte, chunk)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return "", err
	}
	h.Write(buf[:n])
	if seeker, ok := f.(io.Seeker); ok {
		if info, err := f.Stat(); err == nil && info.Size() > chunk {
			if _, err := seeker.Seek(-min64(chunk, info.Size()), io.SeekEnd); err == nil {
				n2, _ := io.ReadFull(f, buf)
				h.Write(buf[:n2])
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
