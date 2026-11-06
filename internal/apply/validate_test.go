package apply

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/cmerk2021/sysprune/data"
	"github.com/cmerk2021/sysprune/internal/runner"
	"github.com/cmerk2021/sysprune/internal/startup"
)

func testKB(t *testing.T) *startup.KB {
	t.Helper()
	kb, err := startup.LoadKB(data.KBYaml, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	return kb
}

func TestValidateUnitNameMaliciousInputs(t *testing.T) {
	tests := []struct {
		name string
		ok   bool
	}{
		{"bluetooth.service", true},
		{"fstrim.timer", true},
		{"cups.socket", true},
		{"foo@bar.service", true},
		{"dbus-:1.2-org.freedesktop.service", true},
		// Malicious / invalid:
		{"../../../etc/passwd.service", false}, // path traversal
		{"/etc/systemd/system/evil.service", false},
		{"-rf.service", false},        // leading dash (argument injection)
		{"--force.service", false},
		{"a;rm -rf /.service", false}, // shell metacharacters
		{"a&&b.service", false},
		{"a|b.service", false},
		{"a$(x).service", false},
		{"a`x`.service", false},
		{"a b.service", false},   // whitespace
		{"a\nb.service", false},  // newline
		{"evil.target", false},   // wrong suffix
		{"evil.mount", false},
		{"evil", false},          // no suffix
		{"", false},
		{".service", false},
	}
	for _, tc := range tests {
		err := ValidateUnitName(tc.name)
		if (err == nil) != tc.ok {
			t.Errorf("ValidateUnitName(%q): err=%v, want ok=%v", tc.name, err, tc.ok)
		}
	}
}

func TestValidateChangeOperationAllowlist(t *testing.T) {
	kb := testKB(t)
	for _, op := range []string{"disable", "enable", "stop", "start", "mask", "unmask"} {
		c := startup.PlanChange{Kind: "unit", Scope: "system", Op: op, Unit: "bluetooth.service"}
		if err := ValidateChange(c, kb); err != nil {
			t.Errorf("op %q should be allowed: %v", op, err)
		}
	}
	for _, op := range []string{"restart", "daemon-reload", "edit", "reset-failed", "exec", "", "disable; rm -rf /"} {
		c := startup.PlanChange{Kind: "unit", Scope: "system", Op: op, Unit: "bluetooth.service"}
		if err := ValidateChange(c, kb); err == nil {
			t.Errorf("op %q must be refused", op)
		}
	}
}

func TestValidateChangeRefusesProtectedUnits(t *testing.T) {
	// Acceptance criterion 2: the helper refuses protected units even from a
	// hand-edited plan.
	kb := testKB(t)
	for _, unitName := range []string{
		"dbus.service", "systemd-logind.service", "NetworkManager.service",
		"gdm.service", "polkit.service", "user@1000.service",
		"sshd.service", "firewalld.service", // firewall override NOT honored in helper
	} {
		c := startup.PlanChange{Kind: "unit", Scope: "system", Op: "disable", Unit: unitName}
		err := ValidateChange(c, kb)
		if err == nil || !strings.Contains(err.Error(), "protected") {
			t.Errorf("protected unit %q accepted by validator: %v", unitName, err)
		}
	}
}

func TestValidateChangeCleanOps(t *testing.T) {
	kb := testKB(t)
	valid := []startup.PlanChange{
		{Kind: "clean", Scope: "system", Op: "vacuum-journal", Param: "500M"},
		{Kind: "clean", Scope: "system", Op: "vacuum-journal", Param: "14d"},
		{Kind: "clean", Scope: "system", Op: "apt-clean"},
		{Kind: "clean", Scope: "system", Op: "docker-prune"},
	}
	for _, c := range valid {
		if err := ValidateChange(c, kb); err != nil {
			t.Errorf("%+v should validate: %v", c, err)
		}
	}
	invalid := []startup.PlanChange{
		{Kind: "clean", Scope: "system", Op: "vacuum-journal", Param: "500M; rm -rf /"},
		{Kind: "clean", Scope: "system", Op: "vacuum-journal", Param: "-rf"},
		{Kind: "clean", Scope: "system", Op: "vacuum-journal", Param: ""},
		{Kind: "clean", Scope: "system", Op: "apt-clean", Param: "--yes"}, // no params allowed
		{Kind: "clean", Scope: "system", Op: "rm-rf"},
		{Kind: "shell", Scope: "system", Op: "disable"},
	}
	for _, c := range invalid {
		if err := ValidateChange(c, kb); err == nil {
			t.Errorf("%+v must be refused", c)
		}
	}
}

func validPlan() *startup.Plan {
	return &startup.Plan{
		Version:   startup.PlanVersion,
		SessionID: "20240101T000000-abcd",
		Changes: []startup.PlanChange{
			{ItemID: "unit/system/bluetooth.service", Kind: "unit", Scope: "system", Op: "disable", Unit: "bluetooth.service", PriorEnabled: "enabled", PriorActive: "active"},
		},
	}
}

func TestValidatePlan(t *testing.T) {
	kb := testKB(t)
	if err := ValidatePlan(validPlan(), kb); err != nil {
		t.Fatalf("valid plan refused: %v", err)
	}
	t.Run("wrong version", func(t *testing.T) {
		p := validPlan()
		p.Version = 99
		if ValidatePlan(p, kb) == nil {
			t.Error("wrong version accepted")
		}
	})
	t.Run("bad session id", func(t *testing.T) {
		p := validPlan()
		p.SessionID = "../../../etc"
		if ValidatePlan(p, kb) == nil {
			t.Error("path-traversal session id accepted")
		}
	})
	t.Run("empty plan", func(t *testing.T) {
		p := validPlan()
		p.Changes = nil
		if ValidatePlan(p, kb) == nil {
			t.Error("empty plan accepted")
		}
	})
}

func writePlan(t *testing.T, mem *runner.MemFS, path string, uid int, perm uint32) {
	t.Helper()
	data, err := validPlan().Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := mem.WriteFile(path, data, fs.FileMode(perm)); err != nil {
		t.Fatal(err)
	}
	mem.SetOwner(path, uid, uid)
}

func TestReadPlanFile(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		mem := runner.NewMemFS()
		writePlan(t, mem, "/tmp/plan.json", 1000, 0o600)
		p, err := ReadPlanFile(mem, "/tmp/plan.json", 1000)
		if err != nil {
			t.Fatalf("valid plan file refused: %v", err)
		}
		if p.SessionID != "20240101T000000-abcd" {
			t.Errorf("session id = %q", p.SessionID)
		}
	})
	t.Run("symlinked plan file", func(t *testing.T) {
		mem := runner.NewMemFS()
		writePlan(t, mem, "/tmp/real.json", 1000, 0o600)
		if err := mem.Symlink("/tmp/real.json", "/tmp/link.json"); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadPlanFile(mem, "/tmp/link.json", 1000); err == nil {
			t.Error("symlinked plan file accepted")
		}
	})
	t.Run("wrong ownership", func(t *testing.T) {
		mem := runner.NewMemFS()
		writePlan(t, mem, "/tmp/plan.json", 0, 0o600) // owned by root
		if _, err := ReadPlanFile(mem, "/tmp/plan.json", 1000); err == nil {
			t.Error("plan file owned by another user accepted")
		}
	})
	t.Run("group writable", func(t *testing.T) {
		mem := runner.NewMemFS()
		writePlan(t, mem, "/tmp/plan.json", 1000, 0o620)
		if _, err := ReadPlanFile(mem, "/tmp/plan.json", 1000); err == nil {
			t.Error("group-writable plan file accepted")
		}
	})
	t.Run("world writable", func(t *testing.T) {
		mem := runner.NewMemFS()
		writePlan(t, mem, "/tmp/plan.json", 1000, 0o602)
		if _, err := ReadPlanFile(mem, "/tmp/plan.json", 1000); err == nil {
			t.Error("world-writable plan file accepted")
		}
	})
	t.Run("oversized", func(t *testing.T) {
		mem := runner.NewMemFS()
		big := make([]byte, MaxPlanSize+1)
		if err := mem.WriteFile("/tmp/plan.json", big, 0o600); err != nil {
			t.Fatal(err)
		}
		mem.SetOwner("/tmp/plan.json", 1000, 1000)
		if _, err := ReadPlanFile(mem, "/tmp/plan.json", 1000); err == nil {
			t.Error("oversized plan file accepted")
		}
	})
	t.Run("invalid json", func(t *testing.T) {
		mem := runner.NewMemFS()
		if err := mem.WriteFile("/tmp/plan.json", []byte("{nope"), 0o600); err != nil {
			t.Fatal(err)
		}
		mem.SetOwner("/tmp/plan.json", 1000, 1000)
		if _, err := ReadPlanFile(mem, "/tmp/plan.json", 1000); err == nil {
			t.Error("invalid JSON accepted")
		}
	})
}
