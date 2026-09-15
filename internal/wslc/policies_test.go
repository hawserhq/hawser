package wslc

import "testing"

// The allowlist semantics are copied from wsl::windows::policies::IsRegistryAllowed
// and have to stay copied: an administrator's deployed policy must mean the same
// thing through Skrog's pipe as it does through the wslc CLI. Anything stricter
// breaks a working deployment; anything looser is a bypass.
func TestRegistryAllowedMatchesWSLSemantics(t *testing.T) {
	cases := []struct {
		name      string
		allowlist []string
		server    string
		want      bool
	}{
		// "The policy only restricts traffic when the sub-key exists and
		// contains at least one entry."
		{"no policy allows anything", nil, "docker.io", true},
		{"empty policy allows anything", []string{}, "evil.example.com", true},

		{"exact match", []string{"contoso.azurecr.io"}, "contoso.azurecr.io", true},
		{"case-insensitive", []string{"Contoso.AzureCR.io"}, "contoso.azurecr.io", true},
		{"no match is denied", []string{"contoso.azurecr.io"}, "docker.io", false},
		{"one of several", []string{"a.example", "b.example"}, "b.example", true},

		// "if (server.empty()) return true" — WSL does not guess at an
		// unattributable server, and neither should Skrog.
		{"empty server is allowed", []string{"contoso.azurecr.io"}, "", true},

		// Matching is on the server, in full: a suffix or prefix must not pass.
		{"suffix must not match", []string{"azurecr.io"}, "contoso.azurecr.io", false},
		{"prefix must not match", []string{"contoso.azurecr.io"}, "contoso.azurecr.io.evil.com", false},
		{"substring must not match", []string{"contoso"}, "contoso.azurecr.io", false},
	}

	for _, c := range cases {
		p := Policies{RegistryAllowlist: c.allowlist}
		if got := p.RegistryAllowed(c.server); got != c.want {
			t.Errorf("%s: RegistryAllowed(%q) with %v = %v, want %v",
				c.name, c.server, c.allowlist, got, c.want)
		}
	}
}

func TestRestrictiveReportsWhetherAnythingIsConstrained(t *testing.T) {
	open := Policies{ContainersAllowed: true, PrivilegedAllowed: true}
	if open.Restrictive() {
		t.Error("an unconfigured policy reported itself restrictive")
	}

	cases := map[string]Policies{
		"containers denied":  {ContainersAllowed: false, PrivilegedAllowed: true},
		"privileged denied":  {ContainersAllowed: true, PrivilegedAllowed: false},
		"registry allowlist": {ContainersAllowed: true, PrivilegedAllowed: true, RegistryAllowlist: []string{"a.example"}},
	}
	for name, p := range cases {
		if !p.Restrictive() {
			t.Errorf("%s: Restrictive() = false", name)
		}
	}
}

// WSL refuses operations it cannot attribute to a registry whenever the
// allowlist is active — `wslc image build` does exactly that. Skrog needs the
// same signal to take the same decision, so this must be true only when the
// allowlist actually constrains something.
func TestHasRegistryAllowlist(t *testing.T) {
	if (Policies{}).HasRegistryAllowlist() {
		t.Error("an unconfigured policy claimed an allowlist")
	}
	if (Policies{RegistryAllowlist: []string{}}).HasRegistryAllowlist() {
		t.Error("an empty allowlist claimed to be in force")
	}
	if !(Policies{RegistryAllowlist: []string{"a.example"}}).HasRegistryAllowlist() {
		t.Error("a configured allowlist did not report itself")
	}
}
