package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cmerk2021/sysprune/internal/journal"
)

type historyModel struct {
	sessions []*journal.Session
	idx      int
	restored []journal.RestoreResult
	showRes  bool
	confirm  bool
	err      string
}

type historyLoadedMsg struct {
	sessions []*journal.Session
	err      error
}

type restoredMsg struct {
	results []journal.RestoreResult
	err     error
}

func (m *Model) loadHistoryCmd() tea.Cmd {
	return func() tea.Msg {
		sessions, err := m.loadSessions()
		return historyLoadedMsg{sessions: sessions, err: err}
	}
}

func (m *Model) restoreCmd(s *journal.Session) tea.Cmd {
	svc := m.svc
	ctx := m.ctx
	return func() tea.Msg {
		if journal.NeedsRoot(s) {
			return restoredMsg{err: fmt.Errorf(
				"this session contains system-scope changes; restore it from a terminal with: %s --restore %s", svc.AppName, s.ID)}
		}
		r := &journal.Restorer{R: svc.R, FS: svc.FS}
		results := r.Restore(ctx, s)
		// Record the restore in the session journal itself.
		if w, err := journal.NewWriter(svc.FS, strings.TrimSuffix(s.Path, "/"+s.ID+".jsonl"), s.ID); err == nil {
			_ = w.Meta("restored")
		}
		return restoredMsg{results: results}
	}
}

func (m *Model) updateHistory(msg tea.Msg) (tea.Model, tea.Cmd) {
	h := &m.history
	switch msg := msg.(type) {
	case historyLoadedMsg:
		h.sessions = msg.sessions
		h.idx, h.showRes, h.confirm, h.err = 0, false, false, ""
		if msg.err != nil {
			h.err = msg.err.Error()
		}
		return m, nil
	case restoredMsg:
		h.confirm = false
		if msg.err != nil {
			h.err = msg.err.Error()
			return m, nil
		}
		h.restored = msg.results
		h.showRes = true
		return m, nil
	case tea.KeyMsg:
		if h.showRes {
			h.showRes = false
			return m, m.loadHistoryCmd()
		}
		switch msg.String() {
		case "up", "k":
			if h.idx > 0 {
				h.idx--
			}
			h.confirm = false
		case "down", "j":
			if h.idx < len(h.sessions)-1 {
				h.idx++
			}
			h.confirm = false
		case "r", "enter":
			if h.idx < len(h.sessions) {
				if !h.confirm {
					h.confirm = true
					return m, nil
				}
				h.confirm = false
				return m, m.restoreCmd(h.sessions[h.idx])
			}
		case "esc", "q":
			m.screen = ScreenMenu
		default:
			h.confirm = false
		}
	}
	return m, nil
}

func (m *Model) viewHistory() string {
	s := m.styles
	h := &m.history
	var b strings.Builder
	b.WriteString(s.Title.Render("History & restore") + "\n\n")
	if h.showRes {
		b.WriteString(s.Bold.Render("Restore results:") + "\n")
		for _, r := range h.restored {
			switch {
			case r.Skipped:
				b.WriteString("  " + s.Warn.Render("~") + " " + r.Op.ID + " — " + r.Action + "\n")
			case r.Err != nil:
				b.WriteString("  " + s.Err.Render("✗") + " " + r.Op.ID + " — " + r.Err.Error() + "\n")
			default:
				b.WriteString("  " + s.Good.Render("✓") + " " + r.Action + "\n")
			}
		}
		b.WriteString("\n" + s.Dim.Render("press any key"))
		return b.String()
	}
	if len(h.sessions) == 0 {
		b.WriteString(s.Dim.Render("No sessions yet. Changes you apply will appear here.") + "\n\n")
		b.WriteString(s.Dim.Render("esc back"))
		return b.String()
	}
	for i, sess := range h.sessions {
		done := len(sess.Completed())
		line := fmt.Sprintf("%s — %d change(s)", sess.ID, done)
		if sess.Restored() {
			line += " (restored)"
		}
		if n := len(sess.Incomplete()); n > 0 {
			line += s.Err.Render(fmt.Sprintf(" (%d incomplete — crash?)", n))
		}
		if i == h.idx {
			line = s.Selected.Render(line)
		}
		b.WriteString("  " + line + "\n")
	}
	if h.idx < len(h.sessions) {
		b.WriteString("\n" + s.Bold.Render("Changes in selected session:") + "\n")
		for _, op := range h.sessions[h.idx].Completed() {
			target := op.Unit
			if target == "" {
				target = op.SrcPath
			}
			b.WriteString(s.Dim.Render(fmt.Sprintf("  %s %s (%s)", op.Op, target, op.Scope)) + "\n")
		}
	}
	if h.err != "" {
		b.WriteString("\n" + s.Err.Render(h.err) + "\n")
	}
	if h.confirm {
		b.WriteString("\n" + s.Warn.Render("Restore this session? Press r/enter again to confirm.") + "\n")
	}
	b.WriteString("\n" + s.Dim.Render("↑/↓ select · r restore (undo) · esc back"))
	return b.String()
}
