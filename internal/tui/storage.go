package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cmerk2021/sysprune/internal/apply"
	"github.com/cmerk2021/sysprune/internal/journal"
	"github.com/cmerk2021/sysprune/internal/startup"
	"github.com/cmerk2021/sysprune/internal/storage"
)

// storageModel drives the storage audit: category menu → background scan →
// card review per finding.
type storageModel struct {
	svc      Services
	styles   Styles
	scanner  *storage.Scanner
	cleaner  *storage.Cleaner
	trash    *storage.Trash
	jw       *journal.Writer
	session  string
	roots    []string
	menuIdx  int
	scanning bool
	findings []storage.Finding
	dups     []storage.DupGroup
	dupMode  bool
	dupIdx   int
	dupSel   map[string]bool
	idx      int
	reclaimed int64
	errText  string
	category storage.Category
	confirmPerm bool
}

type storageCat struct {
	cat   storage.Category
	title string
	desc  string
}

var storageCats = []storageCat{
	{storage.CatUserCache, "App caches (~/.cache)", "Per-app caches, regenerated automatically. Safe to delete."},
	{storage.CatThumbnails, "Thumbnail cache", "Image previews, regenerated as you browse."},
	{storage.CatToolchain, "Developer tool caches", "pip, npm, cargo, Go caches. Re-downloaded on demand."},
	{storage.CatTmp, "Old temp files", "Files in /tmp and /var/tmp untouched for a week."},
	{storage.CatJournalLogs, "System logs", "Vacuum old journal logs (needs root)."},
	{storage.CatPkgCache, "Package manager caches", "apt/dnf/pacman/flatpak caches (needs root)."},
	{storage.CatCrashDumps, "Crash dumps", "Old crash reports in /var/crash (needs root)."},
	{storage.CatContainers, "Docker/Podman cleanup", "Dangling images and build cache (needs root)."},
	{storage.CatLargeFiles, "Large files", "Files over the size threshold in your folders. Moved to Trash."},
	{storage.CatDuplicates, "Duplicate files", "Identical files in your folders. You choose what goes."},
	{storage.CatOldDownloads, "Old downloads", "Files in Downloads older than 90 days. Moved to Trash."},
	{storage.CatEmptyBroken, "Empty files & broken links", "Empty files/dirs and dead symlinks."},
	{storage.CatTrashEmpty, "Empty Trash", "Trash must be emptied to actually free space."},
}

func newStorage(svc Services, s Styles) storageModel {
	return storageModel{svc: svc, styles: s, dupSel: map[string]bool{}}
}

func (st *storageModel) initScan(m *Model) tea.Cmd {
	svc := m.svc
	st.scanner = &storage.Scanner{FS: svc.FS, R: svc.R, Home: svc.Home}
	st.trash = &storage.Trash{FS: svc.FS, Home: svc.Home}
	st.roots = storage.DefaultPersonalRoots(svc.FS, svc.Home)
	st.session = journal.NewSessionID(time.Now())
	st.reclaimed = 0
	return nil
}

func (st *storageModel) ensureJournal(m *Model) error {
	if st.jw != nil {
		return nil
	}
	w, err := journal.NewWriter(m.svc.FS, m.svc.Paths.Journal, st.session)
	if err != nil {
		return err
	}
	_ = w.Meta("storage session start")
	st.jw = w
	st.cleaner = &storage.Cleaner{FS: m.svc.FS, J: w, Trash: st.trash, Home: m.svc.Home}
	return nil
}

type scannedMsg struct {
	cat      storage.Category
	findings []storage.Finding
	dups     []storage.DupGroup
	err      error
}

