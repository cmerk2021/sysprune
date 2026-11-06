package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/cmerk2021/sysprune/internal/startup"
	"github.com/cmerk2021/sysprune/internal/storage"
)

// reviewModel drives the card-style startup review.
type reviewModel struct {
	styles     Styles
	audit      *startup.Audit
	withSystem bool
	current    *startup.StartupItem
	sideScroll int
	// pending holds an action awaiting confirmation (stop warning, mask).
	pending      startup.ActionType
	pendingWarn  []string
	maskConfirm  bool
	actionZones  []zone // mouse hit zones in the bottom bar
	bottomBarRow int
}

type zone struct {
	x0, x1 int
	action string
}

func newReview(a *startup.Audit, withSystem bool, s Styles) reviewModel {
	return reviewModel{styles: s, audit: a, withSystem: withSystem}
}

func (r *reviewModel) advance() {
	r.current = r.audit.Next()
	r.sideScroll = 0
	r.pending = ""
	r.pendingWarn = nil
	r.maskConfirm = false
}

func (m *Model) updateReview(msg tea.Msg) (tea.Model, tea.Cmd) {
	r := &m.review
	if r.current == nil {
		// Audit complete → summary.
		m.screen = ScreenSummary
		return m.updateSummary(nil)
	}
	switch msg := msg.(type) {
	case tea.MouseMsg:
		if msg.Action == tea.MouseActionRelease && msg.Button == tea.MouseButtonLeft && msg.Y == r.bottomBarRow {
			for _, z := range r.actionZones {
				if msg.X >= z.x0 && msg.X < z.x1 {
					return m.reviewAction(z.action)
				}
			}
		}
		return m, nil
	case tea.KeyMsg:
		key := msg.String()
		// Confirmation sub-states first.
		if r.maskConfirm {
			if key == "y" {
				r.maskConfirm = false
				r.audit.Decide(r.current, startup.ActionMask)
				r.advance()
			} else {
				r.maskConfirm = false
			}
			return m, nil
		}
		if r.pending == startup.ActionDisableStop {
			switch key {
			case "b": // fall back to disable-at-boot
				r.audit.Decide(r.current, startup.ActionDisable)
				r.advance()
			case "y":
				if len(r.pendingWarn) == 0 {
					r.audit.Decide(r.current, startup.ActionDisableStop)
					r.advance()
				} else {
					r.pending = "" // blocked: y does nothing
				}
			default:
				r.pending = ""
				r.pendingWarn = nil
			}
			return m, nil
		}
		return m.reviewAction(key)
	}
	return m, nil
}

func (m *Model) reviewAction(key string) (tea.Model, tea.Cmd) {
	r := &m.review
	it := r.current
	switch key {
	case "k":
		r.audit.Decide(it, startup.ActionKeep)
		r.advance()
	case "K":
		r.audit.Decide(it, startup.ActionKeepForever)
		r.advance()
	case "d":
		r.audit.Decide(it, startup.ActionDisable)
		r.advance()
	case "s":
		if it.Kind != startup.KindSystemdUnit || it.ActiveState != "active" {
			// Nothing running to stop; same as disable.
			r.audit.Decide(it, startup.ActionDisable)
			r.advance()
			return m, nil
		}
		blocker := r.audit.CheckStopNow(it)
		if blocker.Blocked {
			r.pending = startup.ActionDisableStop
			r.pendingWarn = blocker.Users
		} else {
			r.pending = startup.ActionDisableStop
			r.pendingWarn = nil
		}
	case "n":
		r.audit.Decide(it, startup.ActionSkip)
		r.advance()
	case "u":
		r.audit.UndoLast()
		r.advance()
	case "m":
		if it.Kind == startup.KindSystemdUnit && it.Disableable() {
			r.maskConfirm = true
		}
	case "p":
		m.prev = ScreenReview
		m.screen = ScreenProtected
	case "r":
		m.screen = ScreenSummary
	case "down", "j":
		r.sideScroll++
	case "up":
		if r.sideScroll > 0 {
			r.sideScroll--
		}
	case "q", "esc":
		if len(r.audit.Decisions) > 0 {
			m.screen = ScreenSummary
		} else {
			m.screen = ScreenMenu
		}
	}
	return m, nil
}

