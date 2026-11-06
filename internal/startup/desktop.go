package startup

import (
	"strings"
)

// DesktopEntry is a parsed XDG .desktop autostart entry.
type DesktopEntry struct {
	Name            string
	Comment         string
	Exec            string
	TryExec         string
	Hidden          bool
	NoDisplay       bool
	OnlyShowIn      []string
	NotShowIn       []string
	GnomeAutostart  *bool // X-GNOME-Autostart-enabled, nil if absent
	Type            string
}

// ParseDesktopEntry parses the [Desktop Entry] group of a .desktop file per
// the Desktop Entry specification. Localized keys (Name[xx]) are ignored in
// favor of the unlocalized key.
func ParseDesktopEntry(data []byte) DesktopEntry {
	var e DesktopEntry
	inEntry := false
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			inEntry = strings.EqualFold(line, "[Desktop Entry]")
			continue
		}
		if !inEntry {
			continue
		}
		i := strings.IndexByte(line, '=')
		if i <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:i])
		val := strings.TrimSpace(line[i+1:])
		switch key {
		case "Name":
			e.Name = unescapeDesktop(val)
		case "Comment":
			e.Comment = unescapeDesktop(val)
		case "Exec":
			e.Exec = val
		case "TryExec":
			e.TryExec = val
		case "Type":
			e.Type = val
		case "Hidden":
			e.Hidden = strings.EqualFold(val, "true")
		case "NoDisplay":
			e.NoDisplay = strings.EqualFold(val, "true")
		case "OnlyShowIn":
			e.OnlyShowIn = splitList(val)
		case "NotShowIn":
			e.NotShowIn = splitList(val)
		case "X-GNOME-Autostart-enabled":
			b := strings.EqualFold(val, "true")
			e.GnomeAutostart = &b
		}
	}
	return e
}

func splitList(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ";") {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func unescapeDesktop(v string) string {
	r := strings.NewReplacer(`\s`, " ", `\n`, "\n", `\t`, "\t", `\r`, "\r", `\\`, `\`)
	return r.Replace(v)
}

// AutostartActive decides whether a desktop entry would actually start in the
// given desktop environment (from XDG_CURRENT_DESKTOP, split on ':').
func AutostartActive(e DesktopEntry, currentDesktops []string) bool {
	if e.Exec == "" {
		return false // nothing to start
	}
	if e.Hidden {
		return false
	}
	if e.GnomeAutostart != nil && !*e.GnomeAutostart {
		return false
	}
	inList := func(list []string) bool {
		for _, want := range list {
			for _, cur := range currentDesktops {
				if strings.EqualFold(want, cur) {
					return true
				}
			}
		}
		return false
	}
	if len(e.OnlyShowIn) > 0 && !inList(e.OnlyShowIn) {
		return false
	}
	if len(e.NotShowIn) > 0 && inList(e.NotShowIn) {
		return false
	}
	return true
}