func (m *Model) scanCmd(cat storage.Category) tea.Cmd {
	st := &m.storage
	ctx := m.ctx
	cfg := m.svc.Config
	return func() tea.Msg {
		s := st.scanner
		switch cat {
		case storage.CatUserCache:
			return scannedMsg{cat: cat, findings: s.ScanUserCache(ctx)}
		case storage.CatThumbnails:
			return scannedMsg{cat: cat, findings: s.ScanThumbnails(ctx)}
		case storage.CatToolchain:
			return scannedMsg{cat: cat, findings: s.ScanToolchainCaches(ctx)}
		case storage.CatTmp:
			return scannedMsg{cat: cat, findings: s.ScanTmp(ctx, cfg.TmpAgeDays)}
		case storage.CatJournalLogs:
			return scannedMsg{cat: cat, findings: s.ScanJournalLogs(ctx)}
		case storage.CatPkgCache:
			return scannedMsg{cat: cat, findings: s.ScanPkgCaches(ctx)}
		case storage.CatCrashDumps:
			return scannedMsg{cat: cat, findings: s.ScanCrashDumps(ctx)}
		case storage.CatContainers:
			return scannedMsg{cat: cat, findings: s.ScanContainers(ctx)}
		case storage.CatLargeFiles:
			return scannedMsg{cat: cat, findings: s.ScanLargeFiles(ctx, st.roots, int64(cfg.LargeFileThresholdMB)<<20)}
		case storage.CatDuplicates:
			groups, err := storage.FindDuplicates(ctx, m.svc.FS, m.svc.Home, st.roots, 4, nil)
			return scannedMsg{cat: cat, dups: groups, err: err}
		case storage.CatOldDownloads:
			dl := filepath.Join(m.svc.Home, "Downloads")
			if len(st.roots) > 0 {
				dl = st.roots[0]
			}
			return scannedMsg{cat: cat, findings: s.ScanOldDownloads(ctx, dl, cfg.DownloadsAgeDays)}
		case storage.CatEmptyBroken:
			return scannedMsg{cat: cat, findings: s.ScanEmptyAndBroken(ctx, st.roots)}
		case storage.CatTrashEmpty:
			size := st.trash.Size()
			return scannedMsg{cat: cat, findings: []storage.Finding{{
				Category: cat, Path: st.trash.FilesDir(), Size: size,
				Note: "Everything currently in your Trash."}}}
		}
		return scannedMsg{cat: cat}
	}
}

func (m *Model) updateStorage(msg tea.Msg) (tea.Model, tea.Cmd) {
	st := &m.storage
	switch msg := msg.(type) {
	case scannedMsg:
		st.scanning = false
		if msg.err != nil {
			st.errText = msg.err.Error()
			m.screen = ScreenStorageMenu
			return m, nil
		}
		st.findings = msg.findings
		st.dups = msg.dups
		st.dupMode = msg.cat == storage.CatDuplicates
		st.idx, st.dupIdx = 0, 0
		st.dupSel = map[string]bool{}
		m.screen = ScreenStorageReview
		return m, nil
	case tea.KeyMsg:
		switch m.screen {
		case ScreenStorageMenu:
			return m.updateStorageMenu(msg)
		case ScreenStorageScan:
			if msg.String() == "esc" || msg.String() == "q" {
				m.screen = ScreenStorageMenu
			}
		case ScreenStorageReview:
			return m.updateStorageReview(msg)
		}
	}
	return m, nil
}

func (m *Model) updateStorageMenu(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	st := &m.storage
	switch msg.String() {
	case "up", "k":
		if st.menuIdx > 0 {
			st.menuIdx--
		}
	case "down", "j":
		if st.menuIdx < len(storageCats)-1 {
			st.menuIdx++
		}
	case "esc", "q":
		m.screen = ScreenMenu
	case "enter":
		st.errText = ""
		st.category = storageCats[st.menuIdx].cat
		st.scanning = true
		m.screen = ScreenStorageScan
		return m, m.scanCmd(st.category)
	}
	return m, nil
}

func (m *Model) updateStorageReview(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	st := &m.storage
	if st.dupMode {
		return m.updateDupReview(msg)
	}
	n := len(st.findings)
	switch msg.String() {
	case "up", "k":
		if st.idx > 0 {
			st.idx--
		}
		st.confirmPerm = false
	case "down", "j":
		if st.idx < n-1 {
			st.idx++
		}
		st.confirmPerm = false
	case "esc", "q":
		st.confirmPerm = false
		m.screen = ScreenStorageMenu
	case "t": // trash (personal) / clean (regenerable local)
		if st.idx >= n {
			return m, nil
		}
		return m.doStorageAction(st.findings[st.idx], false)
	case "D": // permanent delete, explicit, confirmed
		if st.idx >= n {
			return m, nil
		}
		if !st.confirmPerm {
			st.confirmPerm = true
			return m, nil
		}
		st.confirmPerm = false
		return m.doStorageAction(st.findings[st.idx], true)
	default:
		st.confirmPerm = false
	}
	return m, nil
}

