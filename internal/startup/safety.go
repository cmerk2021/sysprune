package startup

import (
	"strings"
)

// ProtectionContext carries dynamic facts about the running system used for
// dynamic protection rules.
type ProtectionContext struct {
	// OverSSH is true when the current session is an SSH session
	// (SSH_CONNECTION / SSH_TTY set).
	OverSSH bool
	// DisplayManagerBinary is the resolved ExecStart binary of the active
	// display manager / compositor, if known.
	DisplayManagerBinary string
	// CriticalChain is the set of unit names in default.target's critical
	// chain (units RequiredBy that path).
	CriticalChain map[string]bool
	// NetworkOnlineProviders is the set of units that provide/are wanted by
	// network-online.target.
	NetworkOnlineProviders map[string]bool
	// SessionUnits is the set of units backing the current login session
	// (e.g. the user's session scope, the running compositor, the unit the
	// app itself runs in).
	SessionUnits map[string]bool
	// RootFSUnits is the set of mount/service units backing the root
	// filesystem.
	RootFSUnits map[string]bool
	// FirewallOverride is true when the user explicitly took the advanced
	// path to manage firewall units. Even then a heavy warning is shown.
	FirewallOverride bool
}

// Protection is the result of a protection check.
type Protection struct {
	Protected bool
	Reason    string
	// Warn is a strong warning to display even when not protected (e.g.
	// firewall with explicit override).
	Warn string
}

// CheckProtection applies Layer 1 of the safety model: the curated protection
// list plus dynamic protections. unitName is the bare unit/desktop-file name;
// execPath is the item's resolved ExecStart binary ("" if unknown).
func (kb *KB) CheckProtection(unitName, execPath string, pc ProtectionContext) Protection {
	base := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(unitName, ".service"), ".timer"), ".socket")

	for _, p := range kb.Protected {
		if !globMatch(p.Pattern, unitName) && !globMatch(p.Pattern, base) {
			continue
		}
		switch p.Conditional {
		case "ssh":
			if pc.OverSSH {
				return Protection{Protected: true, Reason: p.Reason + " You are connected over SSH right now."}
			}
			// Not over SSH: still protect, but with an explanatory reason —
			// losing sshd on a machine you later need remotely is a trap.
			return Protection{Protected: true, Reason: p.Reason}
		case "firewall":
			if pc.FirewallOverride {
				return Protection{Protected: false, Warn: "⚠ This is your firewall. Disabling it exposes every listening service to the network. Only continue if you are absolutely sure."}
			}
			return Protection{Protected: true, Reason: p.Reason}
		default:
			return Protection{Protected: true, Reason: p.Reason}
		}
	}

	// Dynamic protections.
	if pc.SessionUnits[unitName] {
		return Protection{Protected: true, Reason: "This unit backs your current login session."}
	}
	if pc.CriticalChain[unitName] {
		return Protection{Protected: true, Reason: "This unit is required by the boot path to default.target."}
	}
	if pc.NetworkOnlineProviders[unitName] {
		return Protection{Protected: true, Reason: "This unit provides network-online.target — other services wait for it to bring the network up."}
	}
	if pc.RootFSUnits[unitName] {
		return Protection{Protected: true, Reason: "This unit backs the root filesystem mount."}
	}
	if execPath != "" && pc.DisplayManagerBinary != "" && execPath == pc.DisplayManagerBinary {
		return Protection{Protected: true, Reason: "This runs your current display manager / compositor."}
	}
	return Protection{}
}
