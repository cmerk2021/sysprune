package apply

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/cmerk2021/sysprune/internal/journal"
	"github.com/cmerk2021/sysprune/internal/runner"
	"github.com/cmerk2021/sysprune/internal/startup"
)

// LocalApplier applies user-scope changes (user units, XDG autostart, cron)
// directly from the unprivileged process, with full journaling.
type LocalApplier struct {
	R          runner.Runner
	FS         runner.FS
	J          *journal.Writer
	Home       string
	BackupDir  string // ~/.local/state/<app>/backups/<session>/
	CheckDrift bool
}

// ApplyUserPlan applies user-scope systemd unit changes.
func (l *LocalApplier) ApplyUserPlan(ctx context.Context, p *startup.Plan) Result {
	ex := &Executor{R: l.R, FS: l.FS, J: l.J, CheckDrift: l.CheckDrift}
	return ex.Execute(ctx, p)
}

// DisableXDG disables an autostart entry without ever deleting a file:
//   - user entry (under ~/.config/autostart): move it into the backup dir
//   - system entry (/etc/xdg/autostart): write a user override with Hidden=true
func (l *LocalApplier) DisableXDG(it *startup.StartupItem) ChangeResult {
	res := ChangeResult{Change: startup.PlanChange{ItemID: it.ID, Kind: "xdg", Scope: "user", Op: "disable"}}
	userAutostart := filepath.Join(l.Home, ".config", "autostart")
	jop := journal.Operation{ID: it.ID, Kind: "xdg", Scope: "user", SrcPath: it.UnitPath}

	if strings.HasPrefix(it.UnitPath, userAutostart+string(filepath.Separator)) {
		jop.Op = "move-xdg"
		jop.DstPath = filepath.Join(l.BackupDir, it.Name)
		if err := l.FS.MkdirAll(l.BackupDir, 0o700); err != nil {
			res.Message = err.Error()
			return res
		}
		if err := l.J.Intent(jop); err != nil {
			res.Message = "journal write failed; refusing: " + err.Error()
			return res
		}
		if err := l.FS.Rename(it.UnitPath, jop.DstPath); err != nil {
			_ = l.J.Failed(jop, err)
			res.Message = err.Error()
			return res
		}
	} else {
		jop.Op = "hide-xdg"
		jop.DstPath = filepath.Join(userAutostart, it.Name)
		if err := l.FS.MkdirAll(userAutostart, 0o755); err != nil {
			res.Message = err.Error()
			return res
		}
		if err := l.J.Intent(jop); err != nil {
			res.Message = "journal write failed; refusing: " + err.Error()
			return res
		}
		content := fmt.Sprintf("[Desktop Entry]\nType=Application\nName=%s\nHidden=true\n# Created by sysprune on %s to disable the system autostart entry at %s\n",
			it.DisplayName(), time.Now().Format(time.RFC3339), it.UnitPath)
		if err := l.FS.WriteFile(jop.DstPath, []byte(content), 0o644); err != nil {
			_ = l.J.Failed(jop, err)
			res.Message = err.Error()
			return res
		}
	}
	_ = l.J.Done(jop)
	res.OK = true
	return res
}

// cronMarker marks lines commented out by sysprune.
const cronMarker = "# [disabled by sysprune] "

// DisableCron comments out an @reboot crontab line, backing up the entire
// prior crontab first.
func (l *LocalApplier) DisableCron(ctx context.Context, it *startup.StartupItem) ChangeResult {
	res := ChangeResult{Change: startup.PlanChange{ItemID: it.ID, Kind: "cron", Scope: "user", Op: "disable"}}
	out, _, code, err := l.R.Run(ctx, "crontab", "-l")
	if err != nil || code != 0 {
		res.Message = "could not read crontab"
		return res
	}
	backup := filepath.Join(l.BackupDir, "crontab.bak")
	if err := l.FS.MkdirAll(l.BackupDir, 0o700); err != nil {
		res.Message = err.Error()
		return res
	}
	if err := l.FS.WriteFile(backup, out, 0o600); err != nil {
		res.Message = err.Error()
		return res
	}
	lines := strings.Split(string(out), "\n")
	found := false
	for i, line := range lines {
		if line == it.CronLine && strings.HasPrefix(strings.TrimSpace(line), "@reboot") {
			lines[i] = cronMarker + line
			found = true
			break
		}
	}
	if !found {
		res.Skipped = true
		res.Message = "crontab line changed since review; skipped"
		return res
	}
	jop := journal.Operation{ID: it.ID, Kind: "cron", Scope: "user", Op: "comment-cron", BackupPath: backup}
	if err := l.J.Intent(jop); err != nil {
		res.Message = "journal write failed; refusing: " + err.Error()
		return res
	}
	tmp := backup + ".new"
	if err := l.FS.WriteFile(tmp, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		_ = l.J.Failed(jop, err)
		res.Message = err.Error()
		return res
	}
	_, stderr, code2, err := l.R.Run(ctx, "crontab", tmp)
	_ = l.FS.Remove(tmp)
	if err != nil || code2 != 0 {
		e := fmt.Errorf("crontab install failed: %s", strings.TrimSpace(string(stderr)))
		_ = l.J.Failed(jop, e)
		res.Message = e.Error()
		return res
	}
	_ = l.J.Done(jop)
	res.OK = true
	return res
}
