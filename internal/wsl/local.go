package wsl

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// Runner executes an external command. It exists so Local can be tested
// without wsl.exe: every wsl.exe invocation in the product funnels through here.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
	Start(ctx context.Context, name string, args ...string) (stop func(), err error)
}

// Local drives the real wsl.exe.
type Local struct {
	// Exe is the wsl.exe path. Empty resolves through PATH.
	Exe string
	// Runner executes commands. Empty uses the real process runner.
	Runner Runner
}

// NewLocal returns a Local backed by the real wsl.exe.
func NewLocal() *Local { return &Local{} }

var _ WSL = (*Local)(nil)

func (l *Local) exe() string {
	if l.Exe != "" {
		return l.Exe
	}
	return "wsl.exe"
}

func (l *Local) runner() Runner {
	if l.Runner != nil {
		return l.Runner
	}
	return execRunner{}
}

func (l *Local) run(ctx context.Context, args ...string) (string, error) {
	out, err := l.runner().Run(ctx, l.exe(), args...)
	text := decodeOutput(out)
	if err != nil {
		// wsl.exe puts the useful part in its output, not the exit status.
		if text != "" {
			return text, fmt.Errorf("wsl %s: %w: %s",
				strings.Join(args, " "), err, firstLine(text))
		}
		return text, fmt.Errorf("wsl %s: %w", strings.Join(args, " "), err)
	}
	return text, nil
}

func firstLine(s string) string {
	if lines := cleanLines(s); len(lines) > 0 {
		return lines[0]
	}
	return ""
}

// versionRe matches the release in `wsl --version` output. The label is
// localized, so the version pattern is what gets matched, not the words.
var versionRe = regexp.MustCompile(`(\d+\.\d+\.\d+(?:\.\d+)?)`)

// defaultVersionRe matches the trailing digit of the default-version line.
var defaultVersionRe = regexp.MustCompile(`:\s*(\d)\s*$`)

// Status reports WSL availability. A missing wsl.exe is reported as
// not-installed rather than as an error: the caller's job is to print
// enable-and-reboot instructions, not to crash (PLAN §05 preflight).
func (l *Local) Status(ctx context.Context) (Status, error) {
	var st Status

	if out, err := l.run(ctx, "--version"); err == nil {
		// First line carries the WSL release; later lines are kernel, WSLg etc.
		if lines := cleanLines(out); len(lines) > 0 {
			if m := versionRe.FindStringSubmatch(lines[0]); m != nil {
				st.Version = m[1]
			}
		}
		st.Installed = true
	}

	out, err := l.run(ctx, "--status")
	if err != nil {
		if !st.Installed {
			// Neither command worked: treat WSL as absent, not as a failure.
			return Status{}, nil
		}
		return st, nil
	}
	st.Installed = true

	for _, line := range cleanLines(out) {
		if m := defaultVersionRe.FindStringSubmatch(line); m != nil {
			if v, convErr := strconv.Atoi(m[1]); convErr == nil {
				st.DefaultVersion = v
			}
		}
	}
	return st, nil
}

// Import registers a distro as WSL 2 from a rootfs tarball.
func (l *Local) Import(ctx context.Context, distro, installDir, rootfsPath string) error {
	if distro == "" || installDir == "" || rootfsPath == "" {
		return fmt.Errorf("wsl: Import needs distro, installDir and rootfsPath")
	}
	_, err := l.run(ctx, "--import", distro, installDir, rootfsPath, "--version", "2")
	return err
}

// Export writes a distro's filesystem to a tarball. wsl terminates the distro
// as needed for a consistent export.
func (l *Local) Export(ctx context.Context, distro, tarPath string) error {
	if distro == "" || tarPath == "" {
		return fmt.Errorf("wsl: Export needs distro and tarPath")
	}
	_, err := l.run(ctx, "--export", distro, tarPath)
	return err
}

// Unregister removes a distro and deletes its VHDX. Irreversible.
func (l *Local) Unregister(ctx context.Context, distro string) error {
	if distro == "" {
		return fmt.Errorf("wsl: Unregister needs a distro name")
	}
	_, err := l.run(ctx, "--unregister", distro)
	return err
}

// Terminate stops a running distro, leaving it registered.
func (l *Local) Terminate(ctx context.Context, distro string) error {
	if distro == "" {
		return fmt.Errorf("wsl: Terminate needs a distro name")
	}
	_, err := l.run(ctx, "--terminate", distro)
	return err
}

// List returns the distros registered for the current user.
//
// Registration is per-user (HKCU\...\Lxss), so this deliberately reports what
// *this* account can see — the distinction Spike B (#3) exists to measure.
func (l *Local) List(ctx context.Context) ([]Distro, error) {
	out, err := l.run(ctx, "--list", "--verbose")
	if err != nil {
		// A machine with no distros exits non-zero; that is an empty list.
		if strings.Contains(strings.ToLower(out), "no installed distributions") {
			return nil, nil
		}
		return nil, err
	}
	return parseListVerbose(out), nil
}

// parseListVerbose reads the `wsl --list --verbose` table.
//
// The header is localized, so it is identified by structure (the first line,
// which never starts with the default marker) rather than by matching English.
func parseListVerbose(out string) []Distro {
	lines := cleanLines(out)
	var distros []Distro
	for i, line := range lines {
		isDefault := strings.HasPrefix(line, "*")
		row := strings.TrimSpace(strings.TrimPrefix(line, "*"))
		fields := strings.Fields(row)
		if len(fields) < 3 {
			continue
		}
		// Skip the header row: it is first and has no default marker, and its
		// last field is not a number.
		version, err := strconv.Atoi(fields[len(fields)-1])
		if err != nil {
			if i == 0 {
				continue // header
			}
			continue
		}
		state := fields[len(fields)-2]
		name := strings.Join(fields[:len(fields)-2], " ")
		if name == "" {
			continue
		}
		distros = append(distros, Distro{
			Name: name, State: state, Version: version, Default: isDefault,
		})
	}
	return distros
}

// Exec runs a command inside a distro and returns its combined output.
//
// --exec bypasses the shell, so nothing re-interprets arguments — paths with
// spaces and shell metacharacters pass through as given.
func (l *Local) Exec(ctx context.Context, distro, user string, args ...string) (string, error) {
	if distro == "" {
		return "", fmt.Errorf("wsl: Exec needs a distro name")
	}
	if len(args) == 0 {
		return "", fmt.Errorf("wsl: Exec needs a command")
	}
	return l.run(ctx, append(l.execArgs(distro, user), args...)...)
}

func (l *Local) execArgs(distro, user string) []string {
	a := []string{"-d", distro}
	if user != "" {
		a = append(a, "-u", user)
	}
	return append(a, "--exec")
}

// Start launches a command inside a distro without waiting for it, for
// long-lived processes such as dockerd.
func (l *Local) Start(ctx context.Context, distro, user string, args ...string) (func(), error) {
	if distro == "" {
		return nil, fmt.Errorf("wsl: Start needs a distro name")
	}
	if len(args) == 0 {
		return nil, fmt.Errorf("wsl: Start needs a command")
	}
	full := append(l.execArgs(distro, user), args...)
	return l.runner().Start(ctx, l.exe(), full...)
}

// execRunner runs real processes.
type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func (execRunner) Start(ctx context.Context, name string, args ...string) (func(), error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	stopped := make(chan struct{})
	go func() {
		cmd.Wait() // reap, so a restarted engine leaves no zombie behind
		close(stopped)
	}()
	return func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		<-stopped
	}, nil
}
