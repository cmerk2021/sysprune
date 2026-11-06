package startup

import (
	"context"
	"strings"
	"testing"

	"github.com/cmerk2021/sysprune/data"
	"github.com/cmerk2021/sysprune/internal/runner"
)

// Realistic fixtures of systemctl output.
const listUnitFilesFixture = `bluetooth.service                enabled enabled
cups.service                     enabled enabled
cups.socket                      enabled enabled
dbus.service                     static  -
ModemManager.service             enabled enabled
NetworkManager.service           enabled enabled
getty@.service                   enabled enabled
systemd-journald.service         static  -
foo@bar.service                  enabled enabled
masked-thing.service             masked  enabled
broken.service                   enabled enabled
sshd.service                     alias   -
ssh.service                      enabled enabled
fstrim.timer                     enabled enabled
unused-disabled.service          disabled enabled
`

func showFixture(id, desc, state, active string, extra map[string]string) string {
	var b strings.Builder
	b.WriteString("Id=" + id + "\n")
	b.WriteString("Description=" + desc + "\n")
	b.WriteString("FragmentPath=/usr/lib/systemd/system/" + id + "\n")
	b.WriteString("UnitFileState=" + state + "\n")
	b.WriteString("ActiveState=" + active + "\n")
	b.WriteString("SubState=running\n")
	b.WriteString("LoadState=loaded\n")
	b.WriteString("MemoryCurrent=10485760\n")
	b.WriteString("CPUUsageNSec=123456789\n")
	for k, v := range extra {
		b.WriteString(k + "=" + v + "\n")
	}
	return b.String()
}

func scriptShow(f *runner.FakeRunner, unit, out string) {
	f.ScriptPrefix("systemctl show --no-pager --property=", runner.Response{Stdout: []byte(out), MatchSuffix: " -- " + unit})
}

