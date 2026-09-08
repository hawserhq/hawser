package lockfile

import (
	"strings"
	"testing"

	"github.com/zcsizmadia/hawser/internal/release"
)

const goodSHA = "aee4312306d7d613ca3d0c23049c19837707cd4837b266ea057b544ac9605af4"

func TestFromEngineAndRoundTrip(t *testing.T) {
	e := &release.Engine{
		Version:    "29.7.2",
		Rootfs:     release.Rootfs{URL: "https://example.com/rootfs.tar.gz", SHA256: goodSHA},
		Components: map[string]string{"dockerd": "29.7.2", "runc": "1.3.0"},
	}
	l := FromEngine(e)
	if l.SchemaVersion != SchemaVersion || l.EngineVersion != "29.7.2" ||
		l.Rootfs.SHA256 != goodSHA || l.Components["runc"] != "1.3.0" {
		t.Fatalf("FromEngine wrong: %+v", l)
	}

	b, err := l.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if b[len(b)-1] != '\n' {
		t.Error("marshal should end in a newline")
	}
	got, err := Parse(b)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if got.EngineVersion != l.EngineVersion || got.Rootfs.URL != l.Rootfs.URL ||
		got.Rootfs.SHA256 != l.Rootfs.SHA256 || got.Components["dockerd"] != "29.7.2" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

// FromEngine must copy the components map, not alias the engine's.
func TestFromEngineCopiesComponents(t *testing.T) {
	comps := map[string]string{"dockerd": "29.7.2"}
	e := &release.Engine{Version: "v", Rootfs: release.Rootfs{URL: "u", SHA256: goodSHA}, Components: comps}
	l := FromEngine(e)
	l.Components["dockerd"] = "mutated"
	if comps["dockerd"] != "29.7.2" {
		t.Error("FromEngine aliased the engine's components map")
	}
}

func TestValidate(t *testing.T) {
	base := func() Lock {
		return Lock{SchemaVersion: SchemaVersion, EngineVersion: "29.7.2",
			Rootfs: Rootfs{URL: "https://x/y.tar.gz", SHA256: goodSHA}}
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("valid lock rejected: %v", err)
	}

	cases := map[string]func(*Lock){
		"bad schema":    func(l *Lock) { l.SchemaVersion = 99 },
		"no version":    func(l *Lock) { l.EngineVersion = "" },
		"no url":        func(l *Lock) { l.Rootfs.URL = "" },
		"short sha":     func(l *Lock) { l.Rootfs.SHA256 = "abc123" },
		"uppercase sha": func(l *Lock) { l.Rootfs.SHA256 = strings.ToUpper(goodSHA) },
		"non-hex sha":   func(l *Lock) { l.Rootfs.SHA256 = strings.Repeat("g", 64) },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			l := base()
			mut(&l)
			if err := l.Validate(); err == nil {
				t.Errorf("expected %s to be rejected", name)
			}
		})
	}
}

func TestParseRejectsUnknownField(t *testing.T) {
	_, err := Parse([]byte(`{"schemaVersion":1,"engineVersion":"1","rootfs":{"url":"u","sha256":"` + goodSHA + `"},"extra":true}`))
	if err == nil {
		t.Fatal("unknown field should be rejected")
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	if _, err := Parse([]byte("not json")); err == nil {
		t.Fatal("garbage should fail to parse")
	}
}
