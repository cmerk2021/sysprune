package startup

import (
	"sort"

	"github.com/cmerk2021/sysprune/internal/state"
)

// ActionType is a queued decision for one item.
type ActionType string

const (
	ActionKeep        ActionType = "keep"
	ActionKeepForever ActionType = "keep-forever" // keep + don't ask again
	ActionDisable     ActionType = "disable"      // disable at next boot
	ActionDisableStop ActionType = "disable-stop" // disable + stop now
	ActionMask        ActionType = "mask"         // advanced
	ActionSkip        ActionType = "skip"
)

// Decision is one queued user choice.
type Decision struct {
	ItemID string
	Action ActionType
}

// Audit drives the recomputing review loop.
type Audit struct {
	Items     []*StartupItem
	Graph     *Graph
	Decisions []Decision
	Saved     *state.Decisions // may be nil
	ShowKept  bool             // include previously "keep forever" items
	skipped   map[string]bool  // items skipped this session (revisit at end)
}

// NewAudit builds an audit over analyzed items.
func NewAudit(items []*StartupItem, g *Graph, saved *state.Decisions) *Audit {
	return &Audit{Items: items, Graph: g, Saved: saved, skipped: map[string]bool{}}
}

// QueuedDisables returns ids queued for disable/disable-stop/mask — these
// count as already applied when evaluating later items.
func (a *Audit) QueuedDisables() map[string]bool {
	q := map[string]bool{}
	for _, d := range a.Decisions {
		if d.Action == ActionDisable || d.Action == ActionDisableStop || d.Action == ActionMask {
			q[d.ItemID] = true
		}
	}
	return q
}

func (a *Audit) decided(id string) bool {
	for _, d := range a.Decisions {
		if d.ItemID == id {
			return true
		}
	}
	return false
}

func (a *Audit) savedKeep(it *StartupItem) bool {
	if a.Saved == nil || a.ShowKept {
		return false
	}
	return a.Saved.IsKept(state.DecisionKey{Kind: string(it.Kind), Scope: string(it.Scope), Name: it.Name})
}

// SkippedKeptCount reports how many items are hidden by saved decisions.
func (a *Audit) SkippedKeptCount() int {
	if a.Saved == nil {
		return 0
	}
	n := 0
	for _, it := range a.Items {
		if it.Disableable() && a.Saved.IsKept(state.DecisionKey{Kind: string(it.Kind), Scope: string(it.Scope), Name: it.Name}) {
			n++
		}
	}
	return n
}

// Reviewable returns items that take part in the decision flow at all.
func (a *Audit) Reviewable() []*StartupItem {
	var out []*StartupItem
	for _, it := range a.Items {
		if !it.Disableable() {
			continue
		}
		if a.savedKeep(it) {
			continue
		}
		out = append(out, it)
	}
	return out
}

