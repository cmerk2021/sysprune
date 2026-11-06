// Package apply validates and executes plans. The --apply-helper entry point
// is a security boundary: it re-validates everything and trusts nothing in
// the plan file.
package apply

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
	"syscall"

	"github.com/cmerk2021/sysprune/internal/startup"
	"github.com/cmerk2021/sysprune/internal/runner"
)

// MaxPlanSize is the plan file size limit (1 MiB).
const MaxPlanSize = 1 << 20

// unitRe is the strict unit-name allowlist: no path separators, no leading
// dash, limited charset, known suffixes only.
var unitRe = regexp.MustCompile(`^[A-Za-z0-9:_.@\-]+\.(service|timer|socket)$`)

// vacuumSizeRe / vacuumTimeRe validate journal vacuum parameters.
var vacuumParamRe = regexp.MustCompile(`^[0-9]{1,6}([KMG]|d)$`)

// unitOps is the fixed operation allowlist for unit changes.
var unitOps = map[string]bool{
	"disable": true, "enable": true, "stop": true,
	"start": true, "mask": true, "unmask": true,
}

// cleanOps is the fixed allowlist for system cleanup operations.
var cleanOps = map[string]bool{
	"vacuum-journal": true, "apt-clean": true, "dnf-clean": true,
	"pacman-clean": true, "flatpak-unused": true, "docker-prune": true,
	"crash-clean": true,
}

// ValidateUnitName checks a unit name against the strict allowlist regex.
func ValidateUnitName(name string) error {
	if !unitRe.MatchString(name) {
		return fmt.Errorf("invalid unit name %q", name)
	}
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("unit name %q may not start with a dash", name)
	}
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("unit name %q may not contain path separators", name)
	}
	return nil
}

// ValidateChange validates a single plan change structurally (no system
// queries). KB protection is re-checked here too.
func ValidateChange(c startup.PlanChange, kb *startup.KB) error {
	if c.Scope != "user" && c.Scope != "system" {
		return fmt.Errorf("invalid scope %q", c.Scope)
	}
	switch c.Kind {
	case "unit":
		if !unitOps[c.Op] {
			return fmt.Errorf("operation %q is not allowed", c.Op)
		}
		if err := ValidateUnitName(c.Unit); err != nil {
			return err
		}
		if kb != nil {
			// Protection re-check with maximum conservatism: no dynamic
			// context, firewall override NOT honored here.
			prot := kb.CheckProtection(c.Unit, "", startup.ProtectionContext{OverSSH: true})
			if prot.Protected {
				return fmt.Errorf("unit %q is protected: %s", c.Unit, prot.Reason)
			}
		}
	case "clean":
		if !cleanOps[c.Op] {
			return fmt.Errorf("cleanup operation %q is not allowed", c.Op)
		}
		if c.Op == "vacuum-journal" {
			if !vacuumParamRe.MatchString(c.Param) {
				return fmt.Errorf("invalid vacuum parameter %q", c.Param)
			}
		} else if c.Param != "" {
			return fmt.Errorf("operation %q takes no parameter", c.Op)
		}
	default:
		return fmt.Errorf("invalid kind %q", c.Kind)
	}
	return nil
}

// ValidatePlan validates the whole document structurally.
func ValidatePlan(p *startup.Plan, kb *startup.KB) error {
	if p.Version != startup.PlanVersion {
		return fmt.Errorf("unsupported plan version %d", p.Version)
	}
	if p.SessionID == "" || len(p.SessionID) > 64 || strings.ContainsAny(p.SessionID, "/\\ \t\n") {
		return fmt.Errorf("invalid session id")
	}
	if len(p.Changes) == 0 {
		return errors.New("plan contains no changes")
	}
	if len(p.Changes) > 1000 {
		return errors.New("plan too large")
	}
	for i, c := range p.Changes {
		if err := ValidateChange(c, kb); err != nil {
			return fmt.Errorf("change %d: %w", i, err)
		}
	}
	return nil
}

// ReadPlanFile safely reads a plan file for the privileged helper:
// regular file (no symlink), owned by invokingUID, not group/world-writable,
// under the size limit.
func ReadPlanFile(fsys runner.FS, path string, invokingUID int) (*startup.Plan, error) {
	info, err := fsys.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("plan file %s is a symlink; refusing", path)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("plan file %s is not a regular file", path)
	}
	if info.Size() > MaxPlanSize {
		return nil, fmt.Errorf("plan file %s exceeds size limit", path)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("plan file %s is group or world writable; refusing", path)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		if int(st.Uid) != invokingUID {
			return nil, fmt.Errorf("plan file %s is not owned by the invoking user (uid %d)", path, invokingUID)
		}
	}
	data, err := fsys.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p startup.Plan
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("plan file %s: invalid JSON: %w", path, err)
	}
	return &p, nil
}
