package state

import (
	"testing"

	"github.com/cmerk2021/sysprune/internal/runner"
)

func TestDecisionsPersistAndReset(t *testing.T) {
	mem := runner.NewMemFS()
	dir := "/home/u/.local/state/sysprune"
	d, err := LoadDecisions(mem, dir)
	if err != nil {
		t.Fatal(err)
	}
	k := DecisionKey{Kind: "unit", Scope: "system", Name: "bluetooth.service"}
	if d.IsKept(k) {
		t.Error("fresh store should be empty")
	}
	if err := d.Keep(k); err != nil {
		t.Fatal(err)
	}
	if !d.IsKept(k) || d.Count() != 1 {
		t.Error("Keep not recorded")
	}

	// Reload from disk: persisted.
	d2, err := LoadDecisions(mem, dir)
	if err != nil {
		t.Fatal(err)
	}
	if !d2.IsKept(k) {
		t.Error("decision not persisted")
	}

	// Identity is kind+scope+name: a different scope is a different item.
	if d2.IsKept(DecisionKey{Kind: "unit", Scope: "user", Name: "bluetooth.service"}) {
		t.Error("scope must be part of the identity")
	}

	// Reset clears everything (what --reset-decisions uses).
	if err := d2.Reset(); err != nil {
		t.Fatal(err)
	}
	if d2.Count() != 0 {
		t.Error("Reset did not clear in memory")
	}
	d3, _ := LoadDecisions(mem, dir)
	if d3.Count() != 0 {
		t.Error("Reset did not clear on disk")
	}
}

func TestDefaultPaths(t *testing.T) {
	p := DefaultPaths("sysprune")
	if p.StateDir == "" || p.ConfigDir == "" {
		t.Errorf("paths: %+v", p)
	}
}
