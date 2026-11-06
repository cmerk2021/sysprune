// Package journal implements the append-only write-ahead journal that makes
// every change sysprune performs reversible and crash-safe.
package journal

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cmerk2021/sysprune/internal/runner"
)

// Record types. An "intent" record is written BEFORE each operation executes;
// a "done" or "failed" record is appended after.
const (
	TypeMeta   = "meta"
	TypeIntent = "intent"
	TypeDone   = "done"
	TypeFailed = "failed"
)

// Operation describes a single reversible change.
type Operation struct {
	ID    string `json:"id"`              // stable item id (e.g. "unit/system/foo.service")
	Kind  string `json:"kind"`            // unit | xdg | cron | trash | delete | clean
	Scope string `json:"scope"`           // user | system
	Op    string `json:"op"`              // disable, stop, mask, hide-xdg, move-xdg, comment-cron, trash, delete, vacuum-journal, prune, clean-cache
	Unit  string `json:"unit,omitempty"`  // systemd unit name
	Exec  string `json:"exec,omitempty"`  // exact command, space-joined, informational
	Prior struct {
		Enabled string `json:"enabled,omitempty"` // enabled | disabled | masked | static ...
		Active  string `json:"active,omitempty"`  // active | inactive ...
	} `json:"prior"`
	SrcPath    string `json:"src_path,omitempty"`    // xdg/trash: original path
	DstPath    string `json:"dst_path,omitempty"`    // xdg/trash: where it went
	BackupPath string `json:"backup_path,omitempty"` // cron: crontab backup file
	Bytes      int64  `json:"bytes,omitempty"`       // storage: size affected
}

// Record is one JSONL line in a session journal.
type Record struct {
	Type  string    `json:"type"`
	Seq   int       `json:"seq"`
	Time  time.Time `json:"time"`
	Note  string    `json:"note,omitempty"`
	Error string    `json:"error,omitempty"`
	Op    *Operation `json:"op,omitempty"`
}

// Writer appends records for one apply session.
type Writer struct {
	fs   runner.FS
	path string
	seq  int
	// Mirror, when set, receives a copy of every record (used by the
	// privileged helper to mirror the system journal into the user's state).
	Mirror string
}

// NewSessionID returns "<timestamp>-<random>".
func NewSessionID(now time.Time) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return now.UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b)
}

// NewWriter creates the journal file for a session in dir.
func NewWriter(fsys runner.FS, dir, sessionID string) (*Writer, error) {
	if err := fsys.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	w := &Writer{fs: fsys, path: filepath.Join(dir, sessionID+".jsonl")}
	return w, nil
}

func (w *Writer) Path() string { return w.path }

func (w *Writer) append(rec Record) error {
	rec.Seq = w.seq
	w.seq++
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := w.fs.AppendFile(w.path, data, 0o600); err != nil {
		return err
	}
	if w.Mirror != "" {
		_ = w.fs.MkdirAll(filepath.Dir(w.Mirror), 0o700)
		_ = w.fs.AppendFile(w.Mirror, data, 0o600)
	}
	return nil
}

func (w *Writer) Meta(note string) error {
	return w.append(Record{Type: TypeMeta, Time: time.Now(), Note: note})
}

// Intent MUST be called before executing op.
func (w *Writer) Intent(op Operation) error {
	o := op
	return w.append(Record{Type: TypeIntent, Time: time.Now(), Op: &o})
}

func (w *Writer) Done(op Operation) error {
	o := op
	return w.append(Record{Type: TypeDone, Time: time.Now(), Op: &o})
}

func (w *Writer) Failed(op Operation, opErr error) error {
	o := op
	msg := ""
	if opErr != nil {
		msg = opErr.Error()
	}
	return w.append(Record{Type: TypeFailed, Time: time.Now(), Op: &o, Error: msg})
}

// Session is a parsed journal file.
type Session struct {
	ID      string
	Path    string
	Records []Record
}

// Completed returns operations that have a done record, in execution order.
func (s *Session) Completed() []Operation {
	var out []Operation
	for _, r := range s.Records {
		if r.Type == TypeDone && r.Op != nil {
			out = append(out, *r.Op)
		}
	}
	return out
}

// Incomplete returns intents with no matching done/failed record — the sign
// of a crash mid-apply.
func (s *Session) Incomplete() []Operation {
	settled := map[string]bool{}
	for _, r := range s.Records {
		if (r.Type == TypeDone || r.Type == TypeFailed) && r.Op != nil {
			settled[r.Op.ID+"\x00"+r.Op.Op] = true
		}
	}
	var out []Operation
	for _, r := range s.Records {
		if r.Type == TypeIntent && r.Op != nil && !settled[r.Op.ID+"\x00"+r.Op.Op] {
			out = append(out, *r.Op)
		}
	}
	return out
}

// Restored reports whether this session has already been restored.
func (s *Session) Restored() bool {
	for _, r := range s.Records {
		if r.Type == TypeMeta && strings.HasPrefix(r.Note, "restored") {
			return true
		}
	}
	return false
}

// Load parses one session journal file.
func Load(fsys runner.FS, path string) (*Session, error) {
	data, err := fsys.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s := &Session{Path: path, ID: strings.TrimSuffix(filepath.Base(path), ".jsonl")}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue // tolerate a torn final line after a crash
		}
		s.Records = append(s.Records, rec)
	}
	return s, nil
}

// List returns all sessions in dir (and extraDirs), newest first.
func List(fsys runner.FS, dirs ...string) ([]*Session, error) {
	var sessions []*Session
	seen := map[string]bool{}
	for _, dir := range dirs {
		entries, err := fsys.ReadDir(dir)
		if err != nil {
			continue // missing dir is fine
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			id := strings.TrimSuffix(e.Name(), ".jsonl")
			if seen[id] {
				continue
			}
			seen[id] = true
			s, err := Load(fsys, filepath.Join(dir, e.Name()))
			if err == nil {
				sessions = append(sessions, s)
			}
		}
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].ID > sessions[j].ID })
	return sessions, nil
}

// Find locates a session by ID (or prefix) across dirs.
func Find(fsys runner.FS, id string, dirs ...string) (*Session, error) {
	sessions, err := List(fsys, dirs...)
	if err != nil {
		return nil, err
	}
	for _, s := range sessions {
		if s.ID == id || strings.HasPrefix(s.ID, id) {
			return s, nil
		}
	}
	return nil, fmt.Errorf("session %q not found", id)
}
