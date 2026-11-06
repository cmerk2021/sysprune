package storage

import (
	"context"
	"testing"
	"time"

	"github.com/cmerk2021/sysprune/internal/runner"
)

func TestFindDuplicates(t *testing.T) {
	mem := runner.NewMemFS()
	home := "/home/u"
	_ = mem.MkdirAll(home+"/Downloads", 0o755)
	_ = mem.MkdirAll(home+"/Documents", 0o755)
	content := []byte("identical content here, long enough to matter")
	_ = mem.WriteFile(home+"/Downloads/copy.txt", content, 0o644)
	_ = mem.WriteFile(home+"/Documents/original.txt", content, 0o644)
	_ = mem.WriteFile(home+"/Downloads/unique.txt", []byte("different"), 0o644)
	// Same size, different content: must not group.
	_ = mem.WriteFile(home+"/Downloads/samesize.txt", []byte("Identical content here, long enough to matter"), 0o644)
	// Older file in Documents should be the keeper.
	mem.SetModTime(home+"/Documents/original.txt", time.Unix(1000, 0))
	mem.SetModTime(home+"/Downloads/copy.txt", time.Unix(2000, 0))

	groups, err := FindDuplicates(context.Background(), mem, home, []string{home + "/Downloads", home + "/Documents"}, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1 (%+v)", len(groups), groups)
	}
	g := groups[0]
	if len(g.Files) != 2 {
		t.Fatalf("group files = %+v", g.Files)
	}
	if g.Wasted() != int64(len(content)) {
		t.Errorf("Wasted = %d", g.Wasted())
	}
	var keeper string
	keepers := 0
	for _, f := range g.Files {
		if f.Keeper {
			keepers++
			keeper = f.Path
		}
	}
	if keepers != 1 {
		t.Fatalf("keepers = %d, want exactly 1", keepers)
	}
	// Documents beats Downloads for the keeper suggestion; never auto-select
	// deletions (the keeper flag is only a suggestion).
	if keeper != home+"/Documents/original.txt" {
		t.Errorf("keeper = %q, want the Documents copy", keeper)
	}
}

func TestFindDuplicatesSkipsSymlinksAndProjectDirs(t *testing.T) {
	mem := runner.NewMemFS()
	home := "/home/u"
	content := []byte("payload-payload-payload")
	_ = mem.MkdirAll(home+"/Documents/.git", 0o755)
	_ = mem.MkdirAll(home+"/Documents/node_modules", 0o755)
	_ = mem.WriteFile(home+"/Documents/a.txt", content, 0o644)
	_ = mem.WriteFile(home+"/Documents/.git/b.txt", content, 0o644)
	_ = mem.WriteFile(home+"/Documents/node_modules/c.txt", content, 0o644)
	_ = mem.Symlink(home+"/Documents/a.txt", home+"/Documents/link.txt")

	groups, err := FindDuplicates(context.Background(), mem, home, []string{home + "/Documents"}, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Only a.txt is eligible: no group should form.
	if len(groups) != 0 {
		t.Errorf("groups = %+v, want none (symlinks and .git/node_modules skipped)", groups)
	}
}

func TestFindDuplicatesCancellation(t *testing.T) {
	mem := runner.NewMemFS()
	_ = mem.MkdirAll("/home/u/Documents", 0o755)
	_ = mem.WriteFile("/home/u/Documents/a", []byte("x"), 0o644)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := FindDuplicates(ctx, mem, "/home/u", []string{"/home/u/Documents"}, 2, nil); err == nil {
		t.Error("cancelled context should return an error")
	}
}
