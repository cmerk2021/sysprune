package runner

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// Call records one invocation seen by FakeRunner.
type Call struct {
	Name string
	Args []string
}

func (c Call) String() string { return c.Name + " " + strings.Join(c.Args, " ") }

// Response is a scripted reply for FakeRunner.
type Response struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Err      error
	// MatchSuffix, when set on a prefix-scripted response, additionally
	// requires the space-joined command line to end with this suffix.
	MatchSuffix string
}

// FakeRunner returns scripted responses keyed by the full command line and
// records every call. Safe for concurrent use.
type FakeRunner struct {
	mu        sync.Mutex
	responses map[string]Response
	prefixes  []prefixResp
	Calls     []Call
	// Default is returned for unscripted commands.
	Default    Response
	DefaultErr error
	// Strict makes unscripted commands return an error.
	Strict bool
}

type prefixResp struct {
	prefix string
	resp   Response
}

func NewFake() *FakeRunner {
	return &FakeRunner{responses: map[string]Response{}}
}

func key(name string, args []string) string {
	return name + "\x00" + strings.Join(args, "\x00")
}

// Script registers an exact-match response for name+args.
func (f *FakeRunner) Script(name string, args []string, r Response) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responses[key(name, args)] = r
}

// ScriptOutput is a convenience for a successful command with stdout.
func (f *FakeRunner) ScriptOutput(name string, args []string, stdout string) {
	f.Script(name, args, Response{Stdout: []byte(stdout)})
}

// ScriptPrefix matches any call whose space-joined command line starts with
// prefix. Exact matches win over prefixes.
func (f *FakeRunner) ScriptPrefix(prefix string, r Response) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prefixes = append(f.prefixes, prefixResp{prefix, r})
}

func (f *FakeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, []byte, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, -1, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, Call{Name: name, Args: append([]string(nil), args...)})
	if r, ok := f.responses[key(name, args)]; ok {
		return r.Stdout, r.Stderr, r.ExitCode, r.Err
	}
	line := name + " " + strings.Join(args, " ")
	for _, p := range f.prefixes {
		if strings.HasPrefix(line, p.prefix) &&
			(p.resp.MatchSuffix == "" || strings.HasSuffix(line, p.resp.MatchSuffix)) {
			return p.resp.Stdout, p.resp.Stderr, p.resp.ExitCode, p.resp.Err
		}
	}
	if f.Strict {
		return nil, nil, -1, fmt.Errorf("fakerunner: unscripted command: %s", line)
	}
	if f.DefaultErr != nil {
		return nil, nil, -1, f.DefaultErr
	}
	return f.Default.Stdout, f.Default.Stderr, f.Default.ExitCode, f.Default.Err
}

// CalledWith reports whether any recorded call matches name and args exactly.
func (f *FakeRunner) CalledWith(name string, args ...string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := key(name, args)
	for _, c := range f.Calls {
		if key(c.Name, c.Args) == k {
			return true
		}
	}
	return false
}

// CallLines returns every recorded call as a space-joined string.
func (f *FakeRunner) CallLines() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.Calls))
	for i, c := range f.Calls {
		out[i] = c.String()
	}
	return out
}
