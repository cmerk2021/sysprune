package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cmerk2021/sysprune/internal/startup"
)

// ---- Main menu ----

type menuModel struct {
	svc    Services
	styles Styles
	items  []string
	idx    int
}

func newMenu(svc Services, s Styles) menuModel {
	items := []string{
		"Startup audit — review everything that starts automatically",
		"Storage audit — reclaim disk space safely",
		"History & restore — review or undo past sessions",
		"Quit",
	}
	return menuModel{svc: svc, styles: s, items: items}
}

func (mm menuModel) view(w, h int) string {
	s := mm.styles
	var b strings.Builder
	title := mm.svc.AppName + " " + mm.svc.Version
	if mm.svc.DryRun {
		title += "  (dry run — nothing will be changed)"
	}
	b.WriteString(s.Title.Render(title) + "\n")
	b.WriteString(s.Dim.Render("Safety first: protected items are never touched, every change is reversible.") + "\n\n")
	for i, it := range mm.items {
		cursor := "  "
		line := it
		if i == mm.idx {
			cursor = s.Key.Render("> ")
			line = s.Selected.Render(it)
		}
		b.WriteString(cursor + line + "\n")
	}
	b.WriteString("\n" + s.Dim.Render("↑/↓ move · enter select · ? help · q quit"))
	return b.String()
}

func (m *Model) updateMenu(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.menu.idx > 0 {
				m.menu.idx--
			}
		case "down", "j":
			if m.menu.idx < len(m.menu.items)-1 {
				m.menu.idx++
			}
		case "q", "esc":
			m.cancel()
			return m, tea.Quit
		case "enter":
			switch m.menu.idx {
			case 0:
				m.screen = ScreenScope
			case 1:
				m.screen = ScreenStorageMenu
				return m, m.storage.initScan(m)
			case 2:
				m.screen = ScreenHistory
				return m, m.loadHistoryCmd()
			case 3:
				m.cancel()
				return m, tea.Quit
			}
		}
	}
	return m, nil
}

// ---- Scope selection ----

type scopeModel struct {
	styles Styles
	idx    int // 0 = user only, 1 = user + system
}

func newScope(s Styles) scopeModel { return scopeModel{styles: s} }

func (sm scopeModel) view(w, h int) string {
	s := sm.styles
	var b strings.Builder
	b.WriteString(s.Title.Render("Startup audit — choose scope") + "\n\n")
	opts := []struct{ title, desc string }{
		{"User only (recommended)", "Your login items and user services. Never asks for a password."},
		{"User + System", "Also reviews system services. Applying system changes asks for your password once, at the very end, after you review everything."},
	}
	for i, o := range opts {
		cursor := "  "
		title := o.title
		if i == sm.idx {
			cursor = s.Key.Render("> ")
			title = s.Selected.Render(o.title)
		}
		b.WriteString(cursor + title + "\n    " + s.Dim.Render(o.desc) + "\n\n")
	}
	b.WriteString(s.Dim.Render("enter select · t toggle later from the review screen · esc back"))
	return b.String()
}

func (m *Model) updateScope(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k", "down", "j", "tab":
			m.scope.idx = 1 - m.scope.idx
		case "esc", "q":
			m.screen = ScreenMenu
		case "enter":
			m.screen = ScreenLoading
			return m, m.discoverCmd(m.scope.idx == 1)
		}
	}
	return m, nil
}

// ---- Discovery (loading screen) ----

type discoveredMsg struct {
	audit      *startup.Audit
	withSystem bool
}

func (m *Model) discoverCmd(withSystem bool) tea.Cmd {
	svc := m.svc
	ctx := m.ctx
	return func() tea.Msg {
		var items []*startup.StartupItem
		if withSystem {
			sys, err := svc.Disc.DiscoverUnits(ctx, startup.ScopeSystem)
			if err != nil {
				return errMsg{fmt.Errorf("discovering system units: %w", err)}
			}
			items = append(items, sys...)
		}
		userUnits, err := svc.Disc.DiscoverUnits(ctx, startup.ScopeUser)
		if err != nil {
			svc.Log("user unit discovery: %v", err)
		}
		items = append(items, userUnits...)
		xdg, err := svc.Disc.DiscoverXDGAutostart(ctx)
		if err == nil {
			items = append(items, xdg...)
		}
		cron, err := svc.Disc.DiscoverCronReboot(ctx)
		if err == nil {
			items = append(items, cron...)
		}
		pc := svc.Disc.BuildProtectionContext(ctx)
		g := startup.NewGraph(items)
		svc.Disc.Analyze(items, pc, g, nil)
		audit := startup.NewAudit(items, g, svc.Decisions)
		return discoveredMsg{audit: audit, withSystem: withSystem}
	}
}

func (m *Model) updateLoading(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case discoveredMsg:
		m.review = newReview(msg.audit, msg.withSystem, m.styles)
		m.review.advance()
		m.screen = ScreenReview
		return m, nil
	case tea.KeyMsg:
		if msg.String() == "esc" || msg.String() == "q" {
			m.screen = ScreenMenu
		}
	}
	return m, nil
}

func (m *Model) viewLoading() string {
	return "\n  " + m.styles.Title.Render("Scanning startup items…") + "\n\n  " +
		m.styles.Dim.Render("Reading systemd units, autostart entries and cron jobs.\n  This is read-only — nothing is changed. (esc to cancel)")
}
