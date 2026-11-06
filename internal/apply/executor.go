package apply

import (
	"context"
	"fmt"
	"strings"

	"github.com/cmerk2021/sysprune/internal/journal"
	"github.com/cmerk2021/sysprune/internal/runner"
	"github.com/cmerk2021/sysprune/internal/startup"
)

// ChangeResult is the per-change outcome reported back to the TUI.
type ChangeResult struct {
	Change  startup.PlanChange `json:"change"`
	OK      bool               `json:"ok"`
	Skipped bool               `json:"skipped"` // drift detected
	Message string             `json:"message,omitempty"`
}

// Result is the structured apply outcome (JSON on stdout in helper mode).
type Result struct {
	SessionID string         `json:"session_id"`
	Results   []ChangeResult `json:"results"`
	JournalPath string       `json:"journal_path"`
}

// Executor applies validated plan changes with write-ahead journaling.
type Executor struct {
	R  runner.Runner
	FS runner.FS
	J  *journal.Writer
	// CheckDrift controls prior-state verification before each unit change.
	CheckDrift bool
}

func toJournalOp(c startup.PlanChange) journal.Operation {
	op := journal.Operation{
		ID:    c.ItemID,
		Kind:  c.Kind,
		Scope: c.Scope,
		Op:    c.Op,
		Unit:  c.Unit,
		Exec:  strings.Join(c.Commands(), " && "),
	}
	op.Prior.Enabled = c.PriorEnabled
	op.Prior.Active = c.PriorActive
	return op
}

// Execute runs every change in order. A failing change is recorded and does
// not abort the rest.
func (e *Executor) Execute(ctx context.Context, p *startup.Plan) Result {
	res := Result{SessionID: p.SessionID, JournalPath: e.J.Path()}
	for _, c := range p.Changes {
		res.Results = append(res.Results, e.executeOne(ctx, c))
	}
	return res
}

func (e *Executor) executeOne(ctx context.Context, c startup.PlanChange) ChangeResult {
	out := ChangeResult{Change: c}
	jop := toJournalOp(c)

	if c.Kind == "unit" && e.CheckDrift {
		sc := runner.Systemctl{R: e.R, User: c.Scope == "user"}
		props, err := sc.Show(ctx, c.Unit, "UnitFileState", "ActiveState", "LoadState")
		if err != nil {
			out.Message = "could not query unit: " + err.Error()
			return out
		}
		if props["LoadState"] == "not-found" {
			out.Skipped = true
			out.Message = "unit no longer exists; skipped"
			return out
		}
		if c.PriorEnabled != "" && props["UnitFileState"] != c.PriorEnabled {
			out.Skipped = true
			out.Message = fmt.Sprintf("state drifted (expected %s, found %s); skipped", c.PriorEnabled, props["UnitFileState"])
			return out
		}
		if c.Op == "stop" && props["ActiveState"] != "active" && props["ActiveState"] != "activating" {
			out.Skipped = true
			out.Message = "unit is no longer active; stop skipped"
			return out
		}
	}

	// Write-ahead: the intent is journaled BEFORE execution.
	if err := e.J.Intent(jop); err != nil {
		out.Message = "journal write failed; refusing to apply: " + err.Error()
		return out
	}

	var err error
	switch c.Kind {
	case "unit":
		sc := runner.Systemctl{R: e.R, User: c.Scope == "user"}
		switch c.Op {
		case "disable":
			err = sc.Disable(ctx, c.Unit)
		case "enable":
			err = sc.Enable(ctx, c.Unit)
		case "stop":
			err = sc.Stop(ctx, c.Unit)
		case "start":
			err = sc.Start(ctx, c.Unit)
		case "mask":
			err = sc.Mask(ctx, c.Unit)
		case "unmask":
			err = sc.Unmask(ctx, c.Unit)
		default:
			err = fmt.Errorf("op %q not allowed", c.Op)
		}
	case "clean":
		err = e.runClean(ctx, c)
	default:
		err = fmt.Errorf("kind %q not allowed", c.Kind)
	}

	if err != nil {
		_ = e.J.Failed(jop, err)
		out.Message = err.Error()
		return out
	}
	_ = e.J.Done(jop)
	out.OK = true
	return out
}

func (e *Executor) runClean(ctx context.Context, c startup.PlanChange) error {
	run := func(name string, args ...string) error {
		_, stderr, code, err := e.R.Run(ctx, name, args...)
		if err != nil {
			return err
		}
		if code != 0 {
			return fmt.Errorf("%s exited %d: %s", name, code, strings.TrimSpace(string(stderr)))
		}
		return nil
	}
	switch c.Op {
	case "vacuum-journal":
		if strings.HasSuffix(c.Param, "d") {
			return run("journalctl", "--vacuum-time="+c.Param)
		}
		return run("journalctl", "--vacuum-size="+c.Param)
	case "apt-clean":
		return run("apt-get", "clean")
	case "dnf-clean":
		return run("dnf", "clean", "all")
	case "pacman-clean":
		return run("pacman", "-Sc", "--noconfirm")
	case "flatpak-unused":
		return run("flatpak", "uninstall", "--unused", "--noninteractive")
	case "docker-prune":
		return run("docker", "system", "prune", "-f")
	case "crash-clean":
		entries, err := e.FS.ReadDir("/var/crash")
		if err != nil {
			return nil // nothing to clean
		}
		for _, ent := range entries {
			if err := e.FS.RemoveAll("/var/crash/" + ent.Name()); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("cleanup op %q not allowed", c.Op)
}
