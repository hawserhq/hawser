package remote

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hawserhq/hawser/internal/remotecert"
)

// mintCerts writes a CA and a client cert/key into dir under the given
// filenames, using the same generator `hawser serve cert` uses.
func mintCerts(t *testing.T, dir, certName, keyName string) {
	t.Helper()
	ca, err := remotecert.GenerateCA()
	if err != nil {
		t.Fatal(err)
	}
	cl, err := remotecert.GenerateClient(ca, "test-client")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		CAFile:   ca.CertPEM,
		certName: cl.CertPEM,
		keyName:  cl.KeyPEM,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAddAcceptsServeCertLayout(t *testing.T) {
	// `hawser serve cert` writes client.pem / client-key.pem; Add normalizes to
	// docker's cert.pem / key.pem.
	certs := filepath.Join(t.TempDir(), "certs")
	mintCerts(t, certs, "client.pem", "client-key.pem")
	m := &Manager{StateDir: t.TempDir()}

	info, err := m.Add("desk", "tcp://desktop:2376", certs)
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "desk" || info.Host != "tcp://desktop:2376" {
		t.Errorf("info = %+v", info)
	}
	for _, f := range []string{CAFile, CertFile, KeyFile} {
		if _, err := os.Stat(filepath.Join(m.Dir("desk"), f)); err != nil {
			t.Errorf("%s not staged: %v", f, err)
		}
	}
	// The client cert is a 2-year leaf; expiry must be read from it, not guessed.
	if until := time.Until(info.CertNotAfter); until < 365*24*time.Hour || until > 3*365*24*time.Hour {
		t.Errorf("CertNotAfter %v is not ~2 years out", info.CertNotAfter)
	}

	got, err := m.Get("desk")
	if err != nil || got.Host != info.Host {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if !m.Exists("desk") {
		t.Error("Exists should be true after Add")
	}
	ca, cert, key := m.CertPaths("desk")
	if filepath.Base(ca) != CAFile || filepath.Base(cert) != CertFile || filepath.Base(key) != KeyFile {
		t.Errorf("CertPaths = %s %s %s", ca, cert, key)
	}
}

func TestAddAcceptsDockerLayout(t *testing.T) {
	certs := filepath.Join(t.TempDir(), "certs")
	mintCerts(t, certs, CertFile, KeyFile)
	m := &Manager{StateDir: t.TempDir()}
	if _, err := m.Add("ci", "tcp://10.0.0.5:2376", certs); err != nil {
		t.Fatal(err)
	}
}

func TestAddRejectsBadInput(t *testing.T) {
	certs := filepath.Join(t.TempDir(), "certs")
	mintCerts(t, certs, CertFile, KeyFile)
	m := &Manager{StateDir: t.TempDir()}

	cases := []struct{ name, host, dir string }{
		{"local", "tcp://h:2376", certs},     // reserved
		{"bad name!", "tcp://h:2376", certs}, // charset
		{"ok", "http://h:2376", certs},       // scheme
		{"ok", "tcp://h", certs},             // no port
		{"ok", "tcp://h:2376", t.TempDir()},  // no certs
	}
	for _, tc := range cases {
		if _, err := m.Add(tc.name, tc.host, tc.dir); err == nil {
			t.Errorf("Add(%q, %q, %q) should fail", tc.name, tc.host, tc.dir)
		}
	}
	if list, _ := m.List(); len(list) != 0 {
		t.Errorf("failed adds must leave nothing behind, got %+v", list)
	}
}

func TestAddRejectsNonCertificateFiles(t *testing.T) {
	certs := t.TempDir()
	for _, f := range []string{CAFile, CertFile, KeyFile} {
		os.WriteFile(filepath.Join(certs, f), []byte("not pem"), 0o600)
	}
	m := &Manager{StateDir: t.TempDir()}
	if _, err := m.Add("x", "tcp://h:2376", certs); err == nil {
		t.Fatal("garbage files must be refused at add time, not on first connect")
	}
}

func TestListRemoveAndEmpty(t *testing.T) {
	m := &Manager{StateDir: t.TempDir()}
	list, err := m.List()
	if err != nil || list == nil || len(list) != 0 {
		t.Fatalf("empty List = %v, %v; want [] and nil", list, err)
	}

	certs := filepath.Join(t.TempDir(), "certs")
	mintCerts(t, certs, CertFile, KeyFile)
	for _, n := range []string{"zeta", "alpha"} {
		if _, err := m.Add(n, "tcp://h:2376", certs); err != nil {
			t.Fatal(err)
		}
	}
	list, _ = m.List()
	if len(list) != 2 || list[0].Name != "alpha" || list[1].Name != "zeta" {
		t.Errorf("List should be sorted by name: %+v", list)
	}

	if err := m.Remove("alpha"); err != nil {
		t.Fatal(err)
	}
	if m.Exists("alpha") {
		t.Error("alpha still exists after Remove")
	}
	if err := m.Remove("alpha"); err == nil {
		t.Error("removing an unknown remote must be an error")
	}
}

func TestContextName(t *testing.T) {
	if ContextName("desk") != "hawser-desk" {
		t.Errorf("ContextName = %q", ContextName("desk"))
	}
}
