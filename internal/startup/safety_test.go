package startup

import (
	"strings"
	"testing"

	"github.com/cmerk2021/sysprune/data"
	"github.com/cmerk2021/sysprune/internal/runner"
)

func loadTestKB(t *testing.T) *KB {
	t.Helper()
	kb, err := LoadKB(data.KBYaml, nil, "")
	if err != nil {
		t.Fatalf("LoadKB: %v", err)
	}
	return kb
}

func TestProtectionListMatching(t *testing.T) {
	kb := loadTestKB(t)
	pc := ProtectionContext{}
	tests := []struct {
		unit      string
		protected bool
	}{
		{"dbus.service", true},
		{"dbus-broker.service", true},
		{"systemd-journald.service", true},
		{"systemd-logind.service", true},
		{"systemd-udevd.service", true},
		{"systemd-oomd.service", true}, // systemd-* catch-all
		{"NetworkManager.service", true},
		{"NetworkManager-dispatcher.service", true},
		{"wpa_supplicant.service", true},
		{"getty@tty1.service", true},
		{"serial-getty@ttyS0.service", true},
		{"gdm.service", true},
		{"sddm.service", true},
		{"lightdm.service", true},
		{"polkit.service", true},
		{"udisks2.service", true},
		{"accounts-daemon.service", true},
		{"upower.service", true},
		{"rtkit-daemon.service", true},
		{"user@1000.service", true},
		{"user-runtime-dir@1000.service", true},
		{"sshd.service", true},
		{"ssh.service", true},
		{"auditd.service", true},
		{"apparmor.service", true},
		{"firewalld.service", true},
		{"ufw.service", true},
		{"nftables.service", true},
		{"cron.service", true},
		{"crond.service", true},
		{"pipewire.service", true},
		{"pipewire-pulse.socket", true},
		{"wireplumber.service", true},
		{"pulseaudio.service", true},
		// Not protected:
		{"bluetooth.service", false},
		{"cups.service", false},
		{"ModemManager.service", false},
		{"avahi-daemon.service", false},
		{"fstrim.timer", false},
		{"docker.service", false},
		{"my-random.service", false},
	}
	for _, tc := range tests {
		got := kb.CheckProtection(tc.unit, "", pc)
		if got.Protected != tc.protected {
			t.Errorf("CheckProtection(%q) = %v, want %v", tc.unit, got.Protected, tc.protected)
		}
		if tc.protected && got.Reason == "" {
			t.Errorf("CheckProtection(%q): protected without a reason", tc.unit)
		}
	}
}

func TestDynamicProtections(t *testing.T) {
	kb := loadTestKB(t)
	t.Run("ssh session", func(t *testing.T) {
		got := kb.CheckProtection("sshd.service", "", ProtectionContext{OverSSH: true})
		if !got.Protected || !strings.Contains(got.Reason, "SSH") {
			t.Errorf("sshd over SSH: got %+v", got)
		}
	})
	t.Run("firewall override", func(t *testing.T) {
		got := kb.CheckProtection("firewalld.service", "", ProtectionContext{FirewallOverride: true})
		if got.Protected {
			t.Error("firewall with explicit override should not be hard-protected")
		}
		if got.Warn == "" {
			t.Error("firewall override must carry a heavy warning")
		}
	})
	t.Run("critical chain", func(t *testing.T) {
		pc := ProtectionContext{CriticalChain: map[string]bool{"foo.service": true}}
		if !kb.CheckProtection("foo.service", "", pc).Protected {
			t.Error("critical-chain unit must be protected")
		}
	})
	t.Run("network online provider", func(t *testing.T) {
		pc := ProtectionContext{NetworkOnlineProviders: map[string]bool{"connman.service": true}}
		if !kb.CheckProtection("connman.service", "", pc).Protected {
			t.Error("network-online provider must be protected")
		}
	})
	t.Run("session unit", func(t *testing.T) {
		pc := ProtectionContext{SessionUnits: map[string]bool{"myseat.service": true}}
		if !kb.CheckProtection("myseat.service", "", pc).Protected {
			t.Error("session unit must be protected")
		}
	})
	t.Run("display manager binary", func(t *testing.T) {
		pc := ProtectionContext{DisplayManagerBinary: "/usr/bin/sway"}
		if !kb.CheckProtection("my-compositor.service", "/usr/bin/sway", pc).Protected {
			t.Error("unit running the display manager binary must be protected")
		}
	})
	t.Run("rootfs unit", func(t *testing.T) {
		pc := ProtectionContext{RootFSUnits: map[string]bool{"-.mount": true}}
		if !kb.CheckProtection("-.mount", "", pc).Protected {
			t.Error("root filesystem mount must be protected")
		}
	})
}

