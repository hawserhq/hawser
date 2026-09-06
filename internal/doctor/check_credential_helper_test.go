package doctor

import (
	"errors"
	"testing"
)

func TestParseCredHelpers(t *testing.T) {
	lookPath := func(name string) (string, error) {
		if name == "docker-credential-desktop" {
			return `C:\Program Files\Docker\docker-credential-desktop.exe`, nil
		}
		return "", errors.New("not found")
	}

	cfg := []byte(`{
		"credsStore": "desktop",
		"credHelpers": {"myregistry.example.com": "wincred"}
	}`)

	helpers := parseCredHelpers(cfg, lookPath)
	if len(helpers) != 2 {
		t.Fatalf("got %d helpers, want 2: %+v", len(helpers), helpers)
	}

	byName := map[string]CredHelper{}
	for _, h := range helpers {
		byName[h.Name] = h
	}
	if h := byName["desktop"]; !h.Resolved || h.Source != "credsStore" {
		t.Errorf("desktop = %+v; want resolved credsStore", h)
	}
	if h := byName["wincred"]; h.Resolved || h.Binary != "docker-credential-wincred" {
		t.Errorf("wincred = %+v; want unresolved", h)
	}
}

func TestParseCredHelpersEmptyAndBad(t *testing.T) {
	never := func(string) (string, error) { return "", errors.New("x") }
	if got := parseCredHelpers([]byte(`{}`), never); got != nil {
		t.Errorf("empty config: got %+v, want nil", got)
	}
	if got := parseCredHelpers([]byte(`not json`), never); got != nil {
		t.Errorf("bad json: got %+v, want nil", got)
	}
}

func TestCheckCredentialHelper(t *testing.T) {
	c := checkCredentialHelper()

	if got := c.Run(Facts{}).Status; got != OK {
		t.Errorf("no helpers: got %v, want OK", got)
	}

	resolved := Facts{CredHelpers: []CredHelper{{Name: "desktop", Binary: "docker-credential-desktop", Resolved: true, Path: "x"}}}
	if got := c.Run(resolved).Status; got != OK {
		t.Errorf("resolved: got %v, want OK", got)
	}

	missing := Facts{CredHelpers: []CredHelper{{Name: "wincred", Binary: "docker-credential-wincred", Resolved: false}}}
	if got := c.Run(missing).Status; got != Fail {
		t.Errorf("missing: got %v, want Fail", got)
	}
}
