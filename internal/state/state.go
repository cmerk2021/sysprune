// Package state handles XDG paths, saved decisions, and configuration.
package state

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/cmerk2021/sysprune/internal/runner"
)

// AppName is defined in internal/app; state receives it to build paths so the
// binary can be renamed by changing a single constant.
type Paths struct {
	StateDir  string // ~/.local/state/<app>
	ConfigDir string // ~/.config/<app>
	Journal   string // <state>/journal
	Backups   string // <state>/backups
	LogFile   string // <state>/<app>.log
}

// DefaultPaths resolves XDG paths from the environment.
func DefaultPaths(appName string) Paths {
	home, _ := os.UserHomeDir()
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		stateHome = filepath.Join(home, ".local", "state")
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	sd := filepath.Join(stateHome, appName)
	return Paths{
		StateDir:  sd,
		ConfigDir: filepath.Join(configHome, appName),
		Journal:   filepath.Join(sd, "journal"),
		Backups:   filepath.Join(sd, "backups"),
		LogFile:   filepath.Join(sd, appName+".log"),
	}
}

// DecisionKey identifies an item for the "keep, don't ask again" memory.
type DecisionKey struct {
	Kind  string `json:"kind"`
	Scope string `json:"scope"`
	Name  string `json:"name"`
}

func (k DecisionKey) String() string { return k.Kind + "/" + k.Scope + "/" + k.Name }

// Decisions stores persistent "keep, don't ask again" choices.
type Decisions struct {
	mu   sync.Mutex
	fs   runner.FS
	path string
	keep map[string]bool
}

func LoadDecisions(fsys runner.FS, dir string) (*Decisions, error) {
	d := &Decisions{fs: fsys, path: filepath.Join(dir, "decisions.json"), keep: map[string]bool{}}
	data, err := fsys.ReadFile(d.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return d, nil
		}
		return nil, err
	}
	var raw struct {
		Keep []string `json:"keep"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		// Corrupt decisions file: start fresh rather than failing the app.
		return d, nil
	}
	for _, k := range raw.Keep {
		d.keep[k] = true
	}
	return d, nil
}

func (d *Decisions) IsKept(k DecisionKey) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.keep[k.String()]
}

func (d *Decisions) Keep(k DecisionKey) error {
	d.mu.Lock()
	d.keep[k.String()] = true
	d.mu.Unlock()
	return d.save()
}

func (d *Decisions) Count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.keep)
}

func (d *Decisions) Reset() error {
	d.mu.Lock()
	d.keep = map[string]bool{}
	d.mu.Unlock()
	return d.save()
}

func (d *Decisions) save() error {
	d.mu.Lock()
	keys := make([]string, 0, len(d.keep))
	for k := range d.keep {
		keys = append(keys, k)
	}
	d.mu.Unlock()
	sort.Strings(keys)
	data, err := json.MarshalIndent(struct {
		Keep []string `json:"keep"`
	}{Keep: keys}, "", "  ")
	if err != nil {
		return err
	}
	if err := d.fs.MkdirAll(filepath.Dir(d.path), 0o700); err != nil {
		return err
	}
	return d.fs.WriteFile(d.path, data, 0o600)
}

// Config holds user configuration.
type Config struct {
	LargeFileThresholdMB int `json:"large_file_threshold_mb" yaml:"large_file_threshold_mb"`
	DownloadsAgeDays     int `json:"downloads_age_days" yaml:"downloads_age_days"`
	TmpAgeDays           int `json:"tmp_age_days" yaml:"tmp_age_days"`
}

func DefaultConfig() Config {
	return Config{LargeFileThresholdMB: 500, DownloadsAgeDays: 90, TmpAgeDays: 7}
}

func LoadConfig(fsys runner.FS, dir string) Config {
	cfg := DefaultConfig()
	data, err := fsys.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return cfg
	}
	_ = json.Unmarshal(data, &cfg)
	if cfg.LargeFileThresholdMB <= 0 {
		cfg.LargeFileThresholdMB = 500
	}
	if cfg.DownloadsAgeDays <= 0 {
		cfg.DownloadsAgeDays = 90
	}
	if cfg.TmpAgeDays <= 0 {
		cfg.TmpAgeDays = 7
	}
	return cfg
}