func TestUserKBCannotRemoveProtection(t *testing.T) {
	mem := runner.NewMemFS()
	override := `
protected:
  - pattern: "extra-protected.service"
    reason: "user added"
entries:
  - match: "bluetooth.service"
    name: "My Bluetooth"
    description: "overridden"
    recommend: keep
    reason: "user says keep"
`
	if err := mem.WriteFile("/home/u/.config/sysprune/kb.yaml", []byte(override), 0o644); err != nil {
		t.Fatal(err)
	}
	kb, err := LoadKB(data.KBYaml, mem, "/home/u/.config/sysprune/kb.yaml")
	if err != nil {
		t.Fatalf("LoadKB with override: %v", err)
	}
	// Embedded protections still hold.
	if !kb.CheckProtection("dbus.service", "", ProtectionContext{}).Protected {
		t.Error("user override must not remove embedded protection")
	}
	// User additions apply.
	if !kb.CheckProtection("extra-protected.service", "", ProtectionContext{}).Protected {
		t.Error("user-added protection should apply")
	}
	// Entry override replaced the embedded one.
	e := kb.MatchEntry("bluetooth.service")
	if e == nil || e.Name != "My Bluetooth" {
		t.Errorf("user entry override not applied: %+v", e)
	}
}

func TestKBMatchingAndConditions(t *testing.T) {
	kb := loadTestKB(t)
	t.Run("exact beats glob", func(t *testing.T) {
		e := kb.MatchEntry("cups.service")
		if e == nil || e.Match != "cups.service" {
			t.Fatalf("expected exact cups.service match, got %+v", e)
		}
	})
	t.Run("glob match", func(t *testing.T) {
		e := kb.MatchEntry("tracker-miner-fs-3.service")
		if e == nil || !strings.Contains(e.Name, "Tracker") {
			t.Fatalf("expected tracker-* glob match, got %+v", e)
		}
	})
	t.Run("no match", func(t *testing.T) {
		if e := kb.MatchEntry("totally-unknown.service"); e != nil {
			t.Fatalf("expected nil, got %+v", e)
		}
	})
	t.Run("condition: no bluetooth hardware", func(t *testing.T) {
		mem := runner.NewMemFS() // /sys/class/bluetooth absent
		e := kb.MatchEntry("bluetooth.service")
		rec, note, _ := e.EvalConditions(mem, nil)
		if rec != RecDisable {
			t.Errorf("no bluetooth hw: rec=%v want disable", rec)
		}
		if note == "" {
			t.Error("expected a hardware note")
		}
	})
	t.Run("condition: bluetooth hardware present", func(t *testing.T) {
		mem := runner.NewMemFS()
		_ = mem.WriteFile("/sys/class/bluetooth/hci0", []byte("x"), 0o644)
		e := kb.MatchEntry("bluetooth.service")
		rec, note, _ := e.EvalConditions(mem, nil)
		if rec != RecThink || note != "" {
			t.Errorf("bluetooth hw present: rec=%v note=%q, want think and no note", rec, note)
		}
	})
	t.Run("condition: docker without containers", func(t *testing.T) {
		mem := runner.NewMemFS()
		e := kb.MatchEntry("docker.service")
		rec, _, _ := e.EvalConditions(mem, nil)
		if rec != RecDisable {
			t.Errorf("docker with no containers: rec=%v want disable", rec)
		}
	})
}

func TestKBHasEnoughEntries(t *testing.T) {
	kb := loadTestKB(t)
	if len(kb.Entries) < 60 {
		t.Errorf("knowledge base has %d entries, want at least 60", len(kb.Entries))
	}
	if len(kb.Protected) < 30 {
		t.Errorf("protection list has %d patterns, want at least 30", len(kb.Protected))
	}
	for _, e := range kb.Entries {
		if e.Description == "" || e.Name == "" || e.Recommend == "" {
			t.Errorf("entry %q is missing name/description/recommend", e.Match)
		}
	}
}