func (m *Model) viewReview() string {
	r := &m.review
	s := m.styles
	if r.current == nil {
		return "Review complete."
	}
	it := r.current
	w := m.width

	// Top bar.
	done := r.audit.Total() - r.audit.Remaining()
	mem, boot := r.audit.EstimatedSavings()
	savings := ""
	if mem > 0 || boot > 0 {
		savings = fmt.Sprintf(" · saving ≈%s, %.1fs boot", storage.HumanSize(mem), boot/1000)
	}
	scope := "User"
	if r.withSystem {
		scope = "User + System"
	}
	top := fmt.Sprintf("Item %d of %d · %s · %d queued%s", done+1, r.audit.Total(), scope, len(r.audit.QueuedDisables()), savings)
	if n := r.audit.SkippedKeptCount(); n > 0 {
		top += fmt.Sprintf(" · %d kept earlier (hidden)", n)
	}
	topBar := s.TopBar.Width(w).Render(top)

	// Main card.
	var card strings.Builder
	card.WriteString(s.Bold.Render(it.DisplayName()))
	if it.DisplayName() != it.Name {
		card.WriteString(s.Dim.Render("  (" + it.Name + ")"))
	}
	card.WriteString("\n")
	card.WriteString(s.RiskBadge(it.Risk.String()) + "  " +
		s.Dim.Render(fmt.Sprintf("%s · %s · %s scope", it.Enablement, it.ActiveState, it.Scope)) + "\n\n")
	desc := it.KBDescription
	if desc == "" {
		desc = it.Description
	}
	if desc == "" {
		desc = "No description available."
	}
	card.WriteString(wrap(desc, w-8) + "\n\n")

	var res []string
	if it.Resources.MemoryBytes > 0 {
		res = append(res, "memory "+storage.HumanSize(it.Resources.MemoryBytes))
	}
	if it.Resources.BootMillis > 0 {
		res = append(res, fmt.Sprintf("boot %.0f ms", it.Resources.BootMillis))
	}
	if it.Resources.CPUUsageNSec > 0 {
		res = append(res, fmt.Sprintf("cpu %.1fs total", float64(it.Resources.CPUUsageNSec)/1e9))
	}
	if len(res) > 0 {
		card.WriteString(s.Dim.Render("Resources: "+strings.Join(res, " · ")) + "\n")
	}

	rec := ""
	switch it.Recommend {
	case startup.RecKeep:
		rec = s.Good.Render("Recommendation: keep")
	case startup.RecDisable:
		rec = s.Warn.Render("Recommendation: safe to disable")
	default:
		rec = s.Dim.Render("Recommendation: your call")
	}
	card.WriteString(rec + "\n")
	if it.RecommendReason != "" {
		card.WriteString(wrap("Why: "+it.RecommendReason, w-8) + "\n")
	}
	if it.HardwareNote != "" {
		card.WriteString(s.Warn.Render(wrap("Note: "+it.HardwareNote, w-8)) + "\n")
	}
	if it.PromotedBy != "" {
		card.WriteString(s.Good.Render(wrap("Was only needed by "+it.PromotedBy+", which you just disabled.", w-8)) + "\n")
	}

	// Pending confirmations.
	if r.maskConfirm {
		card.WriteString("\n" + s.Err.Render("⚠ Advanced: mask "+it.Name+"?") + "\n" +
			wrap("Masking makes the unit impossible to start, even manually or as a dependency. This is stronger than disable and can break software that tries to start it. Press y to confirm, any other key to cancel.", w-8) + "\n")
	}
	if r.pending == startup.ActionDisableStop {
		if len(r.pendingWarn) > 0 {
			card.WriteString("\n" + s.Err.Render("⚠ Can't stop now — actively in use by:") + "\n")
			for _, u := range r.pendingWarn {
				card.WriteString("    • " + u + "\n")
			}
			card.WriteString(wrap("Disable "+r.pendingWarn[0]+" first, or press b to disable at next boot instead. Any other key cancels.", w-8) + "\n")
		} else {
			card.WriteString("\n" + s.Warn.Render("Stop "+it.Name+" right now and disable it?") + " " +
				s.Dim.Render("y confirm · b disable at boot only · any other key cancels") + "\n")
		}
	}

	mainPanel := s.Card.Width(maxInt(w*2/3-2, 30)).Render(card.String())

	// Side panel: dependencies.
	var side strings.Builder
	side.WriteString(s.Bold.Render("Needs") + "\n")
	needs := r.audit.Graph.Needs(it)
	if len(needs) == 0 {
		side.WriteString(s.Dim.Render("  nothing") + "\n")
	}
	for _, n := range needs {
		side.WriteString("  " + depLine(r.audit, n, s) + "\n")
	}
	side.WriteString("\n" + s.Bold.Render("Needed by") + "\n")
	if len(it.Dependents) == 0 {
		side.WriteString(s.Dim.Render("  nothing that is enabled or active") + "\n")
	}
	for _, d := range it.Dependents {
		side.WriteString("  " + depLine(r.audit, d, s) + "\n")
	}
	sideLines := strings.Split(side.String(), "\n")
	maxSide := maxInt(m.height-10, 5)
	if r.sideScroll > maxInt(len(sideLines)-maxSide, 0) {
		r.sideScroll = maxInt(len(sideLines)-maxSide, 0)
	}
	if len(sideLines) > maxSide {
		sideLines = sideLines[r.sideScroll:minInt(r.sideScroll+maxSide, len(sideLines))]
	}
	sidePanel := s.Side.Width(maxInt(w/3-4, 20)).Render(strings.Join(sideLines, "\n"))

	body := lipgloss.JoinHorizontal(lipgloss.Top, mainPanel, sidePanel)

	// Bottom bar with mouse zones.
	actions := []struct{ key, label, act string }{
		{"k", "keep", "k"}, {"K", "keep forever", "K"}, {"d", "disable@boot", "d"},
		{"s", "disable+stop", "s"}, {"n", "skip", "n"}, {"u", "undo", "u"},
		{"r", "review", "r"}, {"?", "help", "?"}, {"q", "quit", "q"},
	}
	var bottom strings.Builder
	r.actionZones = r.actionZones[:0]
	x := 1 // padding offset
	for i, a := range actions {
		seg := s.Key.Render(a.key) + " " + a.label
		plain := len(a.key) + 1 + len(a.label)
		r.actionZones = append(r.actionZones, zone{x0: x, x1: x + plain, action: a.act})
		bottom.WriteString(seg)
		if i != len(actions)-1 {
			bottom.WriteString("  ")
			x += plain + 2
		}
	}
	bottomBar := s.BottomBar.Render(bottom.String())
	content := topBar + "\n" + body + "\n" + bottomBar
	r.bottomBarRow = strings.Count(topBar+"\n"+body, "\n") + 1
	return content
}

