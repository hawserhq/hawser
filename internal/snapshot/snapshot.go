// Package snapshot saves and restores the whole engine state — every image,
// container and volume — as named, checksummed archives (#122). Docker Desktop
// has no engine-state snapshotting; here you can capture a working dev
// environment, trash it during a risky test, and restore it in one command.
//
// A snapshot is a `wsl --export` tarball of the engine distro plus a small JSON
// sidecar (created time, engine version, SHA-256, size). Restore verifies the
// checksum before it replaces anything, so a corrupt or tampered archive is
// refused rather than imported.
package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/zcsizmadia/hawser/internal/wsl"
)

// Meta is a snapshot's sidecar record.
type Meta struct {
	Name          string    `json:"name"`
	Created       time.Time `json:"created"`
	EngineVersion string    `json:"engineVersion,omitempty"`
	Distro        string    `json:"distro"`
	SHA256        string    `json:"sha256"`
	SizeBytes     int64     `json:"sizeBytes"`
}

// Manager stores snapshots under the state directory. Engine stop/start
// orchestration is the caller's job (it owns the supervisor); this package does
// the export, import, checksum and metadata only.
type Manager struct {
	WSL      wsl.WSL
	StateDir string
	Distro   string
	// DataDir is where a restored distro's VHDX is imported.
	DataDir string
	Logger  *slog.Logger
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidName reports whether a snapshot name is a safe file basename.
func ValidName(name string) error {
	if !nameRe.MatchString(name) || name == "." || name == ".." {
		return fmt.Errorf("invalid snapshot name %q (use letters, digits, . _ -)", name)
	}
	return nil
}

func (m *Manager) log() *slog.Logger {
	if m.Logger != nil {
		return m.Logger
	}
	return slog.Default()
}

func (m *Manager) dir() string             { return filepath.Join(m.StateDir, "snapshots") }
func (m *Manager) tar(name string) string  { return filepath.Join(m.dir(), name+".tar") }
func (m *Manager) meta(name string) string { return filepath.Join(m.dir(), name+".json") }

// Exists reports whether a snapshot is saved.
func (m *Manager) Exists(name string) bool {
	_, err := os.Stat(m.meta(name))
	return err == nil
}

// Save exports the engine distro to a named snapshot and records its checksum.
// The caller is responsible for the engine's lifecycle; `wsl --export`
// terminates the distro to get a consistent image, and the supervisor restarts
// it afterward.
func (m *Manager) Save(ctx context.Context, name, engineVersion string) (Meta, error) {
	if err := ValidName(name); err != nil {
		return Meta{}, err
	}
	if err := os.MkdirAll(m.dir(), 0o755); err != nil {
		return Meta{}, fmt.Errorf("creating snapshots dir: %w", err)
	}

	tmp := m.tar(name) + ".tmp"
	os.Remove(tmp)
	m.log().Info("exporting engine snapshot", "name", name, "distro", m.Distro)
	if err := m.WSL.Export(ctx, m.Distro, tmp); err != nil {
		os.Remove(tmp)
		return Meta{}, fmt.Errorf("exporting %s: %w", m.Distro, err)
	}

	sum, size, err := fileSHA256(tmp)
	if err != nil {
		os.Remove(tmp)
		return Meta{}, err
	}
	if err := os.Rename(tmp, m.tar(name)); err != nil {
		os.Remove(tmp)
		return Meta{}, err
	}

	meta := Meta{
		Name:          name,
		Created:       time.Now().UTC(),
		EngineVersion: engineVersion,
		Distro:        m.Distro,
		SHA256:        sum,
		SizeBytes:     size,
	}
	if err := m.writeMeta(meta); err != nil {
		return Meta{}, err
	}
	return meta, nil
}

// Get returns a snapshot's metadata.
func (m *Manager) Get(name string) (Meta, error) {
	b, err := os.ReadFile(m.meta(name))
	if err != nil {
		if os.IsNotExist(err) {
			return Meta{}, fmt.Errorf("no such snapshot %q", name)
		}
		return Meta{}, err
	}
	var meta Meta
	if err := json.Unmarshal(b, &meta); err != nil {
		return Meta{}, fmt.Errorf("reading snapshot %q: %w", name, err)
	}
	return meta, nil
}

// List returns saved snapshots, newest first.
func (m *Manager) List() ([]Meta, error) {
	entries, err := os.ReadDir(m.dir())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Meta
	for _, e := range entries {
		name := e.Name()
		if filepath.Ext(name) != ".json" {
			continue
		}
		if meta, err := m.Get(name[:len(name)-len(".json")]); err == nil {
			out = append(out, meta)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, nil
}

// Restore replaces the engine distro with a snapshot, after verifying the
// archive matches its recorded checksum. Destructive: the current engine state
// is discarded. The caller must have stopped the engine and paused the
// supervisor first, and restarts it afterward.
func (m *Manager) Restore(ctx context.Context, name string) error {
	meta, err := m.Get(name)
	if err != nil {
		return err
	}
	// Verify before touching anything: a corrupt archive must not replace a good
	// engine with nothing.
	sum, _, err := fileSHA256(m.tar(name))
	if err != nil {
		return fmt.Errorf("reading snapshot archive: %w", err)
	}
	if sum != meta.SHA256 {
		return fmt.Errorf("snapshot %q is corrupt: archive checksum does not match its record; refusing to restore", name)
	}

	if m.DataDir == "" {
		return fmt.Errorf("restore needs a data dir for the imported distro")
	}
	// Terminate first (ignore if already stopped), then replace.
	_ = m.WSL.Terminate(ctx, m.Distro)
	m.log().Info("replacing engine distro from snapshot", "name", name, "distro", m.Distro)
	if err := m.WSL.Unregister(ctx, m.Distro); err != nil {
		return fmt.Errorf("unregistering the current engine: %w", err)
	}
	if err := os.MkdirAll(m.DataDir, 0o755); err != nil {
		return fmt.Errorf("creating data dir: %w", err)
	}
	if err := m.WSL.Import(ctx, m.Distro, m.DataDir, m.tar(name)); err != nil {
		return fmt.Errorf("importing the snapshot: %w", err)
	}
	return nil
}

// Delete removes a saved snapshot.
func (m *Manager) Delete(name string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	if !m.Exists(name) {
		return fmt.Errorf("no such snapshot %q", name)
	}
	os.Remove(m.tar(name))
	return os.Remove(m.meta(name))
}

func (m *Manager) writeMeta(meta Meta) error {
	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.meta(meta.Name) + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.meta(meta.Name))
}

func fileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
