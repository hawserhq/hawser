package dockercli

import "testing"

// TestEmbeddedManifestLoads is the guard that the shipped manifest.json parses
// and is internally sound: real pins, known roles, and — for anything published
// — a checksum. A broken manifest would fail every install, so it fails the
// build here instead.
func TestEmbeddedManifestLoads(t *testing.T) {
	m, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if m.SchemaVersion != 1 {
		t.Fatalf("schemaVersion = %d", m.SchemaVersion)
	}
	seenRoles := map[Role]bool{}
	for _, c := range m.Components {
		if c.Name == "" || c.Version == "" || c.Target == "" {
			t.Errorf("component missing name/version/target: %+v", c)
		}
		switch c.Role {
		case RoleCLI, RolePlugin, RoleHelper:
			seenRoles[c.Role] = true
		default:
			t.Errorf("%s has unknown role %q", c.Name, c.Role)
		}
		for arch, a := range c.Arch {
			// A published asset must carry both URL and checksum; a placeholder
			// (no url) must carry no checksum, and vice versa.
			if (a.URL == "") != (a.SHA256 == "") {
				t.Errorf("%s/%s: URL and sha256 must be set together (url=%q sha=%q)",
					c.Name, arch, a.URL, a.SHA256)
			}
		}
	}
	// The bundle is meaningless without the CLI itself and at least one plugin.
	if !seenRoles[RoleCLI] || !seenRoles[RolePlugin] || !seenRoles[RoleHelper] {
		t.Errorf("manifest should carry a cli, a plugin, and a helper; saw %v", seenRoles)
	}
}

func TestPublished(t *testing.T) {
	c := Component{Arch: map[string]Asset{
		"amd64": {URL: "https://x/y", SHA256: "abc"},
		"arm64": {URL: "", SHA256: ""},
	}}
	if !c.Published("amd64") {
		t.Error("amd64 with url+sha should be published")
	}
	if c.Published("arm64") {
		t.Error("arm64 placeholder must not be published")
	}
	if c.Published("riscv64") {
		t.Error("an arch with no entry must not be published")
	}
}

func TestForArch(t *testing.T) {
	m := &Manifest{Components: []Component{
		{Name: "docker", Arch: map[string]Asset{"amd64": {URL: "u", SHA256: "s"}}},
		{Name: "compose", Arch: map[string]Asset{
			"amd64": {URL: "u", SHA256: "s"},
			"arm64": {URL: "u", SHA256: "s"},
		}},
	}}
	avail, unavail := m.ForArch("arm64")
	if len(avail) != 1 || avail[0].Name != "compose" {
		t.Errorf("arm64 available = %+v, want just compose", avail)
	}
	if len(unavail) != 1 || unavail[0] != "docker" {
		t.Errorf("arm64 unavailable = %v, want [docker]", unavail)
	}
}

func TestComponentLookup(t *testing.T) {
	m, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Component("docker"); err != nil {
		t.Errorf("docker should be present: %v", err)
	}
	if _, err := m.Component("nope"); err == nil {
		t.Error("unknown component should error")
	}
}
