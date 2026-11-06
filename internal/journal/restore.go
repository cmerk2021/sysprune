package journal

import (
	"context"
	"fmt"
	"strings"

	"github.com/cmerk2021/sysprune/internal/runner"
)

// RestoreResult reports the outcome of restoring one operation.
type RestoreResult struct {
	Op      Operation
	Action  string // human-readable description of what was done
	Err     error
	Skipped bool // non-restorable (e.g. permanent deletion)
}

// Restorer reverses completed operations using recorded prior state.
type Restorer struct {
	R  runner.Runner
	FS runner.FS
}

// NeedsRoot reports whether any completed operation in the session requires
// root to reverse.
func NeedsRoot(s *Session) bool {
	for _, op := range s.Completed() {
		if op.Scope == "system" && (op.Kind == "unit" || op.Kind == "clean") {
			return true
		}
	}
	return false
}

// Restore reverses each completed operation in reverse order. Failures are
// reported per-operation and do not abort the rest.
func (r *Restorer) Restore(ctx context.Context, s *Session) []RestoreResult {
	ops := s.Completed()
	results := make([]RestoreResult, 0, len(ops))
	for i := len(ops) - 1; i >= 0; i-- {
		results = append(results, r.restoreOne(ctx, ops[i]))
	}
	return results
}

func (r *Restorer) restoreOne(ctx context.Context, op Operation) RestoreResult {
	res := RestoreResult{Op: op}
	switch op.Kind {
	case "unit":
		res.Action, res.Err = r.restoreUnit(ctx, op)
	case "xdg":
		res.Action, res.Err = r.restoreXDG(op)
	case "cron":
		res.Action, res.Err = r.restoreCron(ctx, op)
	case "trash":
		res.Action, res.Err = r.restoreTrash(op)
	case "delete", "clean":
		res.Skipped = true
		res.Action = "permanent deletion / cache clean — not restorable"
	default:
		res.Skipped = true
		res.Action = "unknown operation kind — skipped"
	}
	return res
}

func (r *Restorer) restoreUnit(ctx context.Context, op Operation) (string, error) {
	sc := runner.Systemctl{R: r.R, User: op.Scope == "user"}
	switch op.Op {
	case "disable":
		if op.Prior.Enabled == "enabled" {
			if err := sc.Enable(ctx, op.Unit); err != nil {
				return "", err
			}
			return "re-enabled " + op.Unit, nil
		}
		return "no prior enabled state; left as-is", nil
	case "stop":
		if op.Prior.Active == "active" {
			if err := sc.Start(ctx, op.Unit); err != nil {
				return "", err
			}
			return "restarted " + op.Unit, nil
		}
		return "was not active before; left stopped", nil
	case "mask":
		if err := sc.Unmask(ctx, op.Unit); err != nil {
			return "", err
		}
		if op.Prior.Enabled == "enabled" {
			if err := sc.Enable(ctx, op.Unit); err != nil {
				return "", err
			}
		}
		return "unmasked " + op.Unit, nil
	case "enable":
		if op.Prior.Enabled == "disabled" {
			if err := sc.Disable(ctx, op.Unit); err != nil {
				return "", err
			}
			return "re-disabled " + op.Unit, nil
		}
		return "no prior disabled state; left as-is", nil
	case "start":
		if op.Prior.Active != "active" {
			if err := sc.Stop(ctx, op.Unit); err != nil {
				return "", err
			}
			return "stopped " + op.Unit, nil
		}
		return "was already active before; left running", nil
	}
	return "", fmt.Errorf("unknown unit op %q", op.Op)
}

func (r *Restorer) restoreXDG(op Operation) (string, error) {
	switch op.Op {
	case "move-xdg":
		// File was moved SrcPath -> DstPath (backup). Move it back.
		if err := r.FS.Rename(op.DstPath, op.SrcPath); err != nil {
			return "", err
		}
		return "restored " + op.SrcPath, nil
	case "hide-xdg":
		// An override file with Hidden=true was created at DstPath.
		if err := r.FS.Remove(op.DstPath); err != nil {
			return "", err
		}
		return "removed override " + op.DstPath, nil
	}
	return "", fmt.Errorf("unknown xdg op %q", op.Op)
}

func (r *Restorer) restoreCron(ctx context.Context, op Operation) (string, error) {
	// The full prior crontab was backed up to BackupPath; reinstall it.
	data, err := r.FS.ReadFile(op.BackupPath)
	if err != nil {
		return "", fmt.Errorf("crontab backup missing: %w", err)
	}
	// crontab <file> — write backup to a temp path and install it.
	tmp := op.BackupPath + ".restore"
	if err := r.FS.WriteFile(tmp, data, 0o600); err != nil {
		return "", err
	}
	_, stderr, code, err := r.R.Run(ctx, "crontab", tmp)
	_ = r.FS.Remove(tmp)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("crontab restore failed: %s", strings.TrimSpace(string(stderr)))
	}
	return "restored crontab from backup", nil
}

func (r *Restorer) restoreTrash(op Operation) (string, error) {
	// File was trashed SrcPath -> DstPath (Trash/files/...). Move back and
	// remove the .trashinfo.
	if _, err := r.FS.Lstat(op.SrcPath); err == nil {
		return "", fmt.Errorf("original path %s exists; not overwriting", op.SrcPath)
	}
	if err := r.FS.Rename(op.DstPath, op.SrcPath); err != nil {
		return "", err
	}
	info := strings.Replace(op.DstPath, "/files/", "/info/", 1) + ".trashinfo"
	_ = r.FS.Remove(info)
	return "restored " + op.SrcPath + " from Trash", nil
}
