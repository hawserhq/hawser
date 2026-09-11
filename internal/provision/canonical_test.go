package provision

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanonicalRootfsURL(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		changed bool
	}{
		{
			// The real shape found on a pre-transfer install.
			name:    "a former home is rewritten, tag and filename untouched",
			in:      "https://github.com/zcsizmadia/hawser/releases/download/rootfs-v29.8.0-1/hawser-rootfs-29.8.0-1.tar.gz",
			want:    "https://github.com/hawserhq/hawser/releases/download/rootfs-v29.8.0-1/hawser-rootfs-29.8.0-1.tar.gz",
			changed: true,
		},
		{
			name:    "already current",
			in:      "https://github.com/hawserhq/hawser/releases/download/rootfs-v29.8.0-1/x.tar.gz",
			want:    "https://github.com/hawserhq/hawser/releases/download/rootfs-v29.8.0-1/x.tar.gz",
			changed: false,
		},
		{
			name:    "host case does not matter",
			in:      "https://GitHub.com/ZCsizmadia/hawser/releases/download/t/x.tar.gz",
			want:    "https://github.com/hawserhq/hawser/releases/download/t/x.tar.gz",
			changed: true,
		},
		{
			// Air-gapped installs point at a local file, and rewriting those
			// would break exactly the installs that chose them deliberately.
			name:    "a file URL is left alone",
			in:      "file:///C:/bundles/hawser-rootfs.tar.gz",
			want:    "file:///C:/bundles/hawser-rootfs.tar.gz",
			changed: false,
		},
		{
			name:    "an internal mirror is left alone",
			in:      "https://artifactory.corp.example/hawser/hawser-rootfs-29.8.0-1.tar.gz",
			want:    "https://artifactory.corp.example/hawser/hawser-rootfs-29.8.0-1.tar.gz",
			changed: false,
		},
		{
			// A different project that merely shares the old owner must not be
			// rewritten into ours.
			name:    "another repo under the same old owner is left alone",
			in:      "https://github.com/zcsizmadia/wsldisk/releases/download/v1/x.tar.gz",
			want:    "https://github.com/zcsizmadia/wsldisk/releases/download/v1/x.tar.gz",
			changed: false,
		},
		{
			name:    "empty",
			in:      "",
			want:    "",
			changed: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, changed := CanonicalRootfsURL(c.in)
			if got != c.want {
				t.Errorf("url = %q, want %q", got, c.want)
			}
			if changed != c.changed {
				t.Errorf("changed = %v, want %v", changed, c.changed)
			}
		})
	}
}

func TestReadManifestCanonicalizesAStaleURL(t *testing.T) {
	// The consequence that makes this worth fixing: `hawser engine rollback`
	// re-fetches from the URL recorded here, so a stale one is not just a
	// cosmetic record — it is where a real download would come from.
	dir := t.TempDir()
	raw := `{
	  "distro": "hawser-engine",
	  "rootfsUrl": "https://github.com/zcsizmadia/hawser/releases/download/rootfs-v29.8.0-1/hawser-rootfs-29.8.0-1.tar.gz",
	  "rootfsSha256": "76101881a8b56ce7fc25b947f2b31dca0aeda6ff3e76d8d7a5fbe3a3be782635",
	  "engineVersion": "29.8.0"
	}`
	writeManifestFile(t, dir, raw)

	p := &Provisioner{}
	m, err := p.ReadManifest(Options{StateDir: dir})
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if strings.Contains(m.RootfsURL, "zcsizmadia") {
		t.Errorf("RootfsURL = %q; a former home must not survive a read", m.RootfsURL)
	}
	if !strings.HasPrefix(m.RootfsURL, "https://github.com/hawserhq/hawser/") {
		t.Errorf("RootfsURL = %q, want it under the current home", m.RootfsURL)
	}
	// The release's own tag and filename are not ours to change: the checksum
	// recorded beside them has to stay meaningful.
	if !strings.HasSuffix(m.RootfsURL, "/rootfs-v29.8.0-1/hawser-rootfs-29.8.0-1.tar.gz") {
		t.Errorf("RootfsURL = %q; the tag and filename should be untouched", m.RootfsURL)
	}
	if m.RootfsSHA256 == "" || m.EngineVersion != "29.8.0" {
		t.Errorf("the rest of the manifest was disturbed: %+v", m)
	}
}

func TestReadManifestLeavesACustomURLAlone(t *testing.T) {
	dir := t.TempDir()
	const mirror = "https://artifactory.corp.example/hawser/rootfs.tar.gz"
	writeManifestFile(t, dir, `{"distro":"d","rootfsUrl":"`+mirror+`","engineVersion":"29.8.0"}`)

	p := &Provisioner{}
	m, err := p.ReadManifest(Options{StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if m.RootfsURL != mirror {
		t.Errorf("RootfsURL = %q, want the mirror untouched", m.RootfsURL)
	}
}

// writeManifestFile puts a manifest where ReadManifest looks for it, going
// through the same path helper so the test cannot drift from the real layout.
func writeManifestFile(t *testing.T, stateDir, body string) {
	t.Helper()
	// Validate the fixture is real JSON, so a typo fails as a bad test rather
	// than as a confusing parse error from the code under test.
	var probe map[string]any
	if err := json.Unmarshal([]byte(body), &probe); err != nil {
		t.Fatalf("test fixture is not valid JSON: %v", err)
	}
	p := &Provisioner{}
	path := p.manifestPath(Options{StateDir: stateDir})
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
