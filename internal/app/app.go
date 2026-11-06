// Package app contains flag parsing, mode dispatch, and the application
// identity constants.
package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cmerk2021/sysprune/data"
	"github.com/cmerk2021/sysprune/internal/apply"
	"github.com/cmerk2021/sysprune/internal/journal"
	"github.com/cmerk2021/sysprune/internal/runner"
	"github.com/cmerk2021/sysprune/internal/startup"
	"github.com/cmerk2021/sysprune/internal/state"
	"github.com/cmerk2021/sysprune/internal/tui"
)

// AppName is the single place the application name is defined.
const AppName = "sysprune"

// Version info injected by the linker (GoReleaser).
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

// Main is the real entry point. Returns the process exit code.
func Main(args []string, bi BuildInfo) int {
	ctx := context.Background()

	// Hidden privileged helper mode: must be checked before anything else and
	// is never shown in help.
	if len(args) > 0 && args[0] == "--apply-helper" {
		planPath := "-"
		if len(args) > 1 {
			planPath = args[1]
		}
		return apply.HelperMain(ctx, AppName, planPath, os.Stdin, os.Stdout, os.Stderr)
	}

	var (
		dryRun, history, resetDecisions, version, help bool
		restore                                        string
		doRestore                                      bool
	)
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dry-run":
			dryRun = true
		case "--history":
			history = true
		case "--restore":
			doRestore = true
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				restore = args[i+1]
				i++
			}
		case "--reset-decisions":
			resetDecisions = true
		case "--version", "-v":
			version = true
		case "--help", "-h":
			help = true
		default:
			fmt.Fprintf(os.Stderr, "unknown flag %q\n\n", args[i])
			printHelp(os.Stderr)
			return 2
		}
	}
	if help {
		printHelp(os.Stdout)
		return 0
	}
	if version {
		fmt.Printf("%s %s (commit %s, built %s)\n", AppName, bi.Version, bi.Commit, bi.Date)
		return 0
	}

	fsys := runner.OSFS{}
	r := &runner.Real{}
	paths := state.DefaultPaths(AppName)
	journalDirs := []string{paths.Journal, apply.SystemJournalDir(AppName)}

	// --history is pure local file reading: it must work anywhere (it is the
	// recovery path), so it runs before the systemd check.
	if history {
		return runHistory(fsys, journalDirs)
	}

	// systemd-only.
	if _, err := fsys.Stat("/run/systemd/system"); err != nil {
		fmt.Fprintln(os.Stderr, AppName+" supports systemd-based Linux distributions only.")
		fmt.Fprintln(os.Stderr, "This system does not appear to be running systemd (no /run/systemd/system).")
		return 1
	}

	if resetDecisions {
		d, err := state.LoadDecisions(fsys, paths.StateDir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		n := d.Count()
		if err := d.Reset(); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		fmt.Printf("Cleared %d saved decision(s).\n", n)
		return 0
	}

	if doRestore {
		return runRestore(ctx, r, fsys, journalDirs, restore)
	}
	return runTUI(ctx, r, fsys, paths, dryRun, bi)
}

func printHelp(w io.Writer) {
	fmt.Fprintf(w, `%s — safety-first startup & storage audit for systemd Linux

Usage:
  %s                    launch the interactive audit
  %s --dry-run          full audit flow; prints what WOULD change, changes nothing
  %s --history          list past sessions and changes (no TUI)
  %s --restore [id]     undo a session (default: most recent; no TUI)
  %s --reset-decisions  clear "keep, don't ask again" memory
  %s --version          print version
`, AppName, AppName, AppName, AppName, AppName, AppName, AppName)
}

