package startup

import "sort"

// Graph is the in-memory dependency graph over startup items and raw unit
// names. Edges mean "from needs to" (dependency direction). Reverse edges
// give dependents.
type Graph struct {
	items map[string]*StartupItem // by item ID
	byName map[string]*StartupItem // by unit/file name within a scope: "<scope>/<name>"
	// needs[name] = set of names this unit depends on (hard + soft)
	needs map[string]map[string]edge
	// dependents[name] = set of names that depend on this unit
	dependents map[string]map[string]edge
}

type edge struct {
	// relation is Requires, Wants, BindsTo, PartOf, Upholds, Requisite,
	// WantedBy, RequiredBy, Triggers (socket/timer activation).
	relation string
}

// NewGraph builds a graph from items. Dependency names are unit names scoped
// with the item's scope, so user and system units never collide.
func NewGraph(items []*StartupItem) *Graph {
	g := &Graph{
		items:      map[string]*StartupItem{},
		byName:     map[string]*StartupItem{},
		needs:      map[string]map[string]edge{},
		dependents: map[string]map[string]edge{},
	}
	for _, it := range items {
		g.items[it.ID] = it
		g.byName[string(it.Scope)+"/"+it.Name] = it
	}
	addEdge := func(fromScope Scope, from, to, rel string) {
		f := string(fromScope) + "/" + from
		t := string(fromScope) + "/" + to
		if g.needs[f] == nil {
			g.needs[f] = map[string]edge{}
		}
		g.needs[f][t] = edge{relation: rel}
		if g.dependents[t] == nil {
			g.dependents[t] = map[string]edge{}
		}
		g.dependents[t][f] = edge{relation: rel}
	}
	for _, it := range items {
		for _, d := range it.Requires {
			addEdge(it.Scope, it.Name, d, "Requires")
		}
		for _, d := range it.Requisite {
			addEdge(it.Scope, it.Name, d, "Requisite")
		}
		for _, d := range it.BindsTo {
			addEdge(it.Scope, it.Name, d, "BindsTo")
		}
		for _, d := range it.Wants {
			addEdge(it.Scope, it.Name, d, "Wants")
		}
		for _, d := range it.Upholds {
			addEdge(it.Scope, it.Name, d, "Upholds")
		}
		// PartOf: stop/restart propagates from the target to this unit, so
		// this unit effectively depends on the PartOf target.
		for _, d := range it.PartOf {
			addEdge(it.Scope, it.Name, d, "PartOf")
		}
		// WantedBy/RequiredBy: the listed unit depends on this item.
		for _, d := range it.WantedBy {
			addEdge(it.Scope, d, it.Name, "WantedBy")
		}
		for _, d := range it.RequiredBy {
			addEdge(it.Scope, d, it.Name, "RequiredBy")
		}
		// A socket/timer that triggers a service is a dependent of the
		// service's function: disabling the service while its trigger stays
		// enabled is surprising. Model trigger -> service as "needs".
		for _, svc := range it.Triggers {
			addEdge(it.Scope, it.Name, svc, "Triggers")
		}
		// TriggeredBy is the reverse view; add for completeness in case only
		// one side was discovered.
		for _, trig := range it.TriggeredBy {
			addEdge(it.Scope, trig, it.Name, "Triggers")
		}
	}
	return g
}

// Item returns the item for a scoped name, if we discovered it.
func (g *Graph) Item(scope Scope, name string) *StartupItem {
	return g.byName[string(scope)+"/"+name]
}

// countableDependent reports whether the dependent identified by scoped name
// should count: it must be enabled or active, and not already queued for
// disable.
func (g *Graph) countableDependent(scoped string, queuedDisable map[string]bool) bool {
	it, ok := g.byName[scoped]
	if !ok {
		// Unknown unit (not in our discovered set, e.g. a target). Targets
		// and unknown units are treated as "pulls" handled by risk rating,
		// not dependents, except we conservatively count non-target units.
		return false
	}
	if queuedDisable[it.ID] {
		return false
	}
	enabled := it.Enablement == "enabled" || it.Enablement == "enabled-runtime" ||
		it.Enablement == "static" || it.Enablement == "linked" || it.Enablement == "generated" ||
		it.Enablement == "autostart"
	active := it.ActiveState == "active" || it.ActiveState == "activating" || it.ActiveState == "reloading"
	return enabled || active
}

// ActiveDependents returns the scoped names of dependents of item that are
// enabled or active and not queued for disable, sorted.
func (g *Graph) ActiveDependents(it *StartupItem, queuedDisable map[string]bool) []string {
	var out []string
	for dep := range g.dependents[string(it.Scope)+"/"+it.Name] {
		if g.countableDependent(dep, queuedDisable) {
			out = append(out, dep)
		}
	}
	sort.Strings(out)
	return out
}

// Needs returns the scoped names this item depends on, sorted.
func (g *Graph) Needs(it *StartupItem) []string {
	var out []string
	for n := range g.needs[string(it.Scope)+"/"+it.Name] {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Relation returns the relation label between dependent and dependency, if any.
func (g *Graph) Relation(fromScoped, toScoped string) string {
	if m, ok := g.needs[fromScoped]; ok {
		if e, ok := m[toScoped]; ok {
			return e.relation
		}
	}
	return ""
}

// RateRisk applies Layer 2: dependency-based risk rating for a non-protected
// item.
func (g *Graph) RateRisk(it *StartupItem, queuedDisable map[string]bool) Risk {
	if it.Protected {
		return RiskProtected
	}
	if len(g.ActiveDependents(it, queuedDisable)) > 0 {
		return RiskThink
	}
	// Pulled in by a target (WantedBy/RequiredBy pointing at a *.target) or
	// socket/timer activated → think twice.
	for _, w := range append(append([]string{}, it.WantedBy...), it.RequiredBy...) {
		if isTarget(w) && w != "default.target" && w != "multi-user.target" && w != "graphical.target" {
			return RiskThink
		}
	}
	if len(it.TriggeredBy) > 0 {
		return RiskThink
	}
	return RiskSafe
}

func isTarget(name string) bool {
	return len(name) > 7 && name[len(name)-7:] == ".target"
}
