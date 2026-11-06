package startup

import (
	"strings"
	"testing"

	"github.com/cmerk2021/sysprune/internal/runner"
	"github.com/cmerk2021/sysprune/internal/state"
)

func newTestAudit(items []*StartupItem, saved *state.Decisions) *Audit {
	g := NewGraph(items)
	for _, it := range items {
		it.Dependents = g.ActiveDependents(it, nil)
		it.Risk = g.RateRisk(it, nil)
	}
	return NewAudit(items, g, saved)
}

func TestAuditLeavesFirstOrdering(t *testing.T) {
	// a requires b: b has a dependent, a is a leaf.
	a := unit(ScopeSystem, "a.service", func(it *StartupItem) { it.Requires = []string{"b.service"} })
	b := unit(ScopeSystem, "b.service", nil)
	audit := newTestAudit([]*StartupItem{a, b}, nil)

	first := audit.Next()
	if first == nil || first.Name != "a.service" {
		t.Fatalf("first item = %v, want leaf a.service", first)
	}
}

func TestAuditPromotionAfterDisable(t *testing.T) {
	// Acceptance criterion 3: disabling A makes B (only needed by A) appear
	// next with a note.
	a := unit(ScopeSystem, "a.service", func(it *StartupItem) { it.Requires = []string{"b.service"} })
	b := unit(ScopeSystem, "b.service", nil)
	c := unit(ScopeSystem, "c.service", nil)
	audit := newTestAudit([]*StartupItem{a, b, c}, nil)

	// Initially b is not a leaf.
	if len(b.Dependents) == 0 {
		t.Fatal("precondition: b must have a dependent")
	}
	// Review: decide on leaves a and c first.
	audit.Decide(a, ActionDisable)
	audit.Decide(c, ActionKeep)

	next := audit.Next()
	if next == nil || next.Name != "b.service" {
		t.Fatalf("next = %v, want promoted b.service", next)
	}
	if next.PromotedBy == "" || !strings.Contains(next.PromotedBy, "a.service") {
		t.Errorf("promotion note = %q, want mention of a.service", next.PromotedBy)
	}
	if next.Risk != RiskSafe {
		t.Errorf("promoted item risk = %v, want safe (queued disable counts as applied)", next.Risk)
	}
}

func TestQueuedChangesCountAsApplied(t *testing.T) {
	a := unit(ScopeSystem, "a.service", func(it *StartupItem) { it.Requires = []string{"b.service"} })
	b := unit(ScopeSystem, "b.service", nil)
	audit := newTestAudit([]*StartupItem{a, b}, nil)

	audit.Decide(a, ActionDisable)
	audit.Recompute()
	if len(b.Dependents) != 0 {
		t.Errorf("b still has dependents %v after a was queued for disable", b.Dependents)
	}
	// Undo restores the dependency.
	undone := audit.UndoLast()
	if undone != a.ID {
		t.Errorf("UndoLast = %q, want %q", undone, a.ID)
	}
	audit.Recompute()
	if len(b.Dependents) != 1 {
		t.Errorf("b should regain its dependent after undo, got %v", b.Dependents)
	}
}

func TestAuditSkipGoesToEnd(t *testing.T) {
	a := unit(ScopeSystem, "aaa.service", nil)
	b := unit(ScopeSystem, "bbb.service", nil)
	audit := newTestAudit([]*StartupItem{a, b}, nil)

	first := audit.Next()
	audit.Decide(first, ActionSkip)
	second := audit.Next()
	if second.ID == first.ID {
		t.Fatal("skipped item offered again immediately")
	}
	audit.Decide(second, ActionKeep)
	third := audit.Next()
	if third == nil || third.ID != first.ID {
		t.Fatalf("skipped item should come back at the end, got %v", third)
	}
}

func TestAuditProtectedExcluded(t *testing.T) {
	p := unit(ScopeSystem, "dbus.service", func(it *StartupItem) {
		it.Protected = true
		it.Risk = RiskProtected
	})
	n := unit(ScopeSystem, "normal.service", nil)
	audit := newTestAudit([]*StartupItem{p, n}, nil)

	for it := audit.Next(); it != nil; it = audit.Next() {
		if it.Protected {
			t.Fatalf("protected item %s offered for decision", it.Name)
		}
		audit.Decide(it, ActionKeep)
	}
	if got := audit.Protected(); len(got) != 1 || got[0].Name != "dbus.service" {
		t.Errorf("Protected() = %v", got)
	}
}

func TestAuditSavedDecisionsSkipped(t *testing.T) {
	mem := runner.NewMemFS()
	saved, err := state.LoadDecisions(mem, "/home/u/.local/state/sysprune/decisions.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := saved.Keep(state.DecisionKey{Kind: "unit", Scope: "system", Name: "kept.service"}); err != nil {
		t.Fatal(err)
	}
	kept := unit(ScopeSystem, "kept.service", nil)
	other := unit(ScopeSystem, "other.service", nil)
	audit := newTestAudit([]*StartupItem{kept, other}, saved)

	if audit.SkippedKeptCount() != 1 {
		t.Errorf("SkippedKeptCount = %d, want 1", audit.SkippedKeptCount())
	}
	first := audit.Next()
	if first == nil || first.Name != "other.service" {
		t.Fatalf("Next = %v, want other.service (kept skipped)", first)
	}
	audit.Decide(first, ActionKeep)
	if audit.Next() != nil {
		t.Error("kept.service should not be offered")
	}

	// ShowKept toggle brings it back.
	audit.ShowKept = true
	if audit.Next() == nil {
		t.Error("with ShowKept, kept.service should be offered")
	}
}