func runHistory(fsys runner.FS, dirs []string) int {
	sessions, err := journal.List(fsys, dirs...)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if len(sessions) == 0 {
		fmt.Println("No sessions recorded yet.")
		return 0
	}
	for _, s := range sessions {
		status := ""
		if s.Restored() {
			status = " (restored)"
		}
		if n := len(s.Incomplete()); n > 0 {
			status += fmt.Sprintf(" (%d INCOMPLETE operation(s) — possible crash mid-apply)", n)
		}
		fmt.Printf("%s%s\n", s.ID, status)
		for _, op := range s.Completed() {
			target := op.Unit
			if target == "" {
				target = op.SrcPath
			}
			fmt.Printf("  %-12s %-8s %s\n", op.Op, op.Scope, target)
		}
	}
	return 0
}

func runRestore(ctx context.Context, r runner.Runner, fsys runner.FS, dirs []string, id string) int {
	var sess *journal.Session
	var err error
	if id == "" {
		sessions, lerr := journal.List(fsys, dirs...)
		if lerr != nil || len(sessions) == 0 {
			fmt.Fprintln(os.Stderr, "no sessions to restore")
			return 1
		}
		for _, s := range sessions {
			if !s.Restored() && len(s.Completed()) > 0 {
				sess = s
				break
			}
		}
		if sess == nil {
			fmt.Fprintln(os.Stderr, "no un-restored sessions found")
			return 1
		}
	} else {
		sess, err = journal.Find(fsys, id, dirs...)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
	}

	// System-scope changes need root: re-exec via sudo/pkexec if we're not
	// root already (same mechanism as apply).
	if journal.NeedsRoot(sess) && os.Geteuid() != 0 {
		self, err := os.Executable()
		if err == nil {
			self, _ = filepath.EvalSymlinks(self)
		}
		argv := []string{"sudo", "--", self, "--restore", sess.ID}
		if _, lerr := runner.LookPath("sudo"); lerr != nil {
			if _, perr := runner.LookPath("pkexec"); perr == nil {
				argv = []string{"pkexec", self, "--restore", sess.ID}
			} else {
				fmt.Fprintf(os.Stderr, "This session contains system changes. Run as root:\n  %s --restore %s\n", self, sess.ID)
				return 1
			}
		}
		fmt.Printf("Session %s contains system-scope changes; escalating:\n  %s\n", sess.ID, strings.Join(argv, " "))
		code, err := runner.RunInteractive(argv[0], argv[1:]...)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		return code
	}

	fmt.Printf("Restoring session %s (%d completed change(s))…\n", sess.ID, len(sess.Completed()))
	rest := &journal.Restorer{R: r, FS: fsys}
	results := rest.Restore(ctx, sess)
	fail := 0
	for _, res := range results {
		switch {
		case res.Skipped:
			fmt.Printf("  ~ %s: %s\n", res.Op.ID, res.Action)
		case res.Err != nil:
			fmt.Printf("  ✗ %s: %v\n", res.Op.ID, res.Err)
			fail++
		default:
			fmt.Printf("  ✓ %s\n", res.Action)
		}
	}
	// Mark restored.
	dir := filepath.Dir(sess.Path)
	if w, err := journal.NewWriter(fsys, dir, sess.ID); err == nil {
		_ = w.Meta("restored " + time.Now().UTC().Format(time.RFC3339))
	}
	if fail > 0 {
		fmt.Printf("%d operation(s) could not be restored.\n", fail)
		return 1
	}
	fmt.Println("Restore complete.")
	return 0
}

