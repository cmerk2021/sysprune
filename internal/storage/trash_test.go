package storage

import (
	"strings"
	"testing"
	"time"

	"github.com/cmerk2021/sysprune/internal/journal"
	"github.com/cmerk2021/sysprune/internal/runner"
)

func newTestTrash(mem *runner.MemFS) *Trash {
	return &Trash{FS: mem, Home: "/home/u", Now: func() time.Time {
		return time.Date(2024, 6, 1, 12, 30, 0, 0, time.UTC)
	}}
}

func TestTrashSpec(t *testing.T) {
	mem := runner.NewMemFS()
	_ = mem.MkdirAll("/home/u/Downloads", 0o755)
	_ = mem.WriteFile("/home/u/Downloads/big file.iso", []byte("data"), 0o644)
	tr := newTestTrash(mem)

	dst, err := tr.Put("/home/u/Downloads/big file.iso")
	if err != nil {
		t.Fatal(err)
	}
	if dst != "/home/u/.local/share/Trash/files/big file.iso" {
		t.Errorf("dst = %q", dst)
	}
	// Original gone, file in Trash.
	if _, err := mem.Lstat("/home/u/Downloads/big file.iso"); err == nil {
		t.Error("original still exists")
	}
	data, err := mem.ReadFile(dst)
	if err != nil || string(data) != "data" {
		t.Errorf("trashed content: %q, %v", data, err)
	}
	// .trashinfo with URL-encoded path and deletion date.
	info, err := mem.ReadFile("/home/u/.local/share/Trash/info/big file.iso.trashinfo")
	if err != nil {
		t.Fatal(err)
	}
	s := string(info)
	if !strings.HasPrefix(s, "[Trash Info]\n") {
		t.Errorf("trashinfo header: %q", s)
	}
	if !strings.Contains(s, "Path=/home/u/Downloads/big%20file.iso") {
		t.Errorf("Path not URL-encoded: %q", s)
	}
	if !strings.Contains(s, "DeletionDate=2024-06-01T12:30:00") {
		t.Errorf("DeletionDate: %q", s)
	}
	// Round-trip decode.
	orig, ok := ParseTrashInfo(info)
	if !ok || orig != "/home/u/Downloads/big file.iso" {
		t.Errorf("ParseTrashInfo = %q, %v", orig, ok)
	}
}

func TestTrashNameCollisions(t *testing.T) {
	mem := runner.NewMemFS()
	_ = mem.MkdirAll("/home/u/a", 0o755)
	_ = mem.MkdirAll("/home/u/b", 0o755)
	_ = mem.WriteFile("/home/u/a/report.pdf", []byte("one"), 0o644)
	_ = mem.WriteFile("/home/u/b/report.pdf", []byte("two"), 0o644)
	tr := newTestTrash(mem)

	d1, err := tr.Put("/home/u/a/report.pdf")
	if err != nil {
		t.Fatal(err)
	}
	d2, err := tr.Put("/home/u/b/report.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if d1 == d2 {
		t.Fatalf("collision not resolved: both at %s", d1)
	}
	if !strings.HasSuffix(d2, "report.1.pdf") {
		t.Errorf("collision name = %q, want report.1.pdf suffix", d2)
	}
	if _, err := mem.Lstat("/home/u/.local/share/Trash/info/report.1.pdf.trashinfo"); err != nil {
		t.Error("collision trashinfo missing")
	}
}

func TestTrashDirectory(t *testing.T) {
	mem := runner.NewMemFS()
	_ = mem.MkdirAll("/home/u/old-project/sub", 0o755)
	_ = mem.WriteFile("/home/u/old-project/sub/x.txt", []byte("x"), 0o644)
	tr := newTestTrash(mem)
	dst, err := tr.Put("/home/u/old-project")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Lstat(dst + "/sub/x.txt"); err != nil {
		t.Error("directory contents not preserved in trash")
	}
}

func TestTrashSizeAndEmpty(t *testing.T) {
	mem := runner.NewMemFS()
	_ = mem.MkdirAll("/home/u/d", 0o755)
	_ = mem.WriteFile("/home/u/d/f1", make([]byte, 100), 0o644)
	_ = mem.WriteFile("/home/u/d/f2", make([]byte, 50), 0o644)
	tr := newTestTrash(mem)
	_, _ = tr.Put("/home/u/d/f1")
	_, _ = tr.Put("/home/u/d/f2")
	if got := tr.Size(); got != 150 {
		t.Errorf("Size = %d, want 150", got)
	}
	freed, err := tr.Empty()
	if err != nil {
		t.Fatal(err)
	}
	if freed != 150 {
		t.Errorf("Empty freed %d, want 150", freed)
	}
	if tr.Size() != 0 {
		t.Error("trash not empty after Empty")
	}
}

