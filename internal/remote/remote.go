// Package remote is the client side of `hawser serve` (#138). It registers a
// remote engine's address and mutual-TLS material under the state dir, and the
// CLI backs a docker context (hawser-<name>) with them — so `docker`, and
// anything that follows the docker context (VS Code Dev Containers, compose),
// can target a remote engine with one command instead of three environment
// variables. Certificates are copied, never printed.
//
// This package needs no docker binary: it validates and stages files. Creating
// the context is the caller's job (internal/dockerctx), which keeps this
// testable without docker installed.
package remote

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ContextPrefix is prepended to a remote's name to form its docker context, so
// remotes are recognizable (and never collide with the local "hawser" context).
const ContextPrefix = "hawser-"

// ContextName is the docker context backing remote name.
func ContextName(name string) string { return ContextPrefix + name }

// Docker's DOCKER_CERT_PATH layout, which is what a remote's directory holds
// regardless of how the source files were named.
const (
	CAFile   = "ca.pem"
	CertFile = "cert.pem"
	KeyFile  = "key.pem"
	metaFile = "remote.json"
)

// Info describes a registered remote. CertNotAfter is the client certificate's
// expiry, so doctor can warn before a remote silently stops working.
type Info struct {
	Name         string    `json:"name"`
	Host         string    `json:"host"`
	Added        time.Time `json:"added"`
	CertNotAfter time.Time `json:"certNotAfter"`
	Dir          string    `json:"dir"`
}

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidName bounds a remote name: it becomes a directory and a docker context
// name. "local" is reserved for `hawser remote use local`.
func ValidName(name string) error {
	if !nameRE.MatchString(name) || name == "local" {
		return fmt.Errorf("invalid remote name %q: letters, digits, . _ - (max 64), and not \"local\"", name)
	}
	return nil
}

// ValidHost accepts tcp://<host>:<port>, the form docker expects for a TLS
// endpoint. A missing port is the classic mistake, so it is refused explicitly.
func ValidHost(host string) error {
	u, err := url.Parse(host)
	if err != nil || u.Scheme != "tcp" || u.Hostname() == "" || u.Port() == "" {
		return fmt.Errorf("invalid host %q: want tcp://<host>:<port>, e.g. tcp://desktop:2376", host)
	}
	return nil
}

// Manager stores remotes under <StateDir>/remotes/<name>/.
type Manager struct {
	StateDir string
}

func (m *Manager) root() string { return filepath.Join(m.StateDir, "remotes") }

// Dir is where a remote's certificates and metadata live.
func (m *Manager) Dir(name string) string { return filepath.Join(m.root(), name) }

// CertPaths are the three files docker needs for a remote.
func (m *Manager) CertPaths(name string) (ca, cert, key string) {
	d := m.Dir(name)
	return filepath.Join(d, CAFile), filepath.Join(d, CertFile), filepath.Join(d, KeyFile)
}

// Add registers a remote: validates the name and host, locates the CA, client
// certificate and key in certsDir — docker's ca.pem/cert.pem/key.pem or
// `hawser serve cert`'s ca.pem/client.pem/client-key.pem — parses the
// certificates (so a wrong file fails here, not on first connect), and copies
// them under the state dir with the key at 0600. Re-adding a name overwrites it.
func (m *Manager) Add(name, host, certsDir string) (Info, error) {
	if err := ValidName(name); err != nil {
		return Info{}, err
	}
	if err := ValidHost(host); err != nil {
		return Info{}, err
	}
	ca, err := findFile(certsDir, CAFile)
	if err != nil {
		return Info{}, err
	}
	cert, err := findFile(certsDir, CertFile, "client.pem")
	if err != nil {
		return Info{}, err
	}
	key, err := findFile(certsDir, KeyFile, "client-key.pem")
	if err != nil {
		return Info{}, err
	}

	if _, err := parseCert(ca); err != nil {
		return Info{}, fmt.Errorf("%s: %w", ca, err)
	}
	leaf, err := parseCert(cert)
	if err != nil {
		return Info{}, fmt.Errorf("%s: %w", cert, err)
	}
	if err := checkPEMKey(key); err != nil {
		return Info{}, fmt.Errorf("%s: %w", key, err)
	}

	dir := m.Dir(name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Info{}, err
	}
	if err := copyFile(ca, filepath.Join(dir, CAFile), 0o644); err != nil {
		return Info{}, err
	}
	if err := copyFile(cert, filepath.Join(dir, CertFile), 0o644); err != nil {
		return Info{}, err
	}
	if err := copyFile(key, filepath.Join(dir, KeyFile), 0o600); err != nil {
		return Info{}, err
	}

	info := Info{
		Name:         name,
		Host:         host,
		Added:        time.Now().UTC().Truncate(time.Second),
		CertNotAfter: leaf.NotAfter,
		Dir:          dir,
	}
	b, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return Info{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, metaFile), append(b, '\n'), 0o644); err != nil {
		return Info{}, err
	}
	return info, nil
}

// Get returns a registered remote.
func (m *Manager) Get(name string) (Info, error) {
	b, err := os.ReadFile(filepath.Join(m.Dir(name), metaFile))
	if errors.Is(err, os.ErrNotExist) {
		return Info{}, fmt.Errorf("no such remote %q", name)
	}
	if err != nil {
		return Info{}, err
	}
	var info Info
	if err := json.Unmarshal(b, &info); err != nil {
		return Info{}, fmt.Errorf("reading remote %q: %w", name, err)
	}
	return info, nil
}

// Exists reports whether name is registered.
func (m *Manager) Exists(name string) bool {
	_, err := m.Get(name)
	return err == nil
}

// List returns every registered remote sorted by name. No remotes yet is an
// empty list, never nil.
func (m *Manager) List() ([]Info, error) {
	entries, err := os.ReadDir(m.root())
	if errors.Is(err, os.ErrNotExist) {
		return []Info{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Info{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if info, err := m.Get(e.Name()); err == nil {
			out = append(out, info)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Remove deletes a remote's material. An unknown name is an error so a typo
// does not silently succeed.
func (m *Manager) Remove(name string) error {
	if !m.Exists(name) {
		return fmt.Errorf("no such remote %q", name)
	}
	return os.RemoveAll(m.Dir(name))
}

func findFile(dir string, candidates ...string) (string, error) {
	for _, c := range candidates {
		p := filepath.Join(dir, c)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("no %s in %s", strings.Join(candidates, " or "), dir)
}

func parseCert(path string) (*x509.Certificate, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("not a PEM certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}

// checkPEMKey verifies the file is a PEM private key (EC, RSA or PKCS#8)
// without loading it — the key is docker's to use, not ours.
func checkPEMKey(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	block, _ := pem.Decode(b)
	if block == nil || !strings.HasSuffix(block.Type, "PRIVATE KEY") {
		return errors.New("not a PEM private key")
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, mode)
}
