package apply

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"time"

	"github.com/cmerk2021/sysprune/internal/journal"
	"github.com/cmerk2021/sysprune/internal/runner"
	"github.com/cmerk2021/sysprune/internal/startup"
)

// SystemJournalDir is where the privileged helper writes its journal.
func SystemJournalDir(appName string) string {
	return filepath.Join("/var/lib", appName, "journal")
}

// InvokingUser resolves the real user behind sudo/pkexec from the environment.
func InvokingUser(getenv func(string) string) (uid, gid int, home string, err error) {
	uidStr := getenv("SUDO_UID")
	if uidStr == "" {
		uidStr = getenv("PKEXEC_UID")
	}
	if uidStr == "" {
		return 0, 0, "", errors.New("helper mode requires SUDO_UID or PKEXEC_UID (run via sudo or pkexec)")
	}
	uid, err = strconv.Atoi(uidStr)
	if err != nil || uid < 0 {
		return 0, 0, "", fmt.Errorf("invalid invoking uid %q", uidStr)
	}
	gid = uid
	if g := getenv("SUDO_GID"); g != "" {
		if v, err := strconv.Atoi(g); err == nil {
			gid = v
		}
	}
	if u, lerr := user.LookupId(uidStr); lerr == nil {
		home = u.HomeDir
		if g, err2 := strconv.Atoi(u.Gid); err2 == nil {
			gid = g
		}
	}
	return uid, gid, home, nil
}

// HelperMain is the --apply-helper entry point. planPath may be "-" to read
// the plan from stdin. Returns the process exit code.
func HelperMain(ctx context.Context, appName string, planPath string, stdin io.Reader, stdout, stderr io.Writer) int {
	// Safe, minimal environment for everything the helper spawns.
	env := []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C"}
	r := &runner.Real{Env: env}
	fsys := runner.OSFS{}

	uid, gid, home, err := InvokingUser(os.Getenv)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 2
	}

	var plan *startup.Plan
	if planPath == "-" {
		data, err := io.ReadAll(io.LimitReader(stdin, MaxPlanSize+1))
		if err != nil {
			fmt.Fprintln(stderr, "error reading plan from stdin:", err)
			return 2
		}
		if len(data) > MaxPlanSize {
			fmt.Fprintln(stderr, "error: plan exceeds size limit")
			return 2
		}
		var p startup.Plan
		if err := json.Unmarshal(data, &p); err != nil {
			fmt.Fprintln(stderr, "error: invalid plan JSON:", err)
			return 2
		}
		plan = &p
	} else {
		plan, err = ReadPlanFile(fsys, planPath, uid)
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 2
		}
	}

	// Independent re-validation. The embedded KB only — user overrides are
	// deliberately ignored in the privileged path.
	kb, err := loadEmbeddedKB()
	if err != nil {
		fmt.Fprintln(stderr, "error loading knowledge base:", err)
		return 2
	}
	if err := ValidatePlan(plan, kb); err != nil {
		fmt.Fprintln(stderr, "error: plan validation failed:", err)
		return 2
	}
	// The helper only ever applies system-scope changes.
	for _, c := range plan.Changes {
		if c.Scope != "system" {
			fmt.Fprintf(stderr, "error: helper refuses non-system change for %s\n", c.ItemID)
			return 2
		}
	}

	dir := SystemJournalDir(appName)
	if err := fsys.MkdirAll(dir, 0o700); err != nil {
		fmt.Fprintln(stderr, "error creating journal dir:", err)
		return 2
	}
	w, err := journal.NewWriter(fsys, dir, plan.SessionID)
	if err != nil {
		fmt.Fprintln(stderr, "error opening journal:", err)
		return 2
	}
	// Mirror the journal into the invoking user's state dir.
	if home != "" {
		mirrorDir := filepath.Join(home, ".local", "state", appName, "journal")
		w.Mirror = filepath.Join(mirrorDir, plan.SessionID+".jsonl")
	}
	_ = w.Meta("apply-helper start " + time.Now().UTC().Format(time.RFC3339))

	ex := &Executor{R: r, FS: fsys, J: w, CheckDrift: true}
	res := ex.Execute(ctx, plan)

	// Fix mirror ownership so the user can read their copy.
	if w.Mirror != "" {
		_ = fsys.Chown(w.Mirror, uid, gid)
		_ = fsys.Chown(filepath.Dir(w.Mirror), uid, gid)
	}

	out, _ := json.Marshal(res)
	fmt.Fprintln(stdout, string(out))
	for _, cr := range res.Results {
		if !cr.OK && !cr.Skipped {
			return 1
		}
	}
	return 0
}