func TestAuditImpactOrdering(t *testing.T) {
	small := unit(ScopeSystem, "small.service", func(it *StartupItem) {
		it.Resources.MemoryBytes = 1 << 20
	})
	big := unit(ScopeSystem, "big.service", func(it *StartupItem) {
		it.Resources.MemoryBytes = 500 << 20
	})
	audit := newTestAudit([]*StartupItem{small, big}, nil)
	if got := audit.Next(); got.Name != "big.service" {
		t.Errorf("Next = %s, want high-impact big.service first", got.Name)
	}
}

func TestEstimatedSavings(t *testing.T) {
	a := unit(ScopeSystem, "a.service", func(it *StartupItem) {
		it.Resources.MemoryBytes = 10 << 20
		it.Resources.BootMillis = 1500
	})
	b := unit(ScopeSystem, "b.service", nil)
	audit := newTestAudit([]*StartupItem{a, b}, nil)
	audit.Decide(a, ActionDisable)
	mem, boot := audit.EstimatedSavings()
	if mem != 10<<20 || boot != 1500 {
		t.Errorf("EstimatedSavings = %d, %v", mem, boot)
	}
}

func TestCheckStopNowNamesBlockers(t *testing.T) {
	// Acceptance criterion 4.
	svc := unit(ScopeSystem, "svc.service", nil)
	dep := unit(ScopeSystem, "dep.service", func(it *StartupItem) {
		it.Requires = []string{"svc.service"}
		it.FriendlyName = "Dep App"
	})
	audit := newTestAudit([]*StartupItem{svc, dep}, nil)

	bl := audit.CheckStopNow(svc)
	if !bl.Blocked {
		t.Fatal("expected stop-now to be blocked")
	}
	if len(bl.Users) != 1 || !strings.Contains(bl.Users[0], "dep.service") {
		t.Errorf("blockers = %v, want dep.service named", bl.Users)
	}
	if !strings.Contains(bl.Suggestion, "Disable") || !strings.Contains(bl.Suggestion, "next boot") {
		t.Errorf("suggestion = %q", bl.Suggestion)
	}

	// Queue the dependent for disable: no longer blocked.
	audit.Decide(dep, ActionDisableStop)
	if audit.CheckStopNow(svc).Blocked {
		t.Error("stop-now should be unblocked once the dependent is queued")
	}
}

func TestCheckStopNowSocketActivation(t *testing.T) {
	svc := unit(ScopeSystem, "cups.service", func(it *StartupItem) {
		it.TriggeredBy = []string{"cups.socket"}
	})
	sock := unit(ScopeSystem, "cups.socket", func(it *StartupItem) {
		it.Triggers = []string{"cups.service"}
	})
	audit := newTestAudit([]*StartupItem{svc, sock}, nil)
	bl := audit.CheckStopNow(svc)
	if !bl.Blocked {
		t.Fatal("socket-activated service should warn on stop-now")
	}
	found := false
	for _, u := range bl.Users {
		if strings.Contains(u, "cups.socket") {
			found = true
		}
	}
	if !found {
		t.Errorf("blockers %v should name cups.socket", bl.Users)
	}
}

func TestBuildPlansSplitsScopes(t *testing.T) {
	sys := unit(ScopeSystem, "sys.service", nil)
	usr := unit(ScopeUser, "usr.service", nil)
	xdg := &StartupItem{
		ID: ItemID(KindXDGAutostart, ScopeUser, "app.desktop"), Kind: KindXDGAutostart,
		Scope: ScopeUser, Name: "app.desktop", Enablement: "enabled", UnitPath: "/home/u/.config/autostart/app.desktop",
	}
	audit := newTestAudit([]*StartupItem{sys, usr, xdg}, nil)
	audit.Decide(sys, ActionDisable)
	audit.Decide(usr, ActionDisableStop)
	audit.Decide(xdg, ActionDisable)

	userPlan, systemPlan, localOps := BuildPlans(audit, "sess1")
	if len(systemPlan.Changes) != 1 || systemPlan.Changes[0].Unit != "sys.service" {
		t.Errorf("system plan = %+v", systemPlan.Changes)
	}
	if len(userPlan.Changes) != 2 || userPlan.Changes[0].Unit != "usr.service" {
		t.Errorf("user plan = %+v", userPlan.Changes)
	}
	// Disable+stop becomes two ops: disable then stop.
	if userPlan.Changes[1].Op != "stop" {
		t.Errorf("second user op = %q, want stop", userPlan.Changes[1].Op)
	}
	if len(localOps) != 1 || localOps[0].ItemID != xdg.ID {
		t.Errorf("localOps = %+v", localOps)
	}
	// Dry-run command rendering.
	cmds := systemPlan.Changes[0].Commands()
	if len(cmds) != 1 || cmds[0] != "systemctl disable -- sys.service" {
		t.Errorf("Commands() = %v", cmds)
	}
}
