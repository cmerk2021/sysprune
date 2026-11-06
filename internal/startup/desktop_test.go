package startup

import "testing"

func TestParseDesktopEntry(t *testing.T) {
	data := []byte(`[Desktop Entry]
Type=Application
Name=Example App
Name[de]=Beispiel
Comment=Does example things
Exec=example --flag "quoted arg"
TryExec=example
Hidden=false
NoDisplay=true
OnlyShowIn=GNOME;KDE;
NotShowIn=XFCE;
X-GNOME-Autostart-enabled=false

[Desktop Action Other]
Name=Should be ignored
Exec=other
`)
	e := ParseDesktopEntry(data)
	if e.Name != "Example App" {
		t.Errorf("Name = %q", e.Name)
	}
	if e.Comment != "Does example things" {
		t.Errorf("Comment = %q", e.Comment)
	}
	if e.Exec != `example --flag "quoted arg"` {
		t.Errorf("Exec = %q", e.Exec)
	}
	if e.TryExec != "example" {
		t.Errorf("TryExec = %q", e.TryExec)
	}
	if e.Hidden {
		t.Error("Hidden should be false")
	}
	if !e.NoDisplay {
		t.Error("NoDisplay should be true")
	}
	if len(e.OnlyShowIn) != 2 || e.OnlyShowIn[0] != "GNOME" || e.OnlyShowIn[1] != "KDE" {
		t.Errorf("OnlyShowIn = %v", e.OnlyShowIn)
	}
	if len(e.NotShowIn) != 1 || e.NotShowIn[0] != "XFCE" {
		t.Errorf("NotShowIn = %v", e.NotShowIn)
	}
	if e.GnomeAutostart == nil || *e.GnomeAutostart {
		t.Error("X-GNOME-Autostart-enabled should parse as false")
	}
	// Keys from other groups must not leak in.
	if e.Exec == "other" {
		t.Error("parsed keys from a non-Desktop-Entry group")
	}
}

func TestAutostartActive(t *testing.T) {
	bf := false
	tests := []struct {
		name     string
		e        DesktopEntry
		desktops []string
		want     bool
	}{
		{"plain", DesktopEntry{Exec: "x"}, []string{"GNOME"}, true},
		{"hidden", DesktopEntry{Exec: "x", Hidden: true}, []string{"GNOME"}, false},
		{"gnome-disabled", DesktopEntry{Exec: "x", GnomeAutostart: &bf}, []string{"GNOME"}, false},
		{"onlyshowin match", DesktopEntry{Exec: "x", OnlyShowIn: []string{"GNOME"}}, []string{"GNOME"}, true},
		{"onlyshowin mismatch", DesktopEntry{Exec: "x", OnlyShowIn: []string{"KDE"}}, []string{"GNOME"}, false},
		{"notshowin match", DesktopEntry{Exec: "x", NotShowIn: []string{"GNOME"}}, []string{"GNOME"}, false},
		{"notshowin mismatch", DesktopEntry{Exec: "x", NotShowIn: []string{"KDE"}}, []string{"GNOME"}, true},
		{"no exec", DesktopEntry{}, []string{"GNOME"}, false},
	}
	for _, tc := range tests {
		if got := AutostartActive(tc.e, tc.desktops); got != tc.want {
			t.Errorf("%s: AutostartActive = %v, want %v", tc.name, got, tc.want)
		}
	}
}