func TestValidateDeletePath(t *testing.T) {
	mem := runner.NewMemFS()
	home := "/home/u"
	_ = mem.MkdirAll("/home/u/.cache/app", 0o755)
	_ = mem.WriteFile("/home/u/.cache/app/data.bin", []byte("x"), 0o644)
	_ = mem.MkdirAll("/home/u/Downloads", 0o755)
	_ = mem.WriteFile("/home/u/Downloads/file.iso", []byte("x"), 0o644)
	_ = mem.WriteFile("/etc/passwd", []byte("root"), 0o644)
	// Symlink escaping the root.
	_ = mem.Symlink("/etc", "/home/u/.cache/evil")

	cacheRoots := []string{"/home/u/.cache"}
	tests := []struct {
		name  string
		path  string
		roots []string
		ok    bool
	}{
		{"inside root", "/home/u/.cache/app/data.bin", cacheRoots, true},
		{"the root itself", "/home/u/.cache", cacheRoots, false},
		{"slash", "/", cacheRoots, false},
		{"home", "/home/u", cacheRoots, false},
		{"outside root", "/home/u/Downloads/file.iso", cacheRoots, false},
		{"relative", "Downloads/file.iso", cacheRoots, false},
		{"system file", "/etc/passwd", cacheRoots, false},
		{"too shallow", "/home/u", []string{"/home"}, false},
		{"missing file", "/home/u/.cache/app/gone", cacheRoots, false},
	}
	for _, tc := range tests {
		_, err := ValidateDeletePath(mem, tc.path, home, tc.roots)
		if (err == nil) != tc.ok {
			t.Errorf("%s: ValidateDeletePath(%q) err=%v, want ok=%v", tc.name, tc.path, err, tc.ok)
		}
	}
}

func TestValidateDeletePathSymlinkEscape(t *testing.T) {
	mem := runner.NewMemFS()
	_ = mem.MkdirAll("/home/u/.cache", 0o755)
	_ = mem.MkdirAll("/etc/secrets", 0o755)
	_ = mem.WriteFile("/etc/secrets/key", []byte("k"), 0o600)
	// A symlinked dir inside the root pointing outside.
	_ = mem.Symlink("/etc/secrets", "/home/u/.cache/link")

	// Deleting *through* the symlink must be refused (resolves outside root).
	if _, err := ValidateDeletePath(mem, "/home/u/.cache/link/key", "/home/u", []string{"/home/u/.cache"}); err == nil {
		t.Error("path through symlink escaping the root was accepted")
	}
	// Deleting the symlink itself is fine (it lives inside the root).
	if _, err := ValidateDeletePath(mem, "/home/u/.cache/link", "/home/u", []string{"/home/u/.cache"}); err != nil {
		t.Errorf("deleting the symlink itself should be allowed: %v", err)
	}
}

func TestCleanerJournalsAndValidates(t *testing.T) {
	mem := runner.NewMemFS()
	_ = mem.MkdirAll("/home/u/Downloads", 0o755)
	_ = mem.WriteFile("/home/u/Downloads/old.zip", make([]byte, 10), 0o644)
	w, _ := journal.NewWriter(mem, "/home/u/.local/state/sysprune/journal", "s1")
	c := &Cleaner{FS: mem, J: w, Trash: newTestTrash(mem), Home: "/home/u"}
	roots := []string{"/home/u/Downloads"}

	f := Finding{Category: CatLargeFiles, Path: "/home/u/Downloads/old.zip", Size: 10}
	if err := c.MoveToTrash(f, roots); err != nil {
		t.Fatal(err)
	}
	s, _ := journal.Load(mem, w.Path())
	ops := s.Completed()
	if len(ops) != 1 || ops[0].Kind != "trash" || ops[0].DstPath == "" {
		t.Errorf("journal ops: %+v", ops)
	}

	// Outside allowed roots: refused even for trash.
	_ = mem.WriteFile("/home/u/Documents.txt", []byte("x"), 0o644)
	bad := Finding{Category: CatLargeFiles, Path: "/home/u/Documents.txt", Size: 1}
	if err := c.MoveToTrash(bad, roots); err == nil {
		t.Error("file outside allowed roots was trashed")
	}
}

func TestDeletePermanentlyRules(t *testing.T) {
	mem := runner.NewMemFS()
	_ = mem.MkdirAll("/home/u/.cache/app", 0o755)
	_ = mem.WriteFile("/home/u/.cache/app/c.bin", []byte("x"), 0o644)
	_ = mem.MkdirAll("/home/u/Downloads", 0o755)
	_ = mem.WriteFile("/home/u/Downloads/p.iso", []byte("x"), 0o644)
	w, _ := journal.NewWriter(mem, "/j", "s1")
	c := &Cleaner{FS: mem, J: w, Trash: newTestTrash(mem), Home: "/home/u"}

	// Regenerable category: permanent delete allowed without extra flag.
	if err := c.DeletePermanently(Finding{Category: CatUserCache, Path: "/home/u/.cache/app/c.bin", Size: 1}, nil, false); err != nil {
		t.Errorf("regenerable permanent delete: %v", err)
	}
	// Personal category: requires explicit user confirmation.
	pf := Finding{Category: CatLargeFiles, Path: "/home/u/Downloads/p.iso", Size: 1}
	if err := c.DeletePermanently(pf, []string{"/home/u/Downloads"}, false); err == nil {
		t.Error("personal permanent delete without confirmation was allowed")
	}
	if err := c.DeletePermanently(pf, []string{"/home/u/Downloads"}, true); err != nil {
		t.Errorf("confirmed personal permanent delete: %v", err)
	}
	if _, err := mem.Lstat("/home/u/Downloads/p.iso"); err == nil {
		t.Error("file not deleted")
	}
}