func (m *Model) doStorageAction(f storage.Finding, permanent bool) (tea.Model, tea.Cmd) {
	st := &m.storage
	st.errText = ""
	if m.svc.DryRun {
		st.errText = "dry run: would process " + f.Path
		return m, nil
	}
	if f.SystemOp != "" {
		// System-level cleanups (journal vacuum, package caches, docker
		// prune, crash dumps) go through the same validated, journaled
		// privileged helper flow as unit changes.
		change := startup.PlanChange{
			ItemID: "storage/" + string(f.Category), Kind: "clean",
			Scope: "system", Op: f.SystemOp, Param: f.Param,
		}
		if err := apply.ValidateChange(change, m.svc.KB); err != nil {
			st.errText = err.Error()
			return m, nil
		}
		plan := &startup.Plan{Version: startup.PlanVersion, SessionID: st.session,
			Changes: []startup.PlanChange{change}}
		if err := m.prepareElevationForPlan(plan, st.session); err != nil {
			st.errText = err.Error()
			return m, nil
		}
		m.result = resultModel{sessionID: st.session}
		m.screen = ScreenElevate
		return m, nil
	}
	if err := st.ensureJournal(m); err != nil {
		st.errText = err.Error()
		return m, nil
	}
	var err error
	switch {
	case f.Category == storage.CatTrashEmpty:
		var freed int64
		freed, err = st.trash.Empty()
		if err == nil {
			st.reclaimed += freed
		}
	case permanent || (f.Category.Regenerable() && f.SystemOp == ""):
		err = st.cleaner.DeletePermanently(f, st.roots, true)
		if err == nil {
			st.reclaimed += f.Size
		}
	default:
		err = st.cleaner.MoveToTrash(f, st.roots)
		if err == nil {
			st.reclaimed += f.Size // freed once Trash is emptied
		}
	}
	if err != nil {
		st.errText = err.Error()
		return m, nil
	}
	st.findings = append(st.findings[:st.idx], st.findings[st.idx+1:]...)
	if st.idx >= len(st.findings) && st.idx > 0 {
		st.idx--
	}
	return m, nil
}

func (m *Model) updateDupReview(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	st := &m.storage
	if len(st.dups) == 0 {
		if msg.String() == "esc" || msg.String() == "q" {
			m.screen = ScreenStorageMenu
		}
		return m, nil
	}
	g := st.dups[st.dupIdx]
	switch msg.String() {
	case "esc", "q":
		m.screen = ScreenStorageMenu
	case "left", "h":
		if st.dupIdx > 0 {
			st.dupIdx--
			st.idx = 0
		}
	case "right", "l":
		if st.dupIdx < len(st.dups)-1 {
			st.dupIdx++
			st.idx = 0
		}
	case "up", "k":
		if st.idx > 0 {
			st.idx--
		}
	case "down", "j":
		if st.idx < len(g.Files)-1 {
			st.idx++
		}
	case " ": // toggle mark (never auto-selected)
		f := g.Files[st.idx]
		st.dupSel[f.Path] = !st.dupSel[f.Path]
	case "t": // trash marked in this group
		if err := st.ensureJournal(m); err != nil {
			st.errText = err.Error()
			return m, nil
		}
		var kept []storage.DupFile
		for _, f := range g.Files {
			if st.dupSel[f.Path] {
				find := storage.Finding{Category: storage.CatDuplicates, Path: f.Path, Size: f.Size}
				if m.svc.DryRun {
					continue
				}
				if err := st.cleaner.MoveToTrash(find, st.roots); err != nil {
					st.errText = err.Error()
					kept = append(kept, f)
					continue
				}
				st.reclaimed += f.Size
				delete(st.dupSel, f.Path)
			} else {
				kept = append(kept, f)
			}
		}
		g.Files = kept
		if len(kept) < 2 {
			st.dups = append(st.dups[:st.dupIdx], st.dups[st.dupIdx+1:]...)
			if st.dupIdx >= len(st.dups) && st.dupIdx > 0 {
				st.dupIdx--
			}
		} else {
			st.dups[st.dupIdx] = g
		}
		st.idx = 0
	}
	return m, nil
}

