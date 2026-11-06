package startup

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// PlanVersion guards against format drift between the TUI and helper.
const PlanVersion = 1

// PlanChange is a single operation in an apply plan. The privileged helper
// re-validates every field and trusts nothing.
type PlanChange struct {
	ItemID string `json:"item_id"`
	Kind   string `json:"kind"`  // unit | clean
	Scope  string `json:"scope"` // user | system
	Op     string `json:"op"`    // disable, enable, stop, start, mask, unmask | vacuum-journal, apt-clean, dnf-clean, pacman-clean, flatpak-unused, docker-prune, crash-clean
	Unit   string `json:"unit,omitempty"`
	// Prior state recorded at review time; the helper checks for drift.
	PriorEnabled string `json:"prior_enabled,omitempty"`
	PriorActive  string `json:"prior_active,omitempty"`
	// Param carries a validated parameter for storage ops (e.g. "500M" or
	// "14d" for vacuum-journal). Strictly validated by the helper.
	Param string `json:"param,omitempty"`
}

// Plan is the JSON document passed to the privileged helper.
type Plan struct {
	Version   int          `json:"version"`
	SessionID string       `json:"session_id"`
	Changes   []PlanChange `json:"changes"`
}

func (p *Plan) Marshal() ([]byte, error) { return json.MarshalIndent(p, "", "  ") }

// Commands renders the plan as human-readable commands (for dry-run and the
// "run this yourself" fallback).
func (c PlanChange) Commands() []string {
	userFlag := ""
	if c.Scope == "user" {
		userFlag = "--user "
	}
	switch c.Kind {
	case "unit":
		switch c.Op {
		case "disable", "enable", "stop", "start", "mask", "unmask":
			return []string{fmt.Sprintf("systemctl %s%s -- %s", userFlag, c.Op, c.Unit)}
		}
	case "clean":
		switch c.Op {
		case "vacuum-journal":
			if strings.HasSuffix(c.Param, "d") {
				return []string{"journalctl --vacuum-time=" + c.Param}
			}
			return []string{"journalctl --vacuum-size=" + c.Param}
		case "apt-clean":
			return []string{"apt-get clean"}
		case "dnf-clean":
			return []string{"dnf clean all"}
		case "pacman-clean":
			return []string{"pacman -Sc --noconfirm"}
		case "flatpak-unused":
			return []string{"flatpak uninstall --unused --noninteractive"}
		case "docker-prune":
			return []string{"docker system prune -f"}
		case "crash-clean":
			return []string{"rm -rf /var/crash/* (performed natively by the helper)"}
		}
	}
	return []string{fmt.Sprintf("(unknown op %s/%s)", c.Kind, c.Op)}
}

// BuildPlans converts queued audit decisions into three groups:
//   - userPlan: user-scope systemd unit changes (applied unprivileged)
//   - systemPlan: system-scope unit changes (applied via the helper)
//   - localOps: XDG/cron changes applied directly by the user process
func BuildPlans(a *Audit, sessionID string) (userPlan, systemPlan *Plan, localOps []Decision) {
	userPlan = &Plan{Version: PlanVersion, SessionID: sessionID}
	systemPlan = &Plan{Version: PlanVersion, SessionID: sessionID}
	for _, d := range a.Decisions {
		it := a.ItemByID(d.ItemID)
		if it == nil {
			continue
		}
		switch d.Action {
		case ActionDisable, ActionDisableStop, ActionMask:
		default:
			continue
		}
		if it.Kind != KindSystemdUnit {
			localOps = append(localOps, d)
			continue
		}
		mk := func(op string) PlanChange {
			return PlanChange{
				ItemID:       it.ID,
				Kind:         "unit",
				Scope:        string(it.Scope),
				Op:           op,
				Unit:         it.Name,
				PriorEnabled: it.Enablement,
				PriorActive:  it.ActiveState,
			}
		}
		var changes []PlanChange
		switch d.Action {
		case ActionDisable:
			changes = []PlanChange{mk("disable")}
		case ActionDisableStop:
			changes = []PlanChange{mk("disable"), mk("stop")}
		case ActionMask:
			changes = []PlanChange{mk("disable"), mk("mask")}
		}
		if it.Scope == ScopeSystem {
			systemPlan.Changes = append(systemPlan.Changes, changes...)
		} else {
			userPlan.Changes = append(userPlan.Changes, changes...)
		}
	}
	return
}

// StopBlocker describes why "disable and stop now" is risky for an item.
type StopBlocker struct {
	Blocked bool
	// Users lists what is actively using the item (scoped names or notes).
	Users []string
	// Suggestion names the item to handle first.
	Suggestion string
}

// CheckStopNow evaluates whether stopping an item right now is safe.
func (a *Audit) CheckStopNow(it *StartupItem) StopBlocker {
	queued := a.QueuedDisables()
	var users []string
	for _, dep := range a.Graph.ActiveDependents(it, queued) {
		// Only count dependents that are currently ACTIVE (not merely
		// enabled) as stop blockers.
		parts := strings.SplitN(dep, "/", 2)
		if len(parts) == 2 {
			if d := a.Graph.Item(Scope(parts[0]), parts[1]); d != nil {
				if d.ActiveState == "active" || d.ActiveState == "activating" {
					users = append(users, d.DisplayName()+" ("+d.Name+")")
				}
			}
		}
	}
	// A socket/timer that still triggers this service may poke it back awake.
	for _, trig := range it.TriggeredBy {
		if t := a.Graph.Item(it.Scope, trig); t != nil && !queued[t.ID] &&
			(t.ActiveState == "active" || t.Enablement == "enabled") {
			users = append(users, t.DisplayName()+" ("+trig+", activates this service)")
		}
	}
	sort.Strings(users)
	b := StopBlocker{Blocked: len(users) > 0, Users: users}
	if len(users) > 0 {
		b.Suggestion = "Disable " + users[0] + " first, or choose \"Disable at next boot\" instead."
	}
	return b
}
