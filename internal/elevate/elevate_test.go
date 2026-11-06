package elevate

import (
	"errors"
	"strings"
	"testing"
)

func TestDetect(t *testing.T) {
	have := func(names ...string) func(string) (string, error) {
		set := map[string]bool{}
		for _, n := range names {
			set[n] = true
		}
		return func(name string) (string, error) {
			if set[name] {
				return "/usr/bin/" + name, nil
			}
			return "", errors.New("not found")
		}
	}
	if m := Detect(have("sudo", "pkexec")); m != MethodSudo {
		t.Errorf("sudo+pkexec: %v, want sudo preferred", m)
	}
	if m := Detect(have("pkexec")); m != MethodPkexec {
		t.Errorf("pkexec only: %v", m)
	}
	if m := Detect(have()); m != MethodNone {
		t.Errorf("neither: %v", m)
	}
}

func TestCommandUsesAbsolutePath(t *testing.T) {
	// Acceptance criterion 7: re-exec by absolute path, never $PATH.
	cmd := Command(MethodSudo, "/home/u/.local/bin/sysprune", "/tmp/plan.json")
	if cmd[0] != "sudo" || cmd[1] != "--" {
		t.Errorf("cmd = %v", cmd)
	}
	joined := strings.Join(cmd, " ")
	if !strings.Contains(joined, "/home/u/.local/bin/sysprune --apply-helper /tmp/plan.json") {
		t.Errorf("cmd = %v", cmd)
	}
}

func TestManualInstructions(t *testing.T) {
	out := ManualInstructions("/home/u/.local/bin/sysprune", "/tmp/plan.json")
	if !strings.Contains(out, "/home/u/.local/bin/sysprune --apply-helper /tmp/plan.json") {
		t.Errorf("manual instructions = %q", out)
	}
}
