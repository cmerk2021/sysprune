package apply

import (
	"context"
	"strings"
	"testing"

	"github.com/cmerk2021/sysprune/internal/journal"
	"github.com/cmerk2021/sysprune/internal/runner"
	"github.com/cmerk2021/sysprune/internal/startup"
)

func newExecutor(t *testing.T, f *runner.FakeRunner, mem *runner.MemFS) *Executor {
	t.Helper()
	w, err := journal.NewWriter(mem, "/state/journal", "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	return &Executor{R: f, FS: mem, J: w, CheckDrift: true}
}

func change(op, unit string) startup.PlanChange {
	return startup.PlanChange{
		ItemID: "unit/system/" + unit, Kind: "unit", Scope: "system",
		Op: op, Unit: unit, PriorEnabled: "enabled", PriorActive: "active",
	}
}

func scriptUnitState(f *runner.FakeRunner, unit, enabled, active string) {
	f.ScriptPrefix("systemctl show", runner.Response{
		Stdout:      []byte("UnitFileState=" + enabled + "\nActiveState=" + active + "\nLoadState=loaded\n"),
		MatchSuffix: " -- " + unit,
	})
}

func TestExecutorWriteAheadJournal(t *testing.T) {
	// Acceptance criterion 5: every change appears in the journal BEFORE
	// execution.
	f := runner.NewFake()
	mem := runner.NewMemFS()
	scriptUnitState(f, "bluetooth.service", "enabled", "active")
	e := newExecutor(t, f, mem)

	p := &startup.Plan{Version: startup.PlanVersion, SessionID: "sess-1",
		Changes: []startup.PlanChange{change("disable", "bluetooth.service")}}
	res := e.Execute(context.Background(), p)
	if len(res.Results) != 1 || !res.Results[0].OK {
		t.Fatalf("results: %+v", res.Results)
	}
	if !f.CalledWith("systemctl", "disable", "--", "bluetooth.service") {
		t.Errorf("expected systemctl disable call, got %v", f.CallLines())
	}
	s, err := journal.Load(mem, e.J.Path())
	if err != nil {
		t.Fatal(err)
	}
	// Intent must precede done in the record stream.
	var seq []string
	for _, r := range s.Records {
		if r.Type == "intent" || r.Type == "done" {
			seq = append(seq, r.Type)
		}
	}
	if len(seq) != 2 || seq[0] != "intent" || seq[1] != "done" {
		t.Errorf("record sequence = %v, want [intent done]", seq)
	}
	ops := s.Completed()
	if len(ops) != 1 || ops[0].Unit != "bluetooth.service" || ops[0].Prior.Enabled != "enabled" {
		t.Errorf("completed ops: %+v", ops)
	}
}

func TestExecutorDriftSkips(t *testing.T) {
	f := runner.NewFake()
	mem := runner.NewMemFS()
	// Plan says enabled, system says disabled -> drift.
	scriptUnitState(f, "drifted.service", "disabled", "inactive")
	e := newExecutor(t, f, mem)

	res := e.executeOne(context.Background(), change("disable", "drifted.service"))
	if !res.Skipped {
		t.Fatalf("drifted unit not skipped: %+v", res)
	}
	if !strings.Contains(res.Message, "drifted") {
		t.Errorf("message = %q", res.Message)
	}
	if f.CalledWith("systemctl", "disable", "--", "drifted.service") {
		t.Error("drifted unit was still disabled")
	}
}

func TestExecutorMissingUnitSkips(t *testing.T) {
	f := runner.NewFake()
	mem := runner.NewMemFS()
	f.ScriptPrefix("systemctl show", runner.Response{Stdout: []byte("LoadState=not-found\n")})
	e := newExecutor(t, f, mem)
	res := e.executeOne(context.Background(), change("disable", "gone.service"))
	if !res.Skipped {
		t.Fatalf("missing unit not skipped: %+v", res)
	}
}

func TestExecutorFailureIsRecordedAndDoesNotAbort(t *testing.T) {
	f := runner.NewFake()
	mem := runner.NewMemFS()
	scriptUnitState(f, "fails.service", "enabled", "active")
	scriptUnitState(f, "ok.service", "enabled", "active")
	f.Script("systemctl", []string{"disable", "--", "fails.service"},
		runner.Response{ExitCode: 1, Stderr: []byte("permission denied")})
	e := newExecutor(t, f, mem)

	p := &startup.Plan{Version: startup.PlanVersion, SessionID: "sess-1",
		Changes: []startup.PlanChange{change("disable", "fails.service"), change("disable", "ok.service")}}
	res := e.Execute(context.Background(), p)
	if res.Results[0].OK {
		t.Error("failing change reported OK")
	}
	if !res.Results[1].OK {
		t.Errorf("later change aborted by earlier failure: %+v", res.Results[1])
	}
	s, _ := journal.Load(mem, e.J.Path())
	var types []string
	for _, r := range s.Records {
		if r.Type != "meta" {
			types = append(types, r.Type)
		}
	}
	want := []string{"intent", "failed", "intent", "done"}
	if strings.Join(types, " ") != strings.Join(want, " ") {
		t.Errorf("record types = %v, want %v", types, want)
	}
}

func TestExecutorCleanOps(t *testing.T) {
	f := runner.NewFake()
	mem := runner.NewMemFS()
	e := newExecutor(t, f, mem)
	c := startup.PlanChange{ItemID: "clean/system/journal", Kind: "clean", Scope: "system", Op: "vacuum-journal", Param: "500M"}
	res := e.executeOne(context.Background(), c)
	if !res.OK {
		t.Fatalf("vacuum failed: %+v", res)
	}
	if !f.CalledWith("journalctl", "--vacuum-size=500M") {
		t.Errorf("calls: %v", f.CallLines())
	}
	c.Param = "14d"
	e.executeOne(context.Background(), c)
	if !f.CalledWith("journalctl", "--vacuum-time=14d") {
		t.Errorf("calls: %v", f.CallLines())
	}
}
