package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cmerk2021/sysprune/internal/startup"
)

func key(s string) tea.KeyMsg {
	if len(s) == 1 {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func testItem(name string, mod func(*startup.StartupItem)) *startup.StartupItem {
	it := &startup.StartupItem{
		ID:   startup.ItemID(startup.KindSystemdUnit, startup.ScopeSystem, name),
		Kind: startup.KindSystemdUnit, Scope: startup.ScopeSystem, Name: name,
		Enablement: "enabled", ActiveState: "active",
	}
	if mod != nil {
		mod(it)
	}
	return it
}

func newReviewTestModel(t *testing.T, items []*startup.StartupItem) *Model {
	t.Helper()
	g := startup.NewGraph(items)
	for _, it := range items {
		it.Dependents = g.ActiveDependents(it, nil)
		it.Risk = g.RateRisk(it, nil)
	}
	a := startup.NewAudit(items, g, nil)
	m := New(Services{AppName: "sysprune", Version: "test"})
	m.width, m.height = 100, 30
	m.review = newReview(a, true, m.styles)
	m.review.advance()
	m.screen = ScreenReview
	return m
}

func TestWindowSizeAndTooSmall(t *testing.T) {
	m := New(Services{AppName: "sysprune"})
	m.Update(tea.WindowSizeMsg{Width: 20, Height: 5})
	v := m.View()
	if !strings.Contains(strings.ToLower(v), "too small") {
		t.Errorf("small terminal view = %q", v)
	}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if strings.Contains(strings.ToLower(m.View()), "too small") {
		t.Error("still too small after resize")
	}
}

func TestHelpOverlayToggle(t *testing.T) {
	m := New(Services{AppName: "sysprune"})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.Update(key("?"))
	if !m.help {
		t.Fatal("? did not open help")
	}
	if !strings.Contains(m.View(), "k") {
		t.Error("help should list keybindings")
	}
	m.Update(key("?"))
	if m.help {
		t.Error("? did not close help")
	}
}

func TestReviewKeepAndDisableFlow(t *testing.T) {
	a := testItem("aaa.service", nil)
	b := testItem("bbb.service", nil)
	m := newReviewTestModel(t, []*startup.StartupItem{a, b})

	first := m.review.current
	if first == nil {
		t.Fatal("no current item")
	}
	m.Update(key("d")) // disable at boot
	if len(m.review.audit.Decisions) != 1 || m.review.audit.Decisions[0].Action != startup.ActionDisable {
		t.Fatalf("decisions: %+v", m.review.audit.Decisions)
	}
	second := m.review.current
	if second == nil || second.ID == first.ID {
		t.Fatal("did not advance to the next item")
	}
	m.Update(key("k")) // keep
	if m.review.current != nil {
		t.Error("audit should be complete")
	}
}

func TestReviewUndo(t *testing.T) {
	a := testItem("aaa.service", nil)
	b := testItem("bbb.service", nil)
	m := newReviewTestModel(t, []*startup.StartupItem{a, b})
	m.Update(key("d"))
	m.Update(key("u"))
	if len(m.review.audit.Decisions) != 0 {
		t.Errorf("undo did not pop the decision: %+v", m.review.audit.Decisions)
	}
	if m.review.current == nil {
		t.Error("current item missing after undo")
	}
}

func TestReviewStopNowBlockedShowsWarning(t *testing.T) {
	// Acceptance criterion 4 at the UI level.
	svc := testItem("svc.service", nil)
	dep := testItem("dep.service", func(it *startup.StartupItem) {
		it.Requires = []string{"svc.service"}
	})
	m := newReviewTestModel(t, []*startup.StartupItem{svc, dep})
	// First item is the leaf (dep); skip to review svc which has a dependent.
	for m.review.current != nil && m.review.current.Name != "svc.service" {
		m.Update(key("n"))
	}
	if m.review.current == nil {
		t.Fatal("svc.service never offered")
	}
	m.Update(key("s")) // disable and stop now
	if m.review.pending != startup.ActionDisableStop {
		t.Fatal("no pending stop confirmation")
	}
	if len(m.review.pendingWarn) == 0 || !strings.Contains(m.review.pendingWarn[0], "dep.service") {
		t.Errorf("warning should name dep.service: %v", m.review.pendingWarn)
	}
	// y must NOT proceed while blocked.
	m.Update(key("y"))
	for _, d := range m.review.audit.Decisions {
		if d.Action == startup.ActionDisableStop {
			t.Error("blocked stop-now was queued anyway")
		}
	}
	// b falls back to disable-at-boot.
	m.Update(key("s"))
	m.Update(key("b"))
	found := false
	for _, d := range m.review.audit.Decisions {
		if d.Action == startup.ActionDisable {
			found = true
		}
	}
	if !found {
		t.Error("fallback to disable-at-boot not recorded")
	}
}

func TestReviewMaskNeedsConfirmation(t *testing.T) {
	a := testItem("aaa.service", nil)
	m := newReviewTestModel(t, []*startup.StartupItem{a})
	m.Update(key("m"))
	if !m.review.maskConfirm {
		t.Fatal("mask should require confirmation")
	}
	m.Update(key("n")) // anything but y cancels
	if m.review.maskConfirm || len(m.review.audit.Decisions) != 0 {
		t.Error("mask confirmation not cancelled")
	}
	m.Update(key("m"))
	m.Update(key("y"))
	if len(m.review.audit.Decisions) != 1 || m.review.audit.Decisions[0].Action != startup.ActionMask {
		t.Errorf("mask not queued after confirm: %+v", m.review.audit.Decisions)
	}
}

func TestReviewMaskRefusedForProtected(t *testing.T) {
	// Acceptance criterion 2 at the UI level: protected items never enter the
	// decision flow at all.
	p := testItem("dbus.service", func(it *startup.StartupItem) {
		it.Protected = true
		it.Risk = startup.RiskProtected
	})
	n := testItem("normal.service", nil)
	m := newReviewTestModel(t, []*startup.StartupItem{p, n})
	for m.review.current != nil {
		if m.review.current.Protected {
			t.Fatal("protected item offered in review")
		}
		m.Update(key("k"))
	}
}

func TestReviewViewShowsCoreInfo(t *testing.T) {
	a := testItem("bluetooth.service", func(it *startup.StartupItem) {
		it.FriendlyName = "Bluetooth"
		it.Description = "Bluetooth service"
		it.Resources.MemoryBytes = 4 << 20
		it.Recommend = startup.RecDisable
		it.RecommendReason = "No Bluetooth hardware detected."
	})
	m := newReviewTestModel(t, []*startup.StartupItem{a})
	v := m.View()
	for _, want := range []string{"bluetooth.service", "Item 1 of 1", "No Bluetooth hardware"} {
		if !strings.Contains(v, want) {
			t.Errorf("review view missing %q", want)
		}
	}
}

func TestErrMsgRoutesToErrorScreen(t *testing.T) {
	m := New(Services{AppName: "sysprune"})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.Update(errMsg{err: errTest{}})
	if m.screen != ScreenError {
		t.Errorf("screen = %v, want ScreenError", m.screen)
	}
	if !strings.Contains(m.View(), "boom") {
		t.Error("error view should show the message")
	}
}

type errTest struct{}

func (errTest) Error() string { return "boom" }
