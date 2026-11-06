package startup

import (
	"context"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/cmerk2021/sysprune/internal/runner"
)

// Env abstracts the process environment for discovery.
type Env struct {
	Home            string
	XDGCurrentDesktop string // raw $XDG_CURRENT_DESKTOP
	OverSSH         bool
	UID             int
}

// Discoverer finds everything that starts automatically.
type Discoverer struct {
	R   runner.Runner
	FS  runner.FS
	Env Env
	KB  *KB
	// LookPath resolves a binary in PATH (injectable for tests).
	LookPath func(string) (string, error)
}

// unitProps fetched per unit via systemctl show.
var showProps = []string{
	"Id", "Description", "FragmentPath", "UnitFileState", "ActiveState",
	"SubState", "ExecStart", "MemoryCurrent", "CPUUsageNSec",
	"Requires", "Requisite", "Wants", "BindsTo", "PartOf", "Upholds",
	"WantedBy", "RequiredBy", "TriggeredBy", "Triggers", "Conflicts",
	"LoadState",
}

// DiscoverUnits discovers systemd units for one scope.
func (d *Discoverer) DiscoverUnits(ctx context.Context, scope Scope) ([]*StartupItem, error) {
	sc := runner.Systemctl{R: d.R, User: scope == ScopeUser}
	files, err := sc.ListUnitFiles(ctx, "*.service", "*.timer", "*.socket")
	if err != nil {
		return nil, err
	}
	blame := runner.AnalyzeBlame(ctx, d.R, scope == ScopeUser)

	names := make([]string, 0, len(files))
	for name, state := range files {
		switch state {
		case "enabled", "enabled-runtime", "static", "masked", "masked-runtime", "generated", "linked", "linked-runtime":
			names = append(names, name)
		case "alias", "disabled", "indirect", "transient", "bad":
			// disabled/indirect units don't start at boot; aliases are
			// duplicates of their target.
			continue
		default:
			continue
		}
		_ = state
	}
	sort.Strings(names)

	var items []*StartupItem
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		props, err := sc.Show(ctx, name, showProps...)
		if err != nil {
			continue // a single broken unit must not abort discovery
		}
		if props["LoadState"] == "not-found" {
			continue
		}
		id := props["Id"]
		if id == "" {
			id = name
		}
		if id != name {
			// Alias resolved to a different Id; skip duplicates, the real Id
			// will be (or was) processed from its own list entry.
			if _, ok := files[id]; ok {
				continue
			}
		}
		state := files[name]
		it := &StartupItem{
			ID:          ItemID(KindSystemdUnit, scope, id),
			Kind:        KindSystemdUnit,
			Scope:       scope,
			Name:        id,
			Description: props["Description"],
			UnitPath:    props["FragmentPath"],
			Enablement:  state,
			ActiveState: props["ActiveState"],
			SubState:    props["SubState"],
			Exec:        firstExec(props["ExecStart"]),
			Static:      state == "static",
			Masked:      strings.HasPrefix(state, "masked"),
			Requires:    fields(props["Requires"]),
			Requisite:   fields(props["Requisite"]),
			Wants:       fields(props["Wants"]),
			BindsTo:     fields(props["BindsTo"]),
			PartOf:      fields(props["PartOf"]),
			Upholds:     fields(props["Upholds"]),
			WantedBy:    fields(props["WantedBy"]),
			RequiredBy:  fields(props["RequiredBy"]),
			TriggeredBy: fields(props["TriggeredBy"]),
			Triggers:    fields(props["Triggers"]),
		}
		it.Resources.MemoryBytes = parseBytes(props["MemoryCurrent"])
		it.Resources.CPUUsageNSec = parseBytes(props["CPUUsageNSec"])
		it.Resources.BootMillis = blame[id]
		items = append(items, it)
	}
	return items, nil
}

func fields(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Fields(s)
}

func parseBytes(s string) int64 {
	if s == "" || s == "[not set]" || strings.HasPrefix(s, "18446744073709") { // (uint64)-1 family
		return -1
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return -1
	}
	return v
}

// firstExec extracts the binary+args from systemd's ExecStart property dump
// ("{ path=/usr/bin/foo ; argv[]=/usr/bin/foo --bar ; ... }").
func firstExec(s string) string {
	if s == "" {
		return ""
	}
	if i := strings.Index(s, "argv[]="); i >= 0 {
		rest := s[i+len("argv[]="):]
		if j := strings.Index(rest, " ;"); j >= 0 {
			return strings.TrimSpace(rest[:j])
		}
		return strings.TrimSpace(rest)
	}
	return s
}

