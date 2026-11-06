package journal

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cmerk2021/sysprune/internal/runner"
)

func op(kind, scope, opName, unit string) Operation {
	o := Operation{ID: kind + "/" + scope + "/" + unit, Kind: kind, Scope: scope, Op: opName, Unit: unit}
	o.Prior.Enabled = "enabled"
	o.Prior.Active = "active"
	return o
}

func TestWriteAheadAndLoad(t *testing.T) {
	mem := runner.NewMemFS()
	w, err := NewWriter(mem, "/j", "sess")
	if err != nil {
		t.Fatal(err)
	}
	o := op("unit", "system", "disable", "a.service")
	if err := w.Intent(o); err != nil {
		t.Fatal(err)
	}
	if err := w.Done(o); err != nil {
		t.Fatal(err)
	}
	s, err := Load(mem, w.Path())
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Completed(); len(got) != 1 || got[0].Unit != "a.service" {
		t.Errorf("Completed = %+v", got)
	}
	if got := s.Incomplete(); len(got) != 0 {
		t.Errorf("Incomplete = %+v", got)
	}
}

func TestCrashRecoveryDetection(t *testing.T) {
	// A crash mid-apply leaves an intent without done/failed.
	mem := runner.NewMemFS()
	w, _ := NewWriter(mem, "/j", "sess")
	done := op("unit", "system", "disable", "done.service")
	hanging := op("unit", "system", "disable", "crashed.service")
	_ = w.Intent(done)
	_ = w.Done(done)
	_ = w.Intent(hanging)
	// crash here: no done/failed record for hanging.

	s, err := Load(mem, w.Path())
	if err != nil {
		t.Fatal(err)
	}
	inc := s.Incomplete()
	if len(inc) != 1 || inc[0].Unit != "crashed.service" {
		t.Fatalf("Incomplete = %+v, want crashed.service", inc)
	}
	if got := s.Completed(); len(got) != 1 || got[0].Unit != "done.service" {
		t.Errorf("Completed = %+v", got)
	}
}

func TestFailedOpsAreNotRestored(t *testing.T) {
	mem := runner.NewMemFS()
	w, _ := NewWriter(mem, "/j", "sess")
	bad := op("unit", "system", "disable", "bad.service")
	_ = w.Intent(bad)
	_ = w.Failed(bad, context.DeadlineExceeded)
	s, _ := Load(mem, w.Path())
	if len(s.Completed()) != 0 {
		t.Error("failed op counted as completed")
	}
}

func TestRestoreReversesInReverseOrder(t *testing.T) {
	// Acceptance criterion 5: --restore returns the system to prior state.
	mem := runner.NewMemFS()
	w, _ := NewWriter(mem, "/j", "sess")
	first := op("unit", "system", "disable", "first.service")
	second := op("unit", "system", "stop", "second.service")
	for _, o := range []Operation{first, second} {
		_ = w.Intent(o)
		_ = w.Done(o)
	}
	s, _ := Load(mem, w.Path())

	f := runner.NewFake()
	r := &Restorer{R: f, FS: mem}
	results := r.Restore(context.Background(), s)
	if len(results) != 2 {
		t.Fatalf("results: %+v", results)
	}
	// Reverse order: second restored first.
	if results[0].Op.Unit != "second.service" || results[1].Op.Unit != "first.service" {
		t.Errorf("restore order wrong: %+v", results)
	}
	if !f.CalledWith("systemctl", "start", "--", "second.service") {
		t.Errorf("stop not reversed by start: %v", f.CallLines())
	}
	if !f.CalledWith("systemctl", "enable", "--", "first.service") {
		t.Errorf("disable not reversed by enable: %v", f.CallLines())
	}
}

func TestRestoreRespectsPriorState(t *testing.T) {
	mem := runner.NewMemFS()
	w, _ := NewWriter(mem, "/j", "sess")
	o := op("unit", "user", "stop", "was-inactive.service")
	o.Prior.Active = "inactive" // was not running before
	_ = w.Intent(o)
	_ = w.Done(o)
	s, _ := Load(mem, w.Path())
	f := runner.NewFake()
	r := &Restorer{R: f, FS: mem}
	res := r.Restore(context.Background(), s)
	if res[0].Err != nil {
		t.Fatal(res[0].Err)
	}
	if f.CalledWith("systemctl", "--user", "start", "--", "was-inactive.service") {
		t.Error("restarted a unit that was not active before")
	}
}

func TestRestoreMask(t *testing.T) {
	mem := runner.NewMemFS()
	w, _ := NewWriter(mem, "/j", "sess")
	o := op("unit", "system", "mask", "masked.service")
	_ = w.Intent(o)
	_ = w.Done(o)
	s, _ := Load(mem, w.Path())
	f := runner.NewFake()
	(&Restorer{R: f, FS: mem}).Restore(context.Background(), s)
	if !f.CalledWith("systemctl", "unmask", "--", "masked.service") {
		t.Errorf("mask not reversed: %v", f.CallLines())
	}
	if !f.CalledWith("systemctl", "enable", "--", "masked.service") {
		t.Error("prior enabled state not restored after unmask")
	}
}

