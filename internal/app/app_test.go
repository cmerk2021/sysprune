package app

import (
	"context"
	"strings"
	"testing"

	"github.com/cmerk2021/sysprune/data"
	"github.com/cmerk2021/sysprune/internal/runner"
	"github.com/cmerk2021/sysprune/internal/startup"
	"github.com/cmerk2021/sysprune/internal/tui"
)

// TestDryRunModifiesNothing is acceptance criterion 6: --dry-run runs
// discovery and planning but never calls a mutating command and never
// escalates.
func TestDryRunModifiesNothing(t *testing.T) {
	f := runner.NewFake()
	mem := runner.NewMemFS()
	f.ScriptPrefix("systemctl list-unit-files", runner.Response{Stdout: []byte(
		"bluetooth.service enabled enabled\n")})
	f.ScriptPrefix("systemctl show --no-pager --property=", runner.Response{Stdout: []byte(
		"Id=bluetooth.service\nDescription=Bluetooth\nUnitFileState=enabled\nActiveState=active\nLoadState=loaded\n"),
		MatchSuffix: " -- bluetooth.service"})
	f.ScriptPrefix("systemctl", runner.Response{})
	f.ScriptPrefix("systemd-analyze", runner.Response{})
	f.ScriptPrefix("crontab", runner.Response{ExitCode: 1})

	kb, err := startup.LoadKB(data.KBYaml, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	disc := &startup.Discoverer{R: f, FS: mem, KB: kb, Env: startup.Env{Home: "/home/u", UID: 1000}}
	svc := tui.Services{AppName: AppName, R: f, FS: mem, KB: kb, Disc: disc, Home: "/home/u", DryRun: true}

	if code := dryRunNonInteractive(context.Background(), svc); code != 0 {
		t.Fatalf("dry run exited %d", code)
	}

	// No mutating command may have run; no sudo/pkexec either.
	mutating := []string{"disable", "enable", "stop", "start", "mask", "unmask", "vacuum"}
	for _, line := range f.CallLines() {
		for _, bad := range mutating {
			if strings.Contains(line, " "+bad) {
				t.Errorf("dry run executed mutating command: %s", line)
			}
		}
		if strings.HasPrefix(line, "sudo") || strings.HasPrefix(line, "pkexec") {
			t.Errorf("dry run escalated: %s", line)
		}
	}
}
