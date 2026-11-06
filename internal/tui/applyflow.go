package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cmerk2021/sysprune/internal/apply"
	"github.com/cmerk2021/sysprune/internal/elevate"
	"github.com/cmerk2021/sysprune/internal/journal"
	"github.com/cmerk2021/sysprune/internal/runner"
	"github.com/cmerk2021/sysprune/internal/startup"
)

// ---- Summary / final review ----

type summaryModel struct {
	userPlan   *startup.Plan
	systemPlan *startup.Plan
	localOps   []startup.Decision
	sessionID  string
	warnings   []string
	built      bool
}

func (m *Model) buildSummary() {
	sm := &m.summary
	sm.sessionID = journal.NewSessionID(time.Now())
	sm.userPlan, sm.systemPlan, sm.localOps = startup.BuildPlans(m.review.audit, sm.sessionID)
	sm.warnings = nil
	// Re-validate the whole batch against the protection list.
	for _, p := range []*startup.Plan{sm.userPlan, sm.systemPlan} {
		for _, c := range p.Changes {
			if err := apply.ValidateChange(c, m.svc.KB); err != nil {
				sm.warnings = append(sm.warnings, err.Error())
			}
		}
	}
	sm.built = true
}

func (m *Model) updateSummary(msg tea.Msg) (tea.Model, tea.Cmd) {
	if !m.summary.built {
		m.buildSummary()
	}
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "b":
			m.summary.built = false
			m.screen = ScreenReview
			if m.review.current == nil {
				m.review.advance()
			}
		case "q":
			if m.totalQueued() > 0 && !m.confirmQuit {
				m.confirmQuit = true
				return m, nil
			}
			m.cancel()
			return m, tea.Quit
		case "y", "enter":
			m.confirmQuit = false
			if m.totalQueued() == 0 {
				m.screen = ScreenMenu
				m.summary.built = false
				return m, nil
			}
			if len(m.summary.warnings) > 0 {
				return m, nil // refuse to apply with validation warnings
			}
			if m.svc.DryRun {
				m.DryRunOutput = m.renderDryRun()
				m.cancel()
				return m, tea.Quit
			}
			m.screen = ScreenResult // result screen receives localAppliedMsg
			return m, m.applyLocalCmd()
		default:
			m.confirmQuit = false
		}
	}
	return m, nil
}

func (m *Model) totalQueued() int {
	return len(m.summary.userPlan.Changes) + len(m.summary.systemPlan.Changes) + len(m.summary.localOps)
}

func (m *Model) renderDryRun() string {
	var b strings.Builder
	b.WriteString("DRY RUN — nothing was changed. The following WOULD be executed:\n\n")
	if len(m.summary.userPlan.Changes) > 0 {
		b.WriteString("User scope (run as you):\n")
		for _, c := range m.summary.userPlan.Changes {
			for _, cmd := range c.Commands() {
				b.WriteString("  " + cmd + "\n")
			}
		}
	}
	for _, d := range m.summary.localOps {
		if it := m.review.audit.ItemByID(d.ItemID); it != nil {
			switch it.Kind {
			case startup.KindXDGAutostart:
				b.WriteString(fmt.Sprintf("  (move or hide autostart entry %s — no files deleted)\n", it.UnitPath))
			case startup.KindCronReboot:
				b.WriteString(fmt.Sprintf("  (comment out crontab line: %s)\n", strings.TrimSpace(it.CronLine)))
			}
		}
	}
	if len(m.summary.systemPlan.Changes) > 0 {
		b.WriteString("\nSystem scope (would run as root via one escalation):\n")
		for _, c := range m.summary.systemPlan.Changes {
			for _, cmd := range c.Commands() {
				b.WriteString("  " + cmd + "\n")
			}
		}
		if data, err := m.summary.systemPlan.Marshal(); err == nil {
			b.WriteString("\nPrivileged helper plan (JSON):\n" + string(data) + "\n")
		}
	}
	return b.String()
}