func TestRestoreXDG(t *testing.T) {
	mem := runner.NewMemFS()
	// Moved file case.
	_ = mem.MkdirAll("/home/u/.local/state/sysprune/backups/sess", 0o755)
	_ = mem.WriteFile("/home/u/.local/state/sysprune/backups/sess/app.desktop", []byte("x"), 0o644)
	moved := Operation{Kind: "xdg", Scope: "user", Op: "move-xdg",
		SrcPath: "/home/u/.config/autostart/app.desktop",
		DstPath: "/home/u/.local/state/sysprune/backups/sess/app.desktop"}
	// Hidden override case.
	_ = mem.WriteFile("/home/u/.config/autostart/sys.desktop", []byte("[Desktop Entry]\nHidden=true\n"), 0o644)
	hidden := Operation{Kind: "xdg", Scope: "user", Op: "hide-xdg",
		SrcPath: "/etc/xdg/autostart/sys.desktop",
		DstPath: "/home/u/.config/autostart/sys.desktop"}

	w, _ := NewWriter(mem, "/j", "sess")
	for _, o := range []Operation{moved, hidden} {
		_ = w.Intent(o)
		_ = w.Done(o)
	}
	s, _ := Load(mem, w.Path())
	results := (&Restorer{R: runner.NewFake(), FS: mem}).Restore(context.Background(), s)
	for _, r := range results {
		if r.Err != nil {
			t.Fatalf("restore error: %v", r.Err)
		}
	}
	if _, err := mem.Lstat("/home/u/.config/autostart/app.desktop"); err != nil {
		t.Error("moved desktop file not restored")
	}
	if _, err := mem.Lstat("/home/u/.config/autostart/sys.desktop"); err == nil {
		t.Error("Hidden=true override not removed")
	}
}

func TestRestoreCron(t *testing.T) {
	mem := runner.NewMemFS()
	_ = mem.MkdirAll("/backups", 0o755)
	_ = mem.WriteFile("/backups/crontab.bak", []byte("@reboot /home/u/bin/x\n"), 0o600)
	o := Operation{Kind: "cron", Scope: "user", Op: "disable-cron", BackupPath: "/backups/crontab.bak"}
	w, _ := NewWriter(mem, "/j", "sess")
	_ = w.Intent(o)
	_ = w.Done(o)
	s, _ := Load(mem, w.Path())
	f := runner.NewFake()
	results := (&Restorer{R: f, FS: mem}).Restore(context.Background(), s)
	if results[0].Err != nil {
		t.Fatal(results[0].Err)
	}
	if !f.CalledWith("crontab", "/backups/crontab.bak.restore") {
		t.Errorf("crontab not reinstalled: %v", f.CallLines())
	}
}

func TestRestoreTrashAndPermanentDeletes(t *testing.T) {
	mem := runner.NewMemFS()
	_ = mem.MkdirAll("/home/u/.local/share/Trash/files", 0o755)
	_ = mem.MkdirAll("/home/u/.local/share/Trash/info", 0o755)
	_ = mem.WriteFile("/home/u/.local/share/Trash/files/big.iso", []byte("data"), 0o644)
	_ = mem.WriteFile("/home/u/.local/share/Trash/info/big.iso.trashinfo", []byte("[Trash Info]\n"), 0o644)

	trash := Operation{Kind: "trash", Scope: "user", Op: "trash",
		SrcPath: "/home/u/Downloads/big.iso",
		DstPath: "/home/u/.local/share/Trash/files/big.iso"}
	perm := Operation{Kind: "delete", Scope: "user", Op: "delete", SrcPath: "/home/u/.cache/x"}

	w, _ := NewWriter(mem, "/j", "sess")
	for _, o := range []Operation{trash, perm} {
		_ = w.Intent(o)
		_ = w.Done(o)
	}
	s, _ := Load(mem, w.Path())
	results := (&Restorer{R: runner.NewFake(), FS: mem}).Restore(context.Background(), s)

	var trashRes, permRes *RestoreResult
	for i := range results {
		switch results[i].Op.Kind {
		case "trash":
			trashRes = &results[i]
		case "delete":
			permRes = &results[i]
		}
	}
	if trashRes.Err != nil {
		t.Fatal(trashRes.Err)
	}
	if _, err := mem.Lstat("/home/u/Downloads/big.iso"); err != nil {
		t.Error("trashed file not restored")
	}
	if _, err := mem.Lstat("/home/u/.local/share/Trash/info/big.iso.trashinfo"); err == nil {
		t.Error("trashinfo not removed after restore")
	}
	if !permRes.Skipped {
		t.Error("permanent deletion must be reported as non-restorable")
	}
}

func TestSessionListAndFind(t *testing.T) {
	mem := runner.NewMemFS()
	w1, _ := NewWriter(mem, "/j", "20240101T000000-aaaa")
	_ = w1.Meta("session one")
	time.Sleep(time.Millisecond)
	w2, _ := NewWriter(mem, "/j2", "20240102T000000-bbbb")
	_ = w2.Meta("session two")

	sessions, err := List(mem, "/j", "/j2")
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("sessions: %d", len(sessions))
	}
	s, err := Find(mem, "20240101T000000-aaaa", "/j", "/j2")
	if err != nil || s == nil {
		t.Fatalf("Find: %v", err)
	}
	if !strings.Contains(s.Path, "aaaa") {
		t.Errorf("found wrong session: %s", s.Path)
	}
}

func TestNeedsRoot(t *testing.T) {
	mem := runner.NewMemFS()
	w, _ := NewWriter(mem, "/j", "s")
	sys := op("unit", "system", "disable", "a.service")
	_ = w.Intent(sys)
	_ = w.Done(sys)
	s, _ := Load(mem, w.Path())
	if !NeedsRoot(s) {
		t.Error("system-scope session should need root")
	}

	w2, _ := NewWriter(mem, "/j", "s2")
	usr := op("unit", "user", "disable", "b.service")
	_ = w2.Intent(usr)
	_ = w2.Done(usr)
	s2, _ := Load(mem, w2.Path())
	if NeedsRoot(s2) {
		t.Error("user-scope session should not need root")
	}
}
