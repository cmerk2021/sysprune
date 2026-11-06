// Package elevate handles the single sudo/pkexec re-exec used to apply
// system-scope changes. It never escalates silently.
package elevate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cmerk2021/sysprune/internal/runner"
)

// Method describes how escalation will happen.
type Method string

const (
	MethodSudo   Method = "sudo"
	MethodPkexec Method = "pkexec"
	MethodNone   Method = "none" // print manual commands instead
)

// SelfPath resolves the running binary's absolute, symlink-free path. We
// never rely on $PATH: sudo's secure_path will not include ~/.local/bin.
func SelfPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(resolved) {
		return "", fmt.Errorf("executable path %q is not absolute", resolved)
	}
	return resolved, nil
}

// Detect picks the escalation method: sudo first, then pkexec.
func Detect(lookPath func(string) (string, error)) Method {
	if _, err := lookPath("sudo"); err == nil {
		return MethodSudo
	}
	if _, err := lookPath("pkexec"); err == nil {
		return MethodPkexec
	}
	return MethodNone
}

// Command builds the exact argv for escalated helper invocation.
func Command(m Method, selfPath, planPath string) []string {
	helper := []string{selfPath, "--apply-helper", planPath}
	switch m {
	case MethodSudo:
		return append([]string{"sudo", "--"}, helper...)
	case MethodPkexec:
		return append([]string{"pkexec"}, helper...)
	}
	return helper
}

// Describe renders the escalation for user display before it happens.
func Describe(m Method, selfPath, planPath string) string {
	return strings.Join(Command(m, selfPath, planPath), " ")
}

// Run performs the one escalation. stdout of the helper (the JSON result) is
// returned. The caller must have shown the user what will run.
func Run(ctx context.Context, r runner.Runner, m Method, selfPath, planPath string) (stdout []byte, exitCode int, err error) {
	argv := Command(m, selfPath, planPath)
	out, _, code, err := r.Run(ctx, argv[0], argv[1:]...)
	return out, code, err
}

// ManualInstructions renders the fallback when neither sudo nor pkexec works.
func ManualInstructions(selfPath, planPath string) string {
	var b strings.Builder
	b.WriteString("Neither sudo nor pkexec is available (or permitted).\n")
	b.WriteString("Your plan has been saved. To apply the system-scope changes, run as root:\n\n")
	b.WriteString("    " + selfPath + " --apply-helper " + planPath + "\n\n")
	b.WriteString("(The plan file contains only the reviewed changes and is re-validated before anything runs.)\n")
	return b.String()
}