func (m *Model) viewStorage() string {
	s := m.styles
	st := &m.storage
	switch m.screen {
	case ScreenStorageMenu:
		var b strings.Builder
		b.WriteString(s.Title.Render("Storage audit") + "\n")
		b.WriteString(s.Dim.Render("Scanning is read-only. Personal files go to Trash by default.") + "\n")
		if st.reclaimed > 0 {
			b.WriteString(s.Good.Render("Space reclaimed this session: "+storage.HumanSize(st.reclaimed)) + "\n")
		}
		b.WriteString("\n")
		for i, c := range storageCats {
			line := c.title
			if i == st.menuIdx {
				line = s.Selected.Render(line)
				b.WriteString(s.Key.Render("> ") + line + "\n    " + s.Dim.Render(c.desc) + "\n")
			} else {
				b.WriteString("  " + line + "\n")
			}
		}
		if st.errText != "" {
			b.WriteString("\n" + s.Warn.Render(wrap(st.errText, m.width-4)) + "\n")
		}
		b.WriteString("\n" + s.Dim.Render("enter scan category · esc back"))
		return b.String()
	case ScreenStorageScan:
		return "\n  " + s.Title.Render("Scanning…") + "\n\n  " + s.Dim.Render("Read-only scan in progress. esc to cancel.")
	case ScreenStorageReview:
		if st.dupMode {
			return m.viewDupReview()
		}
		var b strings.Builder
		b.WriteString(s.Title.Render("Findings — "+string(st.category)) + "  " +
			s.Good.Render("reclaimed "+storage.HumanSize(st.reclaimed)) + "\n\n")
		if len(st.findings) == 0 {
			b.WriteString(s.Dim.Render("Nothing (left) to clean here. 🎉") + "\n\n" + s.Dim.Render("esc back"))
			return b.String()
		}
		maxRows := maxInt(m.height-12, 4)
		start := 0
		if st.idx >= maxRows {
			start = st.idx - maxRows + 1
		}
		for i := start; i < minInt(len(st.findings), start+maxRows); i++ {
			f := st.findings[i]
			line := fmt.Sprintf("%-9s %s", storage.HumanSize(f.Size), f.Path)
			if i == st.idx {
				line = s.Selected.Render(line)
			}
			b.WriteString("  " + line + "\n")
		}
		f := st.findings[st.idx]
		b.WriteString("\n" + wrap(f.Note, m.width-4) + "\n")
		if st.confirmPerm {
			b.WriteString(s.Err.Render(wrap(fmt.Sprintf("⚠ Permanently delete %s (%s)? This cannot be undone. Press D again to confirm.", f.Path, storage.HumanSize(f.Size)), m.width-4)) + "\n")
		}
		if st.errText != "" {
			b.WriteString(s.Warn.Render(wrap(st.errText, m.width-4)) + "\n")
		}
		action := "t move to Trash"
		if f.Category.Regenerable() {
			action = "t delete (regenerable)"
		}
		if f.Category == storage.CatTrashEmpty {
			action = "t empty Trash now"
		}
		b.WriteString("\n" + s.Dim.Render("↑/↓ select · "+s.Key.Render("t")+" "+action[2:]+" · "+s.Key.Render("D")+" delete permanently · esc back"))
		return b.String()
	}
	return ""
}

func (m *Model) viewDupReview() string {
	s := m.styles
	st := &m.storage
	var b strings.Builder
	b.WriteString(s.Title.Render("Duplicate files") + "  " + s.Good.Render("reclaimed "+storage.HumanSize(st.reclaimed)) + "\n\n")
	if len(st.dups) == 0 {
		b.WriteString(s.Dim.Render("No duplicate groups (left).") + "\n\n" + s.Dim.Render("esc back"))
		return b.String()
	}
	g := st.dups[st.dupIdx]
	b.WriteString(fmt.Sprintf("Group %d of %d · %s each · %s wasted\n\n",
		st.dupIdx+1, len(st.dups), storage.HumanSize(g.Size), storage.HumanSize(g.Wasted())))
	for i, f := range g.Files {
		mark := "[ ]"
		if st.dupSel[f.Path] {
			mark = s.Warn.Render("[x]")
		}
		keeper := ""
		if f.Keeper {
			keeper = s.Good.Render(" ← suggested keeper")
		}
		line := mark + " " + f.Path + keeper
		if i == st.idx {
			line = s.Selected.Render(mark+" "+f.Path) + keeper
		}
		b.WriteString("  " + line + "\n")
	}
	if st.errText != "" {
		b.WriteString("\n" + s.Warn.Render(wrap(st.errText, m.width-4)) + "\n")
	}
	b.WriteString("\n" + s.Dim.Render("space mark · t trash marked · ←/→ group · ↑/↓ file · esc back\nNothing is ever auto-selected. The suggested keeper is only a suggestion."))
	return b.String()
}
