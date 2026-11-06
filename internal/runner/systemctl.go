package runner

import (
	"context"
	"fmt"
	"strings"
)

// Systemctl is a thin typed client over Runner for systemctl invocations.
type Systemctl struct {
	R    Runner
	User bool // --user mode
}

func (s Systemctl) base() []string {
	if s.User {
		return []string{"--user"}
	}
	return nil
}

func (s Systemctl) run(ctx context.Context, args ...string) ([]byte, int, error) {
	all := append(s.base(), args...)
	out, _, code, err := s.R.Run(ctx, "systemctl", all...)
	return out, code, err
}

// ListUnitFiles returns unit-file name -> enablement state.
func (s Systemctl) ListUnitFiles(ctx context.Context, patterns ...string) (map[string]string, error) {
	args := []string{"list-unit-files", "--no-legend", "--no-pager", "--plain"}
	args = append(args, patterns...)
	out, code, err := s.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("systemctl list-unit-files exited %d", code)
	}
	res := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			res[fields[0]] = fields[1]
		}
	}
	return res, nil
}

// Show returns the requested properties of a unit as a map.
func (s Systemctl) Show(ctx context.Context, unit string, props ...string) (map[string]string, error) {
	args := []string{"show", "--no-pager"}
	if len(props) > 0 {
		args = append(args, "--property="+strings.Join(props, ","))
	}
	args = append(args, "--", unit)
	out, code, err := s.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("systemctl show %s exited %d", unit, code)
	}
	res := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if i := strings.IndexByte(line, '='); i > 0 {
			res[line[:i]] = line[i+1:]
		}
	}
	return res, nil
}

// IsActive returns the active state string ("active", "inactive", ...).
func (s Systemctl) IsActive(ctx context.Context, unit string) (string, error) {
	out, _, err := s.run(ctx, "is-active", "--", unit)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Disable, Enable, Stop, Start, Mask, Unmask run the respective verb.
func (s Systemctl) verb(ctx context.Context, verb, unit string) error {
	_, stderr, code, err := s.R.Run(ctx, "systemctl", append(s.base(), verb, "--", unit)...)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("systemctl %s %s exited %d: %s", verb, unit, code, strings.TrimSpace(string(stderr)))
	}
	return nil
}

func (s Systemctl) Disable(ctx context.Context, unit string) error { return s.verb(ctx, "disable", unit) }
func (s Systemctl) Enable(ctx context.Context, unit string) error  { return s.verb(ctx, "enable", unit) }
func (s Systemctl) Stop(ctx context.Context, unit string) error    { return s.verb(ctx, "stop", unit) }
func (s Systemctl) Start(ctx context.Context, unit string) error   { return s.verb(ctx, "start", unit) }
func (s Systemctl) Mask(ctx context.Context, unit string) error    { return s.verb(ctx, "mask", unit) }
func (s Systemctl) Unmask(ctx context.Context, unit string) error  { return s.verb(ctx, "unmask", unit) }

// AnalyzeBlame returns unit -> activation time in milliseconds from
// `systemd-analyze blame`. Missing systemd-analyze returns an empty map.
func AnalyzeBlame(ctx context.Context, r Runner, user bool) map[string]float64 {
	args := []string{"blame", "--no-pager"}
	if user {
		args = append([]string{"--user"}, args...)
	}
	out, _, code, err := r.Run(ctx, "systemd-analyze", args...)
	res := map[string]float64{}
	if err != nil || code != 0 {
		return res
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		unit := fields[len(fields)-1]
		var ms float64
		for _, f := range fields[:len(fields)-1] {
			ms += parseDuration(f)
		}
		if ms > 0 {
			res[unit] = ms
		}
	}
	return res
}

func parseDuration(s string) float64 {
	switch {
	case strings.HasSuffix(s, "ms"):
		return parseFloat(strings.TrimSuffix(s, "ms"))
	case strings.HasSuffix(s, "min"):
		return parseFloat(strings.TrimSuffix(s, "min")) * 60_000
	case strings.HasSuffix(s, "s"):
		return parseFloat(strings.TrimSuffix(s, "s")) * 1000
	}
	return 0
}

func parseFloat(s string) float64 {
	var v float64
	_, _ = fmt.Sscanf(s, "%g", &v)
	return v
}
