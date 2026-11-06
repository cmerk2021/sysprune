package tui

import (
	"github.com/charmbracelet/lipgloss"
)

// Styles respects NO_COLOR automatically via lipgloss/termenv and degrades
// to the terminal's base palette (we only use ANSI 16 colors).
type Styles struct {
	Title     lipgloss.Style
	TopBar    lipgloss.Style
	BottomBar lipgloss.Style
	Card      lipgloss.Style
	Side      lipgloss.Style
	Safe      lipgloss.Style
	Think     lipgloss.Style
	Locked    lipgloss.Style
	Dim       lipgloss.Style
	Bold      lipgloss.Style
	Err       lipgloss.Style
	Good      lipgloss.Style
	Warn      lipgloss.Style
	Key       lipgloss.Style
	Selected  lipgloss.Style
	Help      lipgloss.Style
}

func NewStyles() Styles {
	return Styles{
		Title:     lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6")),
		TopBar:    lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(lipgloss.Color("6")).Padding(0, 1),
		BottomBar: lipgloss.NewStyle().Foreground(lipgloss.Color("7")).Padding(0, 1),
		Card:      lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1),
		Side:      lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1).BorderForeground(lipgloss.Color("8")),
		Safe:      lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Bold(true),
		Think:     lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true),
		Locked:    lipgloss.NewStyle().Foreground(lipgloss.Color("4")).Bold(true),
		Dim:       lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
		Bold:      lipgloss.NewStyle().Bold(true),
		Err:       lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true),
		Good:      lipgloss.NewStyle().Foreground(lipgloss.Color("2")),
		Warn:      lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
		Key:       lipgloss.NewStyle().Foreground(lipgloss.Color("5")).Bold(true),
		Selected:  lipgloss.NewStyle().Reverse(true),
		Help:      lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(1, 2),
	}
}

// RiskBadge renders the risk indicator.
func (s Styles) RiskBadge(risk string) string {
	switch risk {
	case "safe":
		return s.Safe.Render("🟢 Safe")
	case "think-twice":
		return s.Think.Render("🟡 Think twice")
	case "protected":
		return s.Locked.Render("🔒 Protected")
	}
	return risk
}

// MinWidth and MinHeight define the smallest usable terminal.
const (
	MinWidth  = 60
	MinHeight = 16
)
