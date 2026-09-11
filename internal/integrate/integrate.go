// Package integrate shares the Skrog engine into the user's own WSL distros
// (#42): Desktop parity for people whose workflow lives inside a distro.
//
// The transport is the /mnt/wsl bind mount the provisioner publishes
// (provision.SharedSocketPath); this package only wires the target distro's
// environment to it, via a profile.d script. Consent is the command itself —
// nothing here runs unless the user asked for that specific distro — and
// every integration is recorded so `skrog uninstall` can reverse it.
package integrate

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/wslkit/skrog/internal/wsl"
)

// ProfilePath is the script written inside a target distro. profile.d keeps
// it out of the user's own dotfiles and makes removal a single rm.
const ProfilePath = "/etc/profile.d/skrog.sh"

// Manager wires and unwires distros.
type Manager struct {
	// WSL runs commands in distros. Required.
	WSL wsl.WSL
	// StateDir is where integrations are recorded. Required.
	StateDir string
	// Logger defaults to slog.Default().
	Logger *slog.Logger
}

func (m *Manager) log() *slog.Logger {
	if m.Logger != nil {
		return m.Logger
	}
	return slog.Default()
}

func (m *Manager) recordPath() string {
	return filepath.Join(m.StateDir, "integrations.json")
}

// Integrate wires DOCKER_HOST inside target to the engine's shared socket.
func (m *Manager) Integrate(ctx context.Context, target, engineDistro, socketPath string) error {
	if target == engineDistro {
		return fmt.Errorf("%q is the engine distro itself; it talks to its own socket already", target)
	}
	if err := m.distroExists(ctx, target); err != nil {
		return err
	}

	// The script announces its origin and its undo, because the person who
	// finds it in six months may not be the person who ran the command.
	content := fmt.Sprintf(
		"# Written by `skrog wsl-integrate %s`; remove with `skrog wsl-integrate --remove %s`.\n"+
			"# Points docker at the Skrog engine shared from the %s distro.\n"+
			`export DOCKER_HOST="unix://%s"`+"\n",
		target, target, engineDistro, socketPath)

	// Content travels as a positional parameter — never spliced into the
	// script — so nothing in it can become shell.
	if _, err := m.WSL.Exec(ctx, target, "root",
		"sh", "-c", `printf '%s' "$1" > `+ProfilePath, "sh", content); err != nil {
		return fmt.Errorf("writing %s in %s: %w", ProfilePath, target, err)
	}

	if err := m.record(target, true); err != nil {
		return err
	}
	m.log().Info("distro integrated", "distro", target, "socket", socketPath)
	return nil
}

// Remove unwires a distro. A distro that is already clean — or no longer
// exists — counts as success: the user asked for a state, not an action.
func (m *Manager) Remove(ctx context.Context, target string) error {
	if err := m.distroExists(ctx, target); err == nil {
		if _, err := m.WSL.Exec(ctx, target, "root", "rm", "-f", ProfilePath); err != nil {
			return fmt.Errorf("removing %s from %s: %w", ProfilePath, target, err)
		}
	} else {
		m.log().Info("distro no longer exists; unrecording only", "distro", target)
	}
	if err := m.record(target, false); err != nil {
		return err
	}
	m.log().Info("distro integration removed", "distro", target)
	return nil
}

// List returns the recorded integrations, sorted.
func (m *Manager) List() ([]string, error) {
	set, err := m.load()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	sort.Strings(out)
	return out, nil
}

// RemoveAll unwires every recorded distro, best-effort, for uninstall.
func (m *Manager) RemoveAll(ctx context.Context) {
	distros, err := m.List()
	if err != nil {
		m.log().Warn("could not read integrations; profile scripts may remain", "error", err)
		return
	}
	for _, d := range distros {
		if err := m.Remove(ctx, d); err != nil {
			m.log().Warn("could not unwire distro", "distro", d, "error", err)
		}
	}
}

func (m *Manager) distroExists(ctx context.Context, target string) error {
	distros, err := m.WSL.List(ctx)
	if err != nil {
		return fmt.Errorf("listing distros: %w", err)
	}
	names := make([]string, 0, len(distros))
	for _, d := range distros {
		if d.Name == target {
			return nil
		}
		names = append(names, d.Name)
	}
	return fmt.Errorf("no WSL distro named %q (have: %s)", target, strings.Join(names, ", "))
}

func (m *Manager) load() (map[string]bool, error) {
	set := map[string]bool{}
	b, err := os.ReadFile(m.recordPath())
	if os.IsNotExist(err) {
		return set, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading integrations: %w", err)
	}
	var list []string
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", m.recordPath(), err)
	}
	for _, d := range list {
		set[d] = true
	}
	return set, nil
}

func (m *Manager) record(target string, present bool) error {
	set, err := m.load()
	if err != nil {
		return err
	}
	if present {
		set[target] = true
	} else {
		delete(set, target)
	}
	list := make([]string, 0, len(set))
	for d := range set {
		list = append(list, d)
	}
	sort.Strings(list)

	if err := os.MkdirAll(m.StateDir, 0o755); err != nil {
		return fmt.Errorf("creating state dir: %w", err)
	}
	b, _ := json.MarshalIndent(list, "", "  ")
	tmp := m.recordPath() + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("recording integration: %w", err)
	}
	if err := os.Rename(tmp, m.recordPath()); err != nil {
		return fmt.Errorf("committing integration record: %w", err)
	}
	return nil
}