func (m *Model) viewSummary() string {
	s := m.styles
	if !m.summary.built {
		m.buildSummary()
	}
	var b strings.Builder
	b.WriteString(s.Title.Render("Review queued changes") + "\n\n")
	if m.totalQueued() == 0 {
		b.WriteString(s.Dim.Render("No changes queued.") + "\n\n")
		if m.review.audit != nil && m.review.audit.Remaining() > 0 {
			b.WriteString(s.Dim.Render(fmt.Sprintf("%d items still to review.", m.review.audit.Remaining())) + "\n\n")
		}
		b.WriteString(s.Key.Render("b") + " back to review · " + s.Key.Render("enter") + " main menu · " + s.Key.Render("q") + " quit")
		return b.String()
	}
	if len(m.summary.userPlan.Changes) > 0 || len(m.summary.localOps) > 0 {
		b.WriteString(s.Bold.Render("User scope") + s.Dim.Render(" (applied as you, no password)") + "\n")
		for _, c := range m.summary.userPlan.Changes {
			b.WriteString("  • " + strings.Join(c.Commands(), " && ") + "\n")
		}
		for _, d := range m.summary.localOps {
			if it := m.review.audit.ItemByID(d.ItemID); it != nil {
				b.WriteString("  • " + it.DisplayName() + s.Dim.Render(" ("+string(it.Kind)+", reversible)") + "\n")
			}
		}
		b.WriteString("\n")
	}
	if len(m.summary.systemPlan.Changes) > 0 {
		b.WriteString(s.Bold.Render("System scope") + s.Warn.Render(" (will run as root)") + "\n")
		for _, c := range m.summary.systemPlan.Changes {
			b.WriteString("  • " + strings.Join(c.Commands(), " && ") + "\n")
		}
		b.WriteString("\n")
	}
	for _, w := range m.summary.warnings {
		b.WriteString(s.Err.Render("⚠ "+w) + "\n")
	}
	if m.review.audit != nil && m.review.audit.Remaining() > 0 {
		b.WriteString(s.Dim.Render(fmt.Sprintf("%d items not reviewed yet — you can apply now and continue later.", m.review.audit.Remaining())) + "\n")
	}
	b.WriteString(s.Dim.Render("Session "+m.summary.sessionID+" · undo later with --restore") + "\n\n")
	if m.confirmQuit {
		b.WriteString(s.Warn.Render("Quit and discard queued changes? q again confirms, any other key cancels.") + "\n")
	}
	verb := "apply"
	if m.svc.DryRun {
		verb = "print (dry run)"
	}
	b.WriteString(s.Key.Render("y") + "/" + s.Key.Render("enter") + " " + verb + " · " + s.Key.Render("b") + " back · " + s.Key.Render("q") + " quit")
	return b.String()
}

// ---- Apply (user scope first, then optional single escalation) ----

type localAppliedMsg struct {
	results []apply.ChangeResult
	journal string
	err     error
}

func (m *Model) applyLocalCmd() tea.Cmd {
	svc := m.svc
	sm := m.summary
	audit := m.review.audit
	ctx := m.ctx
	return func() tea.Msg {
		w, err := journal.NewWriter(svc.FS, svc.Paths.Journal, sm.sessionID)
		if err != nil {
			return localAppliedMsg{err: err}
		}
		_ = w.Meta("apply start " + time.Now().UTC().Format(time.RFC3339))
		var results []apply.ChangeResult

		la := &apply.LocalApplier{
			R: svc.R, FS: svc.FS, J: w, Home: svc.Home,
			BackupDir:  filepath.Join(svc.Paths.Backups, sm.sessionID),
			CheckDrift: true,
		}
		if len(sm.userPlan.Changes) > 0 {
			res := la.ApplyUserPlan(ctx, sm.userPlan)
			results = append(results, res.Results...)
		}
		for _, d := range sm.localOps {
			it := audit.ItemByID(d.ItemID)
			if it == nil {
				continue
			}
			switch it.Kind {
			case startup.KindXDGAutostart:
				results = append(results, la.DisableXDG(it))
			case startup.KindCronReboot:
				results = append(results, la.DisableCron(ctx, it))
			}
		}
		return localAppliedMsg{results: results, journal: w.Path()}
	}
}

// ---- Elevation ----

type elevateModel struct {
	method   elevate.Method
	selfPath string
	planPath string
	command  string
	manual   string
}

type elevatedMsg struct {
	results []apply.ChangeResult
	err     error
}

func (m *Model) prepareElevation() error {
	return m.prepareElevationForPlan(m.summary.systemPlan, m.summary.sessionID)
}

// prepareElevationForPlan sets up the single escalation for any system-scope
// plan (startup changes or storage cleanups).
func (m *Model) prepareElevationForPlan(plan *startup.Plan, sessionID string) error {
	em := &m.elevate
	self, err := elevate.SelfPath()
	if err != nil {
		return err
	}
	em.selfPath = self
	em.method = elevate.Detect(m.svc.LookPath)
	// Save the plan file (0600, owned by us) under the state dir.
	data, err := plan.Marshal()
	if err != nil {
		return err
	}
	em.planPath = filepath.Join(m.svc.Paths.StateDir, "plans", sessionID+".json")
	if err := m.svc.FS.MkdirAll(filepath.Dir(em.planPath), 0o700); err != nil {
		return err
	}
	if err := m.svc.FS.WriteFile(em.planPath, data, 0o600); err != nil {
		return err
	}
	em.command = elevate.Describe(em.method, self, em.planPath)
	if em.method == elevate.MethodNone {
		em.manual = elevate.ManualInstructions(self, em.planPath)
	}
	return nil
}

