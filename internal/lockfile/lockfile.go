// Package lockfile is the reproducible-engine format (#74): skrog.lock pins the
// exact engine — version, rootfs URL and SHA-256, and component versions — so a
// team checks one file into a repo and every developer and CI runner installs
// the same verified engine. It is the embedded release manifest, externalized.
package lockfile

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/wslkit/skrog/internal/release"
)

// SchemaVersion is bumped only on an incompatible change to the file shape.
const SchemaVersion = 1

// Lock is the parsed skrog.lock. The rootfs URL + SHA-256 are the guarantee:
// `install --locked` fetches exactly that URL and refuses on any checksum
// mismatch. Components are informational (they live inside that rootfs).
type Lock struct {
	SchemaVersion int               `json:"schemaVersion"`
	EngineVersion string            `json:"engineVersion"`
	Rootfs        Rootfs            `json:"rootfs"`
	Components    map[string]string `json:"components,omitempty"`
}

// Rootfs pins the artifact to fetch and verify.
type Rootfs struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

var sha256Re = regexp.MustCompile(`^[0-9a-f]{64}$`)

// FromEngine builds a Lock from a resolved release engine.
func FromEngine(e *release.Engine) Lock {
	l := Lock{
		SchemaVersion: SchemaVersion,
		EngineVersion: e.Version,
		Rootfs:        Rootfs{URL: e.Rootfs.URL, SHA256: e.Rootfs.SHA256},
	}
	if len(e.Components) > 0 {
		l.Components = make(map[string]string, len(e.Components))
		for k, v := range e.Components {
			l.Components[k] = v
		}
	}
	return l
}

// Load reads and validates a skrog.lock from disk.
func Load(path string) (Lock, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Lock{}, fmt.Errorf("reading %s: %w", path, err)
	}
	return Parse(b)
}

// Parse decodes and validates a skrog.lock. Unknown fields are rejected so a
// lock written by a newer Skrog is not silently misread by an older one.
func Parse(b []byte) (Lock, error) {
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	var l Lock
	if err := dec.Decode(&l); err != nil {
		return Lock{}, fmt.Errorf("parsing skrog.lock: %w", err)
	}
	if err := l.Validate(); err != nil {
		return Lock{}, err
	}
	return l, nil
}

// Validate checks the fields that make a lock reproducible: a version, a rootfs
// URL, and a well-formed SHA-256. An unverifiable lock is worse than none.
func (l Lock) Validate() error {
	if l.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported lock schemaVersion %d (this build expects %d)",
			l.SchemaVersion, SchemaVersion)
	}
	if strings.TrimSpace(l.EngineVersion) == "" {
		return fmt.Errorf("lock is missing engineVersion")
	}
	if strings.TrimSpace(l.Rootfs.URL) == "" {
		return fmt.Errorf("lock is missing rootfs.url")
	}
	if !sha256Re.MatchString(l.Rootfs.SHA256) {
		return fmt.Errorf("lock rootfs.sha256 %q is not a 64-hex SHA-256", l.Rootfs.SHA256)
	}
	return nil
}

// Marshal renders the lock as pretty JSON with a trailing newline, so a
// checked-in file diffs cleanly.
func (l Lock) Marshal() ([]byte, error) {
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