// ExecBinary returns the resolved first word of the Exec line.
func ExecBinary(exec string) string {
	f := strings.Fields(exec)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

// DiscoverXDGAutostart parses autostart .desktop files. User entries override
// system entries with the same filename.
func (d *Discoverer) DiscoverXDGAutostart(ctx context.Context) ([]*StartupItem, error) {
	userDir := filepath.Join(d.Env.Home, ".config", "autostart")
	sysDir := "/etc/xdg/autostart"
	desktops := strings.Split(d.Env.XDGCurrentDesktop, ":")

	byName := map[string]*StartupItem{}
	scan := func(dir string, userLevel bool) {
		entries, err := d.FS.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".desktop") {
				continue
			}
			p := filepath.Join(dir, e.Name())
			data, err := d.FS.ReadFile(p)
			if err != nil {
				continue
			}
			de := ParseDesktopEntry(data)
			active := AutostartActive(de, desktops)
			if de.TryExec != "" && d.LookPath != nil {
				if _, err := d.LookPath(de.TryExec); err != nil {
					active = false
				}
			}
			name := de.Name
			if name == "" {
				name = strings.TrimSuffix(e.Name(), ".desktop")
			}
			it := &StartupItem{
				ID:           ItemID(KindXDGAutostart, ScopeUser, e.Name()),
				Kind:         KindXDGAutostart,
				Scope:        ScopeUser, // autostart always runs as the user
				Name:         e.Name(),
				FriendlyName: name,
				Description:  de.Comment,
				UnitPath:     p,
				Exec:         de.Exec,
				Enablement:   "autostart",
				ActiveState:  "n/a",
			}
			if !active {
				it.Enablement = "disabled"
			}
			if de.NoDisplay && de.Hidden {
				continue
			}
			// User entry overrides system entry with the same filename.
			if prev, ok := byName[e.Name()]; ok && userLevel {
				*prev = *it
			} else if !ok {
				byName[e.Name()] = it
			}
		}
	}
	scan(sysDir, false)
	scan(userDir, true)

	var items []*StartupItem
	for _, it := range byName {
		if it.Enablement == "autostart" { // only offer active entries
			items = append(items, it)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items, nil
}

// DiscoverCronReboot reads the user crontab and returns @reboot entries.
func (d *Discoverer) DiscoverCronReboot(ctx context.Context) ([]*StartupItem, error) {
	out, _, code, err := d.R.Run(ctx, "crontab", "-l")
	if err != nil || code != 0 {
		return nil, nil // no crontab or no cron installed: fine
	}
	var items []*StartupItem
	for i, line := range strings.Split(string(out), "\n") {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "@reboot") {
			continue
		}
		cmd := strings.TrimSpace(strings.TrimPrefix(t, "@reboot"))
		name := "@reboot:" + strconv.Itoa(i+1)
		items = append(items, &StartupItem{
			ID:           ItemID(KindCronReboot, ScopeUser, name),
			Kind:         KindCronReboot,
			Scope:        ScopeUser,
			Name:         name,
			FriendlyName: "Cron @reboot job",
			Description:  "Runs at every boot from your crontab: " + cmd,
			Exec:         cmd,
			Enablement:   "enabled",
			ActiveState:  "n/a",
			CronLine:     line,
		})
	}
	return items, nil
}

// BuildProtectionContext gathers dynamic protection facts.
func (d *Discoverer) BuildProtectionContext(ctx context.Context) ProtectionContext {
	pc := ProtectionContext{
		OverSSH:                d.Env.OverSSH,
		CriticalChain:          map[string]bool{},
		NetworkOnlineProviders: map[string]bool{},
		SessionUnits:           map[string]bool{},
		RootFSUnits:            map[string]bool{"-.mount": true},
	}
	sys := runner.Systemctl{R: d.R}

	// Units required by default.target's critical chain.
	if out, _, code, err := d.R.Run(ctx, "systemctl", "list-dependencies", "--no-pager", "--plain", "default.target"); err == nil && code == 0 {
		for _, line := range strings.Split(string(out), "\n") {
			name := strings.TrimSpace(line)
			if name != "" && !strings.HasSuffix(name, ".target") {
				// Only units pulled via Requires count as critical; refine
				// by checking RequiredBy below when rating. Here we record
				// the chain membership.
				pc.CriticalChain[name] = false || pc.CriticalChain[name]
			}
		}
		// Narrow to hard requirements: query default.target Requires.
		if props, err := sys.Show(ctx, "default.target", "Requires", "Requisite", "BindsTo"); err == nil {
			for _, list := range props {
				for _, u := range strings.Fields(list) {
					pc.CriticalChain[u] = true
				}
			}
		}
		// Drop the soft entries (false values).
		for k, v := range pc.CriticalChain {
			if !v {
				delete(pc.CriticalChain, k)
			}
		}
	}

	// network-online.target providers.
	if props, err := sys.Show(ctx, "network-online.target", "After", "Wants", "Requires"); err == nil {
		for _, key := range []string{"Wants", "Requires", "After"} {
			for _, u := range strings.Fields(props[key]) {
				if strings.HasSuffix(u, ".service") {
					pc.NetworkOnlineProviders[u] = true
				}
			}
		}
	}

	// The unit backing this very process/session.
	if out, _, code, err := d.R.Run(ctx, "systemctl", "--user", "show", "--property=Id", "--", "init.scope"); err == nil && code == 0 {
		_ = out
	}
	pc.SessionUnits["user@"+strconv.Itoa(d.Env.UID)+".service"] = true

	// Display manager binary.
	if props, err := sys.Show(ctx, "display-manager.service", "ExecStart", "Id"); err == nil {
		if bin := ExecBinary(firstExec(props["ExecStart"])); bin != "" {
			pc.DisplayManagerBinary = bin
		}
	}
	return pc
}

// Analyze applies all three safety layers to items in place.
func (d *Discoverer) Analyze(items []*StartupItem, pc ProtectionContext, g *Graph, queued map[string]bool) {
	for _, it := range items {
		prot := d.KB.CheckProtection(it.Name, ExecBinary(it.Exec), pc)
		it.Protected = prot.Protected
		it.ProtectedReason = prot.Reason
		if e := d.KB.MatchEntry(it.Name); e != nil {
			it.KBName = e.Name
			it.KBDescription = e.Description
			it.KBCategory = e.Category
			rec, note, reason := e.EvalConditions(d.FS, d.LookPath)
			it.Recommend = rec
			it.RecommendReason = reason
			it.HardwareNote = note
			if note != "" && rec == RecDisable {
				it.RecommendReason = note
			}
		} else if !it.Protected {
			it.Recommend = RecThink
			it.RecommendReason = "No knowledge-base entry for this item — review its description and dependents before deciding."
		}
		it.Risk = g.RateRisk(it, queued)
		it.Dependents = g.ActiveDependents(it, queued)
	}
}