func (m *Model) updateElevate(msg tea.Msg) (tea.Model, tea.Cmd) {
	em := &m.elevate
	switch msg := msg.(type) {
	case elevatedMsg:
		m.result.system = msg.results
		if msg.err != nil {
			m.result.systemErr = msg.err.Error()
		}
		m.screen = ScreenResult
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "y", "enter":
			if em.method == elevate.MethodNone {
				m.result.systemErr = "not applied — manual command printed above"
				m.screen = ScreenResult
				return m, nil
			}
			return m, m.runElevationCmd()
		case "esc", "n", "q":
			m.result.systemErr = "system-scope changes were not applied (you cancelled). Plan saved at " + em.planPath
			m.screen = ScreenResult
			return m, nil
		}
	}
	return m, nil
}

// runElevationCmd hands the terminal to sudo/pkexec for the password prompt,
// capturing the helper's stdout (JSON result).
func (m *Model) runElevationCmd() tea.Cmd {
	em := &m.elevate
	argv := elevate.Command(em.method, em.selfPath, em.planPath)
	c := runner.Command(argv[0], argv[1:]...)
	var outBuf strings.Builder
	c.Stdout = &outBuf
	c.Stdin = os.Stdin
	c.Stderr = os.Stderr
	return tea.ExecProcess(c, func(err error) tea.Msg {
		var res apply.Result
		if jerr := json.Unmarshal([]byte(strings.TrimSpace(outBuf.String())), &res); jerr == nil {
			return elevatedMsg{results: res.Results, err: err}
		}
		if err == nil {
			err = fmt.Errorf("helper produced no parseable result")
		}
		return elevatedMsg{err: err}
	})
}

func (m *Model) viewElevate() string {
	s := m.styles
	em := &m.elevate
	var b strings.Builder
	b.WriteString(s.Title.Render("System changes — one-time elevation") + "\n\n")
	b.WriteString(wrap("Your user-scope changes are applied. The remaining system-scope changes need root. Exactly this command will run:", m.width-4) + "\n\n")
	b.WriteString("  " + s.Bold.Render(em.command) + "\n\n")
	b.WriteString(wrap("The helper re-validates the plan independently (allowlisted operations only, protected units always refused) and journals every step before executing it.", m.width-4) + "\n\n")
	if em.method == elevate.MethodNone {
		b.WriteString(s.Warn.Render(em.manual) + "\n")
		b.WriteString(s.Key.Render("enter") + " continue · " + s.Key.Render("esc") + " cancel")
	} else {
		b.WriteString(s.Key.Render("y") + " run it (asks for your password) · " + s.Key.Render("esc") + " skip system changes")
	}
	return b.String()
}

// ---- Result ----

type resultModel struct {
	local     []apply.ChangeResult
	system    []apply.ChangeResult
	localErr  string
	systemErr string
	journal   string
	sessionID string
}

func (m *Model) updateResult(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case localAppliedMsg:
		m.result.local = msg.results
		m.result.journal = msg.journal
		m.result.sessionID = m.summary.sessionID
		if msg.err != nil {
			m.result.localErr = msg.err.Error()
		}
		if len(m.summary.systemPlan.Changes) > 0 {
			if err := m.prepareElevation(); err != nil {
				m.result.systemErr = err.Error()
				return m, nil
			}
			m.screen = ScreenElevate
		}
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "enter":
			m.summary.built = false
			m.review = reviewModel{}
			m.screen = ScreenMenu
		}
	}
	return m, nil
}

func (m *Model) viewResult() string {
	s := m.styles
	r := &m.result
	var b strings.Builder
	b.WriteString(s.Title.Render("Done") + "\n\n")
	render := func(results []apply.ChangeResult) {
		for _, cr := range results {
			label := cr.Change.Unit
			if label == "" {
				label = cr.Change.ItemID
			}
			switch {
			case cr.OK:
				b.WriteString("  " + s.Good.Render("✓") + " " + label + " " + s.Dim.Render(cr.Change.Op) + "\n")
			case cr.Skipped:
				b.WriteString("  " + s.Warn.Render("~") + " " + label + " " + s.Dim.Render(cr.Message) + "\n")
			default:
				b.WriteString("  " + s.Err.Render("✗") + " " + label + " " + cr.Message + "\n")
			}
		}
	}
	if len(r.local) > 0 {
		b.WriteString(s.Bold.Render("User scope") + "\n")
		render(r.local)
	}
	if r.localErr != "" {
		b.WriteString(s.Err.Render("  " + r.localErr) + "\n")
	}
	if len(r.system) > 0 {
		b.WriteString(s.Bold.Render("System scope") + "\n")
		render(r.system)
	}
	if r.systemErr != "" {
		b.WriteString(s.Warn.Render("  "+r.systemErr) + "\n")
	}
	b.WriteString("\n" + s.Bold.Render("Session: ") + r.sessionID + "\n")
	b.WriteString(wrap("To undo everything from this session later, run: "+m.svc.AppName+" --restore "+r.sessionID, m.width-4) + "\n\n")
	b.WriteString(s.Key.Render("enter") + " back to menu")
	return b.String()
}
