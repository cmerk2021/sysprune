// Package runner provides the single boundary through which sysprune
// executes external commands. No other package may use os/exec directly.
package runner

import (
	"bytes"
	"context"
	"os"
	"os/exec"
)

// Runner executes an external command without a shell. Arguments are always
// passed as an argv array; there is no shell interpretation anywhere.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (stdout, stderr []byte, exitCode int, err error)
}

// Real is the production Runner backed by os/exec.
type Real struct {
	// Env, when non-nil, replaces the child environment entirely.
	Env []string
}

// Run executes name with args. exitCode is -1 when the process could not be
// started. A non-zero exit status is NOT returned as err; callers inspect
// exitCode.
func (r *Real) Run(ctx context.Context, name string, args ...string) ([]byte, []byte, int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if r.Env != nil {
		cmd.Env = r.Env
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
			err = nil // exit status conveyed via code
		} else {
			code = -1
		}
	}
	return out.Bytes(), errb.Bytes(), code, err
}

// Command constructs an *exec.Cmd for interactive handover (e.g. Bubble
// Tea's ExecProcess for sudo password prompts). This keeps os/exec usage
// confined to the runner package.
func Command(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}

// LookPath resolves a binary in PATH (wrapper so other packages never import
// os/exec directly).
func LookPath(name string) (string, error) { return exec.LookPath(name) }

// RunInteractive runs a command attached to the current terminal (stdin,
// stdout, stderr inherited). Used for sudo/pkexec outside the TUI.
func RunInteractive(name string, args ...string) (exitCode int, err error) {
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), nil
		}
		return -1, err
	}
	return 0, nil
}