func newFixtureDiscoverer(t *testing.T) (*Discoverer, *runner.FakeRunner, *runner.MemFS) {
	t.Helper()
	f := runner.NewFake()
	mem := runner.NewMemFS()
	f.ScriptPrefix("systemctl list-unit-files", runner.Response{Stdout: []byte(listUnitFilesFixture)})
	f.ScriptPrefix("systemd-analyze blame", runner.Response{Stdout: []byte("5.123s bluetooth.service\n1.2s cups.service\n800ms ModemManager.service\n")})
	f.ScriptPrefix("systemctl list-dependencies", runner.Response{Stdout: []byte("default.target\n  display-manager.service\n  getty.target\n")})

	scriptShow(f, "bluetooth.service", showFixture("bluetooth.service", "Bluetooth service", "enabled", "active", map[string]string{
		"ExecStart": "{ path=/usr/libexec/bluetooth/bluetoothd ; argv[]=/usr/libexec/bluetooth/bluetoothd ; ignore_errors=no ; start_time=[n/a] ; stop_time=[n/a] ; pid=0 ; code=(null) ; status=0/0 }",
	}))
	scriptShow(f, "cups.service", showFixture("cups.service", "CUPS Scheduler", "enabled", "active", map[string]string{"TriggeredBy": "cups.socket"}))
	scriptShow(f, "cups.socket", showFixture("cups.socket", "CUPS Scheduler Socket", "enabled", "active", map[string]string{"Triggers": "cups.service"}))
	scriptShow(f, "dbus.service", showFixture("dbus.service", "D-Bus System Message Bus", "static", "active", nil))
	scriptShow(f, "ModemManager.service", showFixture("ModemManager.service", "Modem Manager", "enabled", "active", nil))
	scriptShow(f, "NetworkManager.service", showFixture("NetworkManager.service", "Network Manager", "enabled", "active", map[string]string{"WantedBy": "multi-user.target network-online.target"}))
	scriptShow(f, "getty@.service", showFixture("getty@.service", "Getty on %I", "enabled", "inactive", nil))
	scriptShow(f, "systemd-journald.service", showFixture("systemd-journald.service", "Journal Service", "static", "active", nil))
	scriptShow(f, "foo@bar.service", showFixture("foo@bar.service", "Template instance", "enabled", "active", nil))
	scriptShow(f, "masked-thing.service", showFixture("masked-thing.service", "Masked unit", "masked", "inactive", nil))
	scriptShow(f, "ssh.service", showFixture("ssh.service", "OpenSSH server", "enabled", "active", nil))
	scriptShow(f, "fstrim.timer", showFixture("fstrim.timer", "Discard unused blocks weekly", "enabled", "active", map[string]string{"Triggers": "fstrim.service"}))
	// broken.service: systemctl show fails -> skipped, must not abort.
	f.ScriptPrefix("systemctl show --no-pager --property=", runner.Response{ExitCode: 1, MatchSuffix: " -- broken.service"})
	// Fallback for context units.
	f.ScriptPrefix("systemctl show", runner.Response{Stdout: []byte("")})
	f.ScriptPrefix("crontab -l", runner.Response{Stdout: []byte("# m h dom mon dow command\n@reboot /home/u/bin/sync-notes --fast\n0 5 * * * echo hi\n")})

	kb, err := LoadKB(data.KBYaml, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	d := &Discoverer{R: f, FS: mem, KB: kb, Env: Env{Home: "/home/u", UID: 1000}}
	return d, f, mem
}

func TestDiscoverUnitsFromFixtures(t *testing.T) {
	d, _, _ := newFixtureDiscoverer(t)
	items, err := d.DiscoverUnits(context.Background(), ScopeSystem)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*StartupItem{}
	for _, it := range items {
		byName[it.Name] = it
	}
	if _, ok := byName["unused-disabled.service"]; ok {
		t.Error("disabled unit must not be discovered")
	}
	if _, ok := byName["broken.service"]; ok {
		t.Error("unit whose show failed must be skipped, not fatal")
	}
	if _, ok := byName["sshd.service"]; ok {
		t.Error("alias entries must be skipped")
	}
	bt := byName["bluetooth.service"]
	if bt == nil {
		t.Fatal("bluetooth.service missing")
	}
	if bt.Exec != "/usr/libexec/bluetooth/bluetoothd" {
		t.Errorf("ExecStart parse: %q", bt.Exec)
	}
	if bt.Resources.MemoryBytes != 10485760 {
		t.Errorf("MemoryCurrent: %d", bt.Resources.MemoryBytes)
	}
	if bt.Resources.BootMillis != 5123 {
		t.Errorf("blame millis: %v", bt.Resources.BootMillis)
	}
	if st := byName["dbus.service"]; st == nil || !st.Static {
		t.Error("static unit should be flagged Static")
	}
	if m := byName["masked-thing.service"]; m == nil || !m.Masked {
		t.Error("masked unit should be flagged Masked")
	}
	if tpl := byName["foo@bar.service"]; tpl == nil {
		t.Error("template instance should be discovered")
	}
	if cups := byName["cups.service"]; cups == nil || len(cups.TriggeredBy) != 1 || cups.TriggeredBy[0] != "cups.socket" {
		t.Errorf("cups TriggeredBy: %+v", cups)
	}
}

func TestDiscoverXDGAutostartOverride(t *testing.T) {
	d, _, mem := newFixtureDiscoverer(t)
	_ = mem.WriteFile("/etc/xdg/autostart/app.desktop", []byte("[Desktop Entry]\nName=System App\nExec=app\n"), 0o644)
	_ = mem.WriteFile("/etc/xdg/autostart/sys-only.desktop", []byte("[Desktop Entry]\nName=Sys Only\nExec=sysonly\n"), 0o644)
	// User override hides the system entry.
	_ = mem.WriteFile("/home/u/.config/autostart/app.desktop", []byte("[Desktop Entry]\nName=User App\nExec=app\nHidden=true\n"), 0o644)
	_ = mem.WriteFile("/home/u/.config/autostart/mine.desktop", []byte("[Desktop Entry]\nName=Mine\nExec=mine\n"), 0o644)

	items, err := d.DiscoverXDGAutostart(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, it := range items {
		names[it.Name] = true
	}
	if names["app.desktop"] {
		t.Error("user override with Hidden=true should remove app.desktop from active autostart")
	}
	if !names["sys-only.desktop"] || !names["mine.desktop"] {
		t.Errorf("expected sys-only and mine, got %v", names)
	}
}

func TestDiscoverCronReboot(t *testing.T) {
	d, _, _ := newFixtureDiscoverer(t)
	items, err := d.DiscoverCronReboot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	if items[0].Exec != "/home/u/bin/sync-notes --fast" {
		t.Errorf("Exec = %q", items[0].Exec)
	}
	if items[0].Kind != KindCronReboot || items[0].Scope != ScopeUser {
		t.Errorf("kind/scope: %+v", items[0])
	}
}

func TestAnalyzeAppliesAllLayers(t *testing.T) {
	d, _, _ := newFixtureDiscoverer(t)
	items, err := d.DiscoverUnits(context.Background(), ScopeSystem)
	if err != nil {
		t.Fatal(err)
	}
	g := NewGraph(items)
	pc := ProtectionContext{}
	d.Analyze(items, pc, g, nil)

	byName := map[string]*StartupItem{}
	for _, it := range items {
		byName[it.Name] = it
	}
	if !byName["NetworkManager.service"].Protected {
		t.Error("NetworkManager must be protected (layer 1)")
	}
	if !byName["dbus.service"].Protected {
		t.Error("dbus must be protected")
	}
	bt := byName["bluetooth.service"]
	if bt.Protected {
		t.Error("bluetooth must not be protected")
	}
	// No bluetooth hardware in MemFS -> recommend disable (layer 3).
	if bt.Recommend != RecDisable {
		t.Errorf("bluetooth recommend = %v, want disable", bt.Recommend)
	}
	// cups.service has an active socket dependent -> think twice (layer 2).
	if byName["cups.service"].Risk != RiskThink {
		t.Errorf("cups risk = %v, want think", byName["cups.service"].Risk)
	}
}
