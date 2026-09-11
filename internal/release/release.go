// Package release exposes the locked version manifest compiled into the binary.
//
// PLAN §04: nothing is fetched as "latest at install time". The manifest ships
// inside the executable, so a given Skrog build installs exactly the
// components it was tested with, and `skrog install` on a CI runner in six
// months produces the same engine it produces today. That determinism is a
// direct anti-feature of Docker Desktop's auto-updating, and the reason the
// manifest is embedded rather than downloaded.
package release

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

//go:embed manifest.json
var manifestJSON []byte

// Manifest is the set of engines a build can install.
type Manifest struct {
	SchemaVersion int      `json:"schemaVersion"`
	Engines       []Engine `json:"engines"`
}

// Engine is one installable engine version and the rootfs that carries it.
type Engine struct {
	Version    string            `json:"version"`
	Default    bool              `json:"default"`
	Rootfs     Rootfs            `json:"rootfs"`
	Components map[string]string `json:"components"`
}

// Rootfs locates a rootfs tarball and the digest it must match.
type Rootfs struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

// Published reports whether this entry can actually be installed. An entry
// with no checksum is a placeholder: the rootfs release has not been cut yet,
// and installing it unverified is not an option (the rootfs becomes root inside
// the engine VM).
func (e Engine) Published() bool {
	return e.Rootfs.URL != "" && e.Rootfs.SHA256 != ""
}

// ErrNotPublished reports an engine entry without a checksum.
type ErrNotPublished struct{ Version string }

func (e *ErrNotPublished) Error() string {
	return fmt.Sprintf("engine %s has no published rootfs checksum in this build's manifest.\n"+
		"This happens in a development build before the rootfs release is cut. Either:\n"+
		"  - install a release build of skrog, or\n"+
		"  - build the rootfs yourself (guest/rootfs/build.sh) and pass\n"+
		"    --rootfs-url file:///... together with --rootfs-sha256 <digest>",
		e.Version)
}

// Load parses the embedded manifest.
func Load() (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(manifestJSON, &m); err != nil {
		return nil, fmt.Errorf("parsing embedded release manifest: %w", err)
	}
	if m.SchemaVersion != 1 {
		// A future binary reading an older embedded file cannot happen, but a
		// hand-edited manifest can, and silently misreading it would be worse.
		return nil, fmt.Errorf("unsupported manifest schemaVersion %d", m.SchemaVersion)
	}
	if len(m.Engines) == 0 {
		return nil, fmt.Errorf("release manifest lists no engines")
	}
	return &m, nil
}

// Engine returns the entry for a version, or the default when version is empty.
//
// Selection is exact: `--engine-version 29.7` does not resolve to 29.7.2,
// because a pin that quietly matches something else is not a pin.
func (m *Manifest) Engine(version string) (*Engine, error) {
	if version == "" {
		for i := range m.Engines {
			if m.Engines[i].Default {
				return &m.Engines[i], nil
			}
		}
		return nil, fmt.Errorf("release manifest marks no default engine (available: %s)",
			strings.Join(m.Versions(), ", "))
	}
	for i := range m.Engines {
		if m.Engines[i].Version == version {
			return &m.Engines[i], nil
		}
	}
	return nil, fmt.Errorf("engine version %q is not in this build's manifest (available: %s)",
		version, strings.Join(m.Versions(), ", "))
}

// Versions lists every engine version the build can install, sorted.
func (m *Manifest) Versions() []string {
	out := make([]string, 0, len(m.Engines))
	for _, e := range m.Engines {
		out = append(out, e.Version)
	}
	sort.Strings(out)
	return out
}
