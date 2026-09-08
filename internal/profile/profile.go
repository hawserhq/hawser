// Package profile stores named bundles of engine settings (#73): the same
// laptop wants different config on the corporate VPN than at home — proxy, CA
// trust, registry mirrors, DNS — and every other tool makes you hand-toggle
// each one. A profile is a named set of the settings Hawser already exposes
// (idle-timeout, engine daemon.json keys, lifecycle hooks), saved as a small
// YAML file and applied in one switch.
//
// A profile reuses the hawser.yaml shape (hawserfile.File) so `profile show`
// and `hawser config export` speak the same language; only the settings fields
// are used, never the install-time ones (distro, data-dir, engine version).
package profile

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/zcsizmadia/hawser/internal/hawserfile"
)

// Manager stores profiles under the state directory.
type Manager struct {
	StateDir string
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidName reports whether a profile name is a safe file basename: no path
// separators, no leading dot, so it cannot escape the profiles directory.
func ValidName(name string) error {
	if !nameRe.MatchString(name) || name == "." || name == ".." {
		return fmt.Errorf("invalid profile name %q (use letters, digits, . _ -)", name)
	}
	return nil
}

func (m *Manager) dir() string { return filepath.Join(m.StateDir, "profiles") }

func (m *Manager) path(name string) string {
	return filepath.Join(m.dir(), name+".yaml")
}

func (m *Manager) activePath() string {
	return filepath.Join(m.StateDir, "active-profile")
}

// List returns the saved profile names, sorted.
func (m *Manager) List() ([]string, error) {
	entries, err := os.ReadDir(m.dir())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if n := strings.TrimSuffix(e.Name(), ".yaml"); n != e.Name() {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names, nil
}

// Exists reports whether a profile is saved.
func (m *Manager) Exists(name string) bool {
	_, err := os.Stat(m.path(name))
	return err == nil
}

// Save writes a profile, keeping only the settings fields — a profile never
// carries install-time identity (distro, data dir, engine version) or autostart,
// which are machine-scoped, not network-scoped.
func (m *Manager) Save(name string, f hawserfile.File) error {
	if err := ValidName(name); err != nil {
		return err
	}
	settings := hawserfile.File{
		IdleTimeout: f.IdleTimeout,
		Engine:      f.Engine,
		Hooks:       f.Hooks,
	}
	b, err := settings.Marshal()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(m.dir(), 0o755); err != nil {
		return fmt.Errorf("creating profiles dir: %w", err)
	}
	tmp := m.path(name) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.path(name))
}

// Load reads and validates a saved profile.
func (m *Manager) Load(name string) (hawserfile.File, error) {
	if err := ValidName(name); err != nil {
		return hawserfile.File{}, err
	}
	if !m.Exists(name) {
		return hawserfile.File{}, fmt.Errorf("no such profile %q", name)
	}
	return hawserfile.Load(m.path(name))
}

// Delete removes a saved profile. Deleting the active profile clears the active
// marker, so `status` does not name a profile that is gone.
func (m *Manager) Delete(name string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	if !m.Exists(name) {
		return fmt.Errorf("no such profile %q", name)
	}
	if err := os.Remove(m.path(name)); err != nil {
		return err
	}
	if m.Active() == name {
		return m.clearActive()
	}
	return nil
}

// Active returns the active profile name, or "" if none is set.
func (m *Manager) Active() string {
	b, err := os.ReadFile(m.activePath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// SetActive records which profile was last switched to, atomically.
func (m *Manager) SetActive(name string) error {
	if err := os.MkdirAll(m.StateDir, 0o755); err != nil {
		return err
	}
	tmp := m.activePath() + ".tmp"
	if err := os.WriteFile(tmp, []byte(name+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.activePath())
}

func (m *Manager) clearActive() error {
	err := os.Remove(m.activePath())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