func isTTY() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func buildServices(r runner.Runner, fsys runner.FS, paths state.Paths, dryRun bool, bi BuildInfo) (tui.Services, error) {
	home, _ := os.UserHomeDir()
	kb, err := startup.LoadKB(data.KBYaml, fsys, filepath.Join(paths.ConfigDir, "kb.yaml"))
	if err != nil {
		return tui.Services{}, err
	}
	decisions, err := state.LoadDecisions(fsys, paths.StateDir)
	if err != nil {
		return tui.Services{}, err
	}
	_ = fsys.MkdirAll(paths.StateDir, 0o700)
	logf := func(format string, args ...any) {
		line := time.Now().Format(time.RFC3339) + " " + fmt.Sprintf(format, args...) + "\n"
		_ = fsys.AppendFile(paths.LogFile, []byte(line), 0o600)
	}
	disc := &startup.Discoverer{
		R: r, FS: fsys, KB: kb,
		Env: startup.Env{
			Home:              home,
			XDGCurrentDesktop: os.Getenv("XDG_CURRENT_DESKTOP"),
			OverSSH:           os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != "",
			UID:               os.Getuid(),
		},
		LookPath: runner.LookPath,
	}
	return tui.Services{
		AppName:   AppName,
		Version:   bi.Version,
		R:         r,
		FS:        fsys,
		Paths:     paths,
		Decisions: decisions,
		Config:    state.LoadConfig(fsys, paths.ConfigDir),
		KB:        kb,
		Disc:      disc,
		Home:      home,
		DryRun:    dryRun,
		LookPath:  runner.LookPath,
		Log:       logf,
	}, nil
}

func runTUI(ctx context.Context, r runner.Runner, fsys runner.FS, paths state.Paths, dryRun bool, bi BuildInfo) int {
	svc, err := buildServices(r, fsys, paths, dryRun, bi)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if dryRun && !isTTY() {
		return dryRunNonInteractive(ctx, svc)
	}
	m := tui.New(svc)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	final, err := p.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if fm, ok := final.(*tui.Model); ok && fm.DryRunOutput != "" {
		fmt.Print(fm.DryRunOutput)
	}
	return 0
}

// dryRunNonInteractive prints discovery + the proposed (recommended) plan.
func dryRunNonInteractive(ctx context.Context, svc tui.Services) int {
	fmt.Println("DRY RUN (non-interactive): discovery and proposed plan. Nothing will change.")
	var items []*startup.StartupItem
	sys, err := svc.Disc.DiscoverUnits(ctx, startup.ScopeSystem)
	if err == nil {
		items = append(items, sys...)
	}
	if user, err := svc.Disc.DiscoverUnits(ctx, startup.ScopeUser); err == nil {
		items = append(items, user...)
	}
	if xdg, err := svc.Disc.DiscoverXDGAutostart(ctx); err == nil {
		items = append(items, xdg...)
	}
	if cron, err := svc.Disc.DiscoverCronReboot(ctx); err == nil {
		items = append(items, cron...)
	}
	pc := svc.Disc.BuildProtectionContext(ctx)
	g := startup.NewGraph(items)
	svc.Disc.Analyze(items, pc, g, nil)

	fmt.Printf("\nDiscovered %d startup item(s).\n\n", len(items))
	var proposals []*startup.StartupItem
	for _, it := range items {
		tag := it.Risk.String()
		fmt.Printf("  %-12s %-10s %-45s %s\n", tag, it.Scope, truncate(it.Name, 45), truncate(it.DisplayName(), 30))
		if it.Recommend == startup.RecDisable && it.Disableable() && it.Risk == startup.RiskSafe {
			proposals = append(proposals, it)
		}
	}
	fmt.Println("\nProposed plan (knowledge-base recommendations rated Safe):")
	if len(proposals) == 0 {
		fmt.Println("  (nothing recommended for disabling)")
		return 0
	}
	sessionID := journal.NewSessionID(time.Now())
	audit := startup.NewAudit(items, g, nil)
	for _, it := range proposals {
		audit.Decide(it, startup.ActionDisable)
	}
	userPlan, sysPlan, _ := startup.BuildPlans(audit, sessionID)
	for _, c := range userPlan.Changes {
		fmt.Println("  " + strings.Join(c.Commands(), " && "))
	}
	for _, c := range sysPlan.Changes {
		fmt.Println("  sudo " + strings.Join(c.Commands(), " && "))
	}
	if len(sysPlan.Changes) > 0 {
		if data, err := sysPlan.Marshal(); err == nil {
			fmt.Println("\nPrivileged helper plan that would be used:")
			fmt.Println(string(data))
		}
	}
	fmt.Println("\nNothing was modified and no escalation happened.")
	return 0
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
