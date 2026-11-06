package startup

import (
	"fmt"
	"path"

	"gopkg.in/yaml.v3"

	"github.com/cmerk2021/sysprune/internal/runner"
)

// KBCondition is a hardware/system check attached to a KB entry.
type KBCondition struct {
	Type      string `yaml:"type"` // dir_empty, dir_missing, file_missing, file_exists, cmd_absent
	Path      string `yaml:"path"`
	Cmd       string `yaml:"cmd"`
	Recommend string `yaml:"recommend"`
	Note      string `yaml:"note"`
}

// KBEntry is one knowledge-base record.
type KBEntry struct {
	Match       string        `yaml:"match"`
	Name        string        `yaml:"name"`
	Description string        `yaml:"description"`
	Category    string        `yaml:"category"`
	Recommend   string        `yaml:"recommend"`
	Reason      string        `yaml:"reason"`
	Conditions  []KBCondition `yaml:"conditions"`
}

// KBProtected is one hard-locked protection pattern.
type KBProtected struct {
	Pattern     string `yaml:"pattern"`
	Reason      string `yaml:"reason"`
	Conditional string `yaml:"conditional"` // ssh | timesync | firewall | ""
}

// KB is the loaded knowledge base.
type KB struct {
	Protected []KBProtected `yaml:"protected"`
	Entries   []KBEntry     `yaml:"entries"`
}

// LoadKB parses the embedded KB and merges an optional user override from
// overridePath. User overrides may ADD protection patterns and add/replace
// entries, but can never remove embedded protection patterns.
func LoadKB(embedded []byte, fsys runner.FS, overridePath string) (*KB, error) {
	var kb KB
	if err := yaml.Unmarshal(embedded, &kb); err != nil {
		return nil, fmt.Errorf("embedded kb.yaml: %w", err)
	}
	if overridePath != "" && fsys != nil {
		if data, err := fsys.ReadFile(overridePath); err == nil {
			var user KB
			if err := yaml.Unmarshal(data, &user); err != nil {
				return nil, fmt.Errorf("user kb override %s: %w", overridePath, err)
			}
			// Protection entries are append-only: embedded ones always stay.
			kb.Protected = append(kb.Protected, user.Protected...)
			// User entries override embedded entries with the same match.
			byMatch := map[string]int{}
			for i, e := range kb.Entries {
				byMatch[e.Match] = i
			}
			for _, e := range user.Entries {
				if i, ok := byMatch[e.Match]; ok {
					kb.Entries[i] = e
				} else {
					kb.Entries = append(kb.Entries, e)
				}
			}
		}
	}
	return &kb, nil
}

// globMatch matches name against pattern (filepath.Match-style glob, plus
// exact match).
func globMatch(pattern, name string) bool {
	if pattern == name {
		return true
	}
	ok, err := path.Match(pattern, name)
	return err == nil && ok
}

// MatchEntry finds the first KB entry matching the item name. Exact matches
// win over glob matches.
func (kb *KB) MatchEntry(name string) *KBEntry {
	for i := range kb.Entries {
		if kb.Entries[i].Match == name {
			return &kb.Entries[i]
		}
	}
	for i := range kb.Entries {
		if globMatch(kb.Entries[i].Match, name) {
			return &kb.Entries[i]
		}
	}
	return nil
}

// EvalConditions evaluates an entry's conditions against the system via fsys
// and returns the effective recommendation plus a hardware note. The first
// matching condition wins; with no match the entry defaults apply.
func (e *KBEntry) EvalConditions(fsys runner.FS, lookPath func(string) (string, error)) (Recommendation, string, string) {
	for _, c := range e.Conditions {
		hit := false
		switch c.Type {
		case "dir_empty":
			entries, err := fsys.ReadDir(c.Path)
			hit = err == nil && len(entries) == 0
			if err != nil {
				// A missing directory also means "no such hardware".
				hit = true
			}
		case "dir_missing":
			_, err := fsys.Stat(c.Path)
			hit = err != nil
		case "file_missing":
			_, err := fsys.Stat(c.Path)
			hit = err != nil
		case "file_exists":
			info, err := fsys.Stat(c.Path)
			hit = err == nil && !info.IsDir()
		case "cmd_absent":
			if lookPath != nil {
				_, err := lookPath(c.Cmd)
				hit = err != nil
			}
		}
		if hit {
			rec := Recommendation(c.Recommend)
			if rec == "" {
				rec = Recommendation(e.Recommend)
			}
			return rec, c.Note, e.Reason
		}
	}
	return Recommendation(e.Recommend), "", e.Reason
}