// Protected returns the read-only protected view.
func (a *Audit) Protected() []*StartupItem {
	var out []*StartupItem
	for _, it := range a.Items {
		if it.Protected {
			out = append(out, it)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Informational returns static/masked units shown read-only.
func (a *Audit) Informational() []*StartupItem {
	var out []*StartupItem
	for _, it := range a.Items {
		if !it.Protected && (it.Static || it.Masked) {
			out = append(out, it)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Recompute refreshes dependents, risk, and promotion notes against the
// current queue.
func (a *Audit) Recompute() {
	queued := a.QueuedDisables()
	queuedNames := map[string]string{} // scoped name -> friendly label
	for id := range queued {
		for _, it := range a.Items {
			if it.ID == id {
				queuedNames[string(it.Scope)+"/"+it.Name] = it.DisplayName()
			}
		}
	}
	for _, it := range a.Items {
		prevDeps := it.Dependents
		it.Dependents = a.Graph.ActiveDependents(it, queued)
		it.Risk = a.Graph.RateRisk(it, queued)
		// Promotion note: previously had dependents, now none, and at least
		// one former dependent is queued for disable.
		if len(prevDeps) > 0 && len(it.Dependents) == 0 && it.PromotedBy == "" {
			for _, dep := range prevDeps {
				if label, ok := queuedNames[dep]; ok {
					it.PromotedBy = label
					break
				}
			}
		}
	}
}

// Next returns the next undecided item, leaves-first, resource-impact second;
// skipped items come back at the end. Returns nil when the audit is complete.
func (a *Audit) Next() *StartupItem {
	a.Recompute()
	var leaves, withDeps, skippedItems []*StartupItem
	for _, it := range a.Reviewable() {
		if a.decided(it.ID) {
			continue
		}
		if a.skipped[it.ID] {
			skippedItems = append(skippedItems, it)
			continue
		}
		if len(it.Dependents) == 0 {
			leaves = append(leaves, it)
		} else {
			withDeps = append(withDeps, it)
		}
	}
	impact := func(it *StartupItem) float64 {
		var v float64
		if it.Resources.MemoryBytes > 0 {
			v += float64(it.Resources.MemoryBytes) / (1 << 20) // MB
		}
		v += it.Resources.BootMillis / 10
		return v
	}
	byImpact := func(items []*StartupItem) {
		sort.SliceStable(items, func(i, j int) bool {
			// Promoted items first (they explain themselves), then impact.
			pi, pj := items[i].PromotedBy != "", items[j].PromotedBy != ""
			if pi != pj {
				return pi
			}
			ii, ij := impact(items[i]), impact(items[j])
			if ii != ij {
				return ii > ij
			}
			return items[i].Name < items[j].Name
		})
	}
	byImpact(leaves)
	byImpact(withDeps)
	byImpact(skippedItems)
	if len(leaves) > 0 {
		return leaves[0]
	}
	if len(withDeps) > 0 {
		return withDeps[0]
	}
	if len(skippedItems) > 0 {
		s := skippedItems[0]
		delete(a.skipped, s.ID) // offering it again; another skip re-queues it
		return s
	}
	return nil
}

// Remaining counts undecided reviewable items.
func (a *Audit) Remaining() int {
	n := 0
	for _, it := range a.Reviewable() {
		if !a.decided(it.ID) {
			n++
		}
	}
	return n
}

// Total counts reviewable items.
func (a *Audit) Total() int { return len(a.Reviewable()) }

// Decide records a decision for an item.
func (a *Audit) Decide(it *StartupItem, action ActionType) {
	if action == ActionSkip {
		a.skipped[it.ID] = true
		return
	}
	a.Decisions = append(a.Decisions, Decision{ItemID: it.ID, Action: action})
	if action == ActionKeepForever && a.Saved != nil {
		_ = a.Saved.Keep(state.DecisionKey{Kind: string(it.Kind), Scope: string(it.Scope), Name: it.Name})
	}
	a.Recompute()
}

// UndoLast pops the most recent decision and returns its item id ("" if none).
func (a *Audit) UndoLast() string {
	if len(a.Decisions) == 0 {
		return ""
	}
	last := a.Decisions[len(a.Decisions)-1]
	a.Decisions = a.Decisions[:len(a.Decisions)-1]
	// Clear promotion notes that may no longer hold.
	for _, it := range a.Items {
		it.PromotedBy = ""
	}
	a.Recompute()
	return last.ItemID
}

// ItemByID finds an item.
func (a *Audit) ItemByID(id string) *StartupItem {
	for _, it := range a.Items {
		if it.ID == id {
			return it
		}
	}
	return nil
}

// EstimatedSavings sums memory and boot-time impact of queued disables.
func (a *Audit) EstimatedSavings() (memBytes int64, bootMillis float64) {
	for id := range a.QueuedDisables() {
		if it := a.ItemByID(id); it != nil {
			if it.Resources.MemoryBytes > 0 {
				memBytes += it.Resources.MemoryBytes
			}
			bootMillis += it.Resources.BootMillis
		}
	}
	return
}

// DisplayName returns the friendliest available name.
func (it *StartupItem) DisplayName() string {
	if it.KBName != "" {
		return it.KBName
	}
	if it.FriendlyName != "" {
		return it.FriendlyName
	}
	return it.Name
}
