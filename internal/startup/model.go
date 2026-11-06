// Package startup implements discovery, analysis, and planning for the
// startup audit — the core of sysprune.
package startup

import "fmt"

// Kind of a startup item.
type Kind string

const (
	KindSystemdUnit Kind = "unit"
	KindXDGAutostart Kind = "xdg"
	KindCronReboot  Kind = "cron"
)

// Scope of a startup item.
type Scope string

const (
	ScopeUser   Scope = "user"
	ScopeSystem Scope = "system"
)

// Risk is the three-level safety rating.
type Risk int

const (
	RiskSafe Risk = iota // 🟢 no active/enabled dependents, off critical path
	RiskThink            // 🟡 has dependents or is target/socket/timer-pulled
	RiskProtected        // 🔒 hard-locked, never offered for disabling
)

func (r Risk) String() string {
	switch r {
	case RiskSafe:
		return "safe"
	case RiskThink:
		return "think-twice"
	case RiskProtected:
		return "protected"
	}
	return "unknown"
}

// Recommendation from the knowledge base.
type Recommendation string

const (
	RecKeep    Recommendation = "keep"
	RecDisable Recommendation = "disable"
	RecThink   Recommendation = "think"
)

// Resources holds per-item resource usage data.
type Resources struct {
	MemoryBytes  int64   // MemoryCurrent, -1 if unknown
	CPUUsageNSec int64   // CPUUsageNSec, -1 if unknown
	BootMillis   float64 // activation time from systemd-analyze blame, 0 if unknown
}

// StartupItem is the normalized model for everything that starts automatically.
type StartupItem struct {
	ID          string // "<kind>/<scope>/<name>"
	Kind        Kind
	Scope       Scope
	Name        string // unit name, desktop filename, or cron line label
	FriendlyName string
	Description string
	UnitPath    string // unit file / desktop file path
	Enablement  string // enabled, disabled, static, masked, generated, alias, linked...
	ActiveState string // active, inactive, failed...
	SubState    string
	Exec        string // ExecStart or Exec line
	Resources   Resources
	// Dependency info (unit names / ids)
	Requires   []string
	Wants      []string
	BindsTo    []string
	PartOf     []string
	Upholds    []string
	Requisite  []string
	WantedBy   []string
	RequiredBy []string
	TriggeredBy []string // sockets/timers that activate this service
	Triggers    []string // what this socket/timer activates
	// Analysis results
	Protected       bool
	ProtectedReason string
	Risk            Risk
	KBName          string
	KBDescription   string
	KBCategory      string
	Recommend       Recommendation
	RecommendReason string
	HardwareNote    string
	Static          bool // static units are informational only
	Masked          bool
	// Dependents is computed from the graph: ids of items/units that need
	// this one and are themselves enabled or active.
	Dependents []string
	// PromotedBy is set when this item became a leaf because the named item
	// was queued for disable ("was only needed by X").
	PromotedBy string
	// CronLine holds the raw @reboot line for cron items.
	CronLine string
}

// ItemID builds the canonical id.
func ItemID(kind Kind, scope Scope, name string) string {
	return fmt.Sprintf("%s/%s/%s", kind, scope, name)
}

// Disableable reports whether the item can ever be offered for disabling.
func (it *StartupItem) Disableable() bool {
	return !it.Protected && !it.Static && !it.Masked
}