func depLine(a *startup.Audit, scoped string, s Styles) string {
	parts := strings.SplitN(scoped, "/", 2)
	name := scoped
	if len(parts) == 2 {
		name = parts[1]
		if it := a.Graph.Item(startup.Scope(parts[0]), parts[1]); it != nil {
			st := it.ActiveState
			if st == "" {
				st = it.Enablement
			}
			return name + " " + s.Dim.Render("("+st+")")
		}
	}
	return name
}

func wrap(text string, width int) string {
	if width < 10 {
		width = 10
	}
	words := strings.Fields(text)
	var b strings.Builder
	line := 0
	for i, w := range words {
		if line+len(w)+1 > width && line > 0 {
			b.WriteString("\n")
			line = 0
		} else if i > 0 {
			b.WriteString(" ")
			line++
		}
		b.WriteString(w)
		line += len(w)
	}
	return b.String()
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ---- Protected view ----

func (m *Model) updateProtected(msg tea.Msg) (tea.Model, tea.Cmd) {
	items := m.review.audit.Protected()
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.protIdx > 0 {
				m.protIdx--
			}
		case "down", "j":
			if m.protIdx < len(items)-1 {
				m.protIdx++
			}
		case "esc", "q", "p":
			m.screen = ScreenReview
		}
	}
	return m, nil
}

func (m *Model) viewProtected() string {
	s := m.styles
	items := m.review.audit.Protected()
	var b strings.Builder
	b.WriteString(s.Title.Render("🔒 Protected items (read-only)") + "\n")
	b.WriteString(s.Dim.Render("These can never be disabled from this app. They keep your system bootable,\nconnected, and recoverable.") + "\n\n")
	maxRows := maxInt(m.height-10, 4)
	start := 0
	if m.protIdx >= maxRows {
		start = m.protIdx - maxRows + 1
	}
	for i := start; i < minInt(len(items), start+maxRows); i++ {
		it := items[i]
		line := fmt.Sprintf("%s %s", s.Locked.Render("🔒"), it.Name)
		if i == m.protIdx {
			line = s.Selected.Render(line)
		}
		b.WriteString(line + "\n")
	}
	if len(items) > 0 && m.protIdx < len(items) {
		b.WriteString("\n" + wrap(items[m.protIdx].ProtectedReason, m.width-6) + "\n")
	}
	b.WriteString("\n" + s.Dim.Render("↑/↓ browse · esc back to review"))
	return b.String()
}
