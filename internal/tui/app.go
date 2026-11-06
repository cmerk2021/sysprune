// Package tui contains the Bubble Tea models for sysprune's screens.
package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cmerk2021/sysprune/internal/journal"
	"github.com/cmerk2021/sysprune/internal/runner"
	"github.com/cmerk2021/sysprune/internal/startup"
	"github.com/cmerk2021/sysprune/internal/state"
)

// Screen identifies the active screen.
type Screen int

const (
	ScreenMenu Screen = iota
	ScreenScope
	ScreenLoading
	ScreenReview
	ScreenProtected
	ScreenSummary
	ScreenElevate
	ScreenResult
	ScreenHistory
	ScreenStorageMenu
	ScreenStorageScan
	ScreenStorageReview
	ScreenError
)

// Services bundles everything the TUI needs; all system access goes through
// these injected interfaces.
type Services struct {
	AppName   string
	Version   string
	R         runner.Runner
	FS        runner.FS
	Paths     state.Paths
	Decisions *state.Decisions
	Config    state.Config
	KB        *startup.KB
	Disc      *startup.Discoverer
	Home      string
	DryRun    bool
	// LookPath for elevate detection.
	LookPath func(string) (string, error)
	// Log receives diagnostic lines (file-backed, never the TUI).
	Log func(format string, args ...any)
}

// Model is the root Bubble Tea model.
type Model struct {
	svc    Services
	styles Styles
	width  int
	height int
	screen Screen
	prev   Screen
	help   bool

	// Root context for all background work; cancelled on quit.
	ctx    context.Context
	cancel context.CancelFunc

	menu     menuModel
	scope    scopeModel
	review   reviewModel
	summary  summaryModel
	elevate  elevateModel
	result   resultModel
	history  historyModel
	storage  storageModel
	protIdx  int
	errMsg   string

	// DryRunOutput collects what would be executed; printed after exit.
	DryRunOutput string

	confirmQuit bool
}

// New creates the root model.
func New(svc Services) *Model {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Model{
		svc:    svc,
		styles: NewStyles(),
		screen: ScreenMenu,
		ctx:    ctx,
		cancel: cancel,
	}
	m.menu = newMenu(svc, m.styles)
	m.scope = newScope(m.styles)
	m.storage = newStorage(svc, m.styles)
	return m
}

func (m *Model) Init() tea.Cmd { return nil }

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		// Global keys.
		switch msg.String() {
		case "ctrl+c":
			m.cancel()
			return m, tea.Quit
		case "?":
			m.help = !m.help
			return m, nil
		}
		if m.help {
			m.help = false
			return m, nil
		}
	case errMsg:
		m.errMsg = msg.err.Error()
		m.screen = ScreenError
		return m, nil
	}
	if m.width > 0 && (m.width < MinWidth || m.height < MinHeight) {
		// Terminal too small: only react to resize/quit (handled above).
		return m, nil
	}
	return m.route(msg)
}

type errMsg struct{ err error }

func (m *Model) route(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m.screen {
	case ScreenMenu:
		return m.updateMenu(msg)
	case ScreenScope:
		return m.updateScope(msg)
	case ScreenLoading:
		return m.updateLoading(msg)
	case ScreenReview:
		return m.updateReview(msg)
	case ScreenProtected:
		return m.updateProtected(msg)
	case ScreenSummary:
		return m.updateSummary(msg)
	case ScreenElevate:
		return m.updateElevate(msg)
	case ScreenResult:
		return m.updateResult(msg)
	case ScreenHistory:
		return m.updateHistory(msg)
	case ScreenStorageMenu, ScreenStorageScan, ScreenStorageReview:
		return m.updateStorage(msg)
	case ScreenError:
		if _, ok := msg.(tea.KeyMsg); ok {
			m.screen = ScreenMenu
		}
		return m, nil
	}
	return m, nil
}

func (m *Model) View() string {
	if m.width > 0 && (m.width < MinWidth || m.height < MinHeight) {
		return fmt.Sprintf("\n  Terminal too small (%dx%d).\n  Please resize to at least %dx%d — sysprune needs a little room.\n",
			m.width, m.height, MinWidth, MinHeight)
	}
	if m.help {
		return m.viewHelp()
	}
	switch m.screen {
	case ScreenMenu:
		return m.menu.view(m.width, m.height)
	case ScreenScope:
		return m.scope.view(m.width, m.height)
	case ScreenLoading:
		return m.viewLoading()
	case ScreenReview:
		return m.viewReview()
	case ScreenProtected:
		return m.viewProtected()
	case ScreenSummary:
		return m.viewSummary()
	case ScreenElevate:
		return m.viewElevate()
	case ScreenResult:
		return m.viewResult()
	case ScreenHistory:
		return m.viewHistory()
	case ScreenStorageMenu, ScreenStorageScan, ScreenStorageReview:
		return m.viewStorage()
	case ScreenError:
		return m.styles.Err.Render("Something went wrong:") + "\n\n  " + m.errMsg +
			"\n\n" + m.styles.Dim.Render("Press any key to return to the menu. Details are in the log file.")
	}
	return ""
}

func (m *Model) viewHelp() string {
	s := m.styles
	rows := []string{
		s.Title.Render("Help — " + m.svc.AppName),
		"",
		s.Key.Render("↑/↓, j/k") + "  move    " + s.Key.Render("enter") + "  select    " + s.Key.Render("esc") + "  back",
		"",
		s.Bold.Render("Startup review card:"),
		"  " + s.Key.Render("k") + "  keep            " + s.Key.Render("K") + "  keep and don't ask again",
		"  " + s.Key.Render("d") + "  disable at next boot",
		"  " + s.Key.Render("s") + "  disable and stop now (checked for active users first)",
		"  " + s.Key.Render("n") + "  skip (decide later)   " + s.Key.Render("u") + "  undo last decision",
		"  " + s.Key.Render("m") + "  advanced: mask (extra confirmation, never for protected items)",
		"  " + s.Key.Render("p") + "  browse protected items    " + s.Key.Render("r") + "  review queued changes",
		"",
		s.Bold.Render("Everywhere:"),
		"  " + s.Key.Render("?") + "  toggle this help    " + s.Key.Render("q") + "  quit (confirms if changes are queued)",
		"",
		s.Dim.Render("Nothing is changed until you confirm the final review screen."),
		s.Dim.Render("Every change is journaled first and reversible with --restore."),
	}
	return s.Help.Render(strings.Join(rows, "\n"))
}

// listJournalDirs returns user + system journal dirs for history/restore.
func (m *Model) listJournalDirs() []string {
	return []string{m.svc.Paths.Journal, "/var/lib/" + m.svc.AppName + "/journal"}
}

// loadSessions is used by the history screen.
func (m *Model) loadSessions() ([]*journal.Session, error) {
	return journal.List(m.svc.FS, m.listJournalDirs()...)
}
