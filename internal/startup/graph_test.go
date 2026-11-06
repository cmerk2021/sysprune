package startup

import (
	"reflect"
	"testing"
)

func unit(scope Scope, name string, mod func(*StartupItem)) *StartupItem {
	it := &StartupItem{
		ID: ItemID(KindSystemdUnit, scope, name), Kind: KindSystemdUnit,
		Scope: scope, Name: name, Enablement: "enabled", ActiveState: "active",
	}
	if mod != nil {
		mod(it)
	}
	return it
}

func TestGraphReverseDependencies(t *testing.T) {
	a := unit(ScopeSystem, "a.service", func(it *StartupItem) {
		it.Requires = []string{"b.service"}
		it.Wants = []string{"c.service"}
	})
	b := unit(ScopeSystem, "b.service", nil)
	c := unit(ScopeSystem, "c.service", nil)
	d := unit(ScopeSystem, "d.service", func(it *StartupItem) {
		it.WantedBy = []string{"a.service"} // a depends on d
	})
	g := NewGraph([]*StartupItem{a, b, c, d})

	if got := g.ActiveDependents(b, nil); !reflect.DeepEqual(got, []string{"system/a.service"}) {
		t.Errorf("dependents of b = %v", got)
	}
	if got := g.ActiveDependents(c, nil); !reflect.DeepEqual(got, []string{"system/a.service"}) {
		t.Errorf("dependents of c = %v", got)
	}
	if got := g.ActiveDependents(d, nil); !reflect.DeepEqual(got, []string{"system/a.service"}) {
		t.Errorf("dependents of d (via WantedBy) = %v", got)
	}
	needs := g.Needs(a)
	want := []string{"system/b.service", "system/c.service", "system/d.service"}
	if !reflect.DeepEqual(needs, want) {
		t.Errorf("needs of a = %v, want %v", needs, want)
	}
}

func TestGraphScopeSeparation(t *testing.T) {
	sysA := unit(ScopeSystem, "a.service", func(it *StartupItem) { it.Requires = []string{"b.service"} })
	sysB := unit(ScopeSystem, "b.service", nil)
	usrB := unit(ScopeUser, "b.service", nil)
	g := NewGraph([]*StartupItem{sysA, sysB, usrB})
	if got := g.ActiveDependents(usrB, nil); len(got) != 0 {
		t.Errorf("user b.service should have no dependents from system scope, got %v", got)
	}
	if got := g.ActiveDependents(sysB, nil); len(got) != 1 {
		t.Errorf("system b.service should have one dependent, got %v", got)
	}
}

func TestDependentsOnlyCountIfEnabledActiveAndNotQueued(t *testing.T) {
	svc := unit(ScopeSystem, "svc.service", nil)
	disabled := unit(ScopeSystem, "disabled-dep.service", func(it *StartupItem) {
		it.Requires = []string{"svc.service"}
		it.Enablement = "disabled"
		it.ActiveState = "inactive"
	})
	active := unit(ScopeSystem, "active-dep.service", func(it *StartupItem) {
		it.Requires = []string{"svc.service"}
	})
	g := NewGraph([]*StartupItem{svc, disabled, active})

	got := g.ActiveDependents(svc, nil)
	if !reflect.DeepEqual(got, []string{"system/active-dep.service"}) {
		t.Errorf("dependents = %v, want only active-dep", got)
	}
	// Queue active-dep for disable: it must stop counting.
	queued := map[string]bool{active.ID: true}
	if got := g.ActiveDependents(svc, queued); len(got) != 0 {
		t.Errorf("queued dependent still counted: %v", got)
	}
}

func TestSocketTimerActivationEdges(t *testing.T) {
	svc := unit(ScopeSystem, "cups.service", func(it *StartupItem) {
		it.TriggeredBy = []string{"cups.socket"}
	})
	sock := unit(ScopeSystem, "cups.socket", func(it *StartupItem) {
		it.Triggers = []string{"cups.service"}
	})
	g := NewGraph([]*StartupItem{svc, sock})
	got := g.ActiveDependents(svc, nil)
	if !reflect.DeepEqual(got, []string{"system/cups.socket"}) {
		t.Errorf("socket should be a dependent of its service, got %v", got)
	}
}

func TestRiskRating(t *testing.T) {
	leaf := unit(ScopeSystem, "leaf.service", nil)
	hub := unit(ScopeSystem, "hub.service", nil)
	dep := unit(ScopeSystem, "dep.service", func(it *StartupItem) { it.Requires = []string{"hub.service"} })
	timered := unit(ScopeSystem, "timered.service", func(it *StartupItem) { it.TriggeredBy = []string{"timered.timer"} })
	prot := unit(ScopeSystem, "prot.service", func(it *StartupItem) { it.Protected = true })
	targetPulled := unit(ScopeSystem, "pulled.service", func(it *StartupItem) {
		it.WantedBy = []string{"network-online.target"}
	})
	normalTarget := unit(ScopeSystem, "normal.service", func(it *StartupItem) {
		it.WantedBy = []string{"multi-user.target"}
	})
	g := NewGraph([]*StartupItem{leaf, hub, dep, timered, prot, targetPulled, normalTarget})

	tests := []struct {
		it   *StartupItem
		want Risk
	}{
		{leaf, RiskSafe},
		{hub, RiskThink},   // dep requires it
		{dep, RiskSafe},    // nothing depends on dep
		{timered, RiskThink},
		{prot, RiskProtected},
		{targetPulled, RiskThink},
		{normalTarget, RiskSafe}, // multi-user.target doesn't raise risk
	}
	for _, tc := range tests {
		if got := g.RateRisk(tc.it, nil); got != tc.want {
			t.Errorf("RateRisk(%s) = %v, want %v", tc.it.Name, got, tc.want)
		}
	}

	// Queue the dependent: hub becomes safe.
	queued := map[string]bool{dep.ID: true}
	if got := g.RateRisk(hub, queued); got != RiskSafe {
		t.Errorf("RateRisk(hub) with dependent queued = %v, want safe", got)
	}
}
