package version_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wslkit/skrog/internal/version"
)

// fakeFS answers Stat from a set of paths, so PATH scanning needs no real files.
func fakeFS(present ...string) func(string) error {
	set := map[string]bool{}
	for _, p := range present {
		set[strings.ToLower(filepath.Clean(p))] = true
	}
	return func(p string) error {
		if set[strings.ToLower(filepath.Clean(p))] {
			return nil
		}
		return os.ErrNotExist
	}
}

func envWith(pathDirs []string, present []string, skrogBin string) version.Env {
	return version.Env{
		PathVar:  strings.Join(pathDirs, string(os.PathListSeparator)),
		PathExt:  ".EXE",
		SkrogBin: skrogBin,
		Stat:     fakeFS(present...),
		Getenv:   func(string) string { return "" },
	}
}

func TestFindDockerBinariesResolutionOrder(t *testing.T) {
	// The first match on PATH is the one that runs; everything else is context.
	dirs := []string{
		`C:\Program Files\Docker\Docker\resources\bin`,
		`C:\Users\me\AppData\Local\Skrog\bin`,
		`C:\ProgramData\chocolatey\bin`,
	}
	present := []string{
		`C:\Program Files\Docker\Docker\resources\bin\docker.exe`,
		`C:\Users\me\AppData\Local\Skrog\bin\docker.exe`,
		`C:\ProgramData\chocolatey\bin\docker.exe`,
	}
	got := version.FindDockerBinaries(envWith(dirs, present, `C:\Users\me\AppData\Local\Skrog\bin`))

	if len(got) != 3 {
		t.Fatalf("found %d binaries %+v, want 3", len(got), got)
	}
	if !got[0].First {
		t.Error("first entry is not marked First")
	}
	if got[1].First || got[2].First {
		t.Error("more than one entry marked First")
	}
	want := []version.Origin{
		version.OriginDockerDesktop, version.OriginSkrog, version.OriginChocolatey,
	}
	for i, w := range want {
		if got[i].Origin != w {
			t.Errorf("binary %d origin = %q, want %q (%s)", i, got[i].Origin, w, got[i].Path)
		}
	}
}

func TestClassifyOrigins(t *testing.T) {
	skrogBin := `C:\Tools\Skrog\bin`
	tests := []struct {
		path string
		want version.Origin
	}{
		{`C:\Tools\Skrog\bin\docker.exe`, version.OriginSkrog},
		{`C:\Program Files\Docker\Docker\resources\bin\docker.exe`, version.OriginDockerDesktop},
		{`C:\Users\me\AppData\Local\Programs\rancher-desktop\resources\bin\docker.exe`, version.OriginRancher},
		{`C:\Users\me\AppData\Local\Microsoft\WinGet\Links\docker.exe`, version.OriginWinget},
		{`C:\ProgramData\chocolatey\bin\docker.exe`, version.OriginChocolatey},
		{`C:\Users\me\scoop\shims\docker.exe`, version.OriginScoop},
		{`D:\somewhere\odd\docker.exe`, version.OriginUnknown},
	}
	for _, tt := range tests {
		t.Run(string(tt.want), func(t *testing.T) {
			got := version.FindDockerBinaries(envWith(
				[]string{filepath.Dir(tt.path)}, []string{tt.path}, skrogBin))
			if len(got) != 1 {
				t.Fatalf("found %d, want 1", len(got))
			}
			if got[0].Origin != tt.want {
				t.Errorf("origin = %q, want %q", got[0].Origin, tt.want)
			}
		})
	}
}

func TestFindDockerBinariesDeduplicates(t *testing.T) {
	// A directory listed twice in PATH must not produce two entries.
	dir := `C:\bin`
	env := envWith([]string{dir, dir}, []string{dir + `\docker.exe`}, "")
	if got := version.FindDockerBinaries(env); len(got) != 1 {
		t.Errorf("found %d binaries, want 1: %+v", len(got), got)
	}
}

func TestFindDockerBinariesOneEntryPerDirectory(t *testing.T) {
	// Docker Desktop ships both docker.exe and an extensionless docker in the
	// same directory. PATH resolves one file per directory, so reporting two
	// would dress noise up as a second installation. Found by running
	// `skrog version` on a machine with Docker Desktop installed.
	dir := `C:\Program Files\Docker\Docker\resources\bin`
	env := envWith([]string{dir}, []string{dir + `\docker.exe`, dir + `\docker`}, "")

	got := version.FindDockerBinaries(env)
	if len(got) != 1 {
		t.Fatalf("found %d binaries, want 1: %+v", len(got), got)
	}
	// The .exe is what Windows actually runs, so that is what gets reported.
	if !strings.HasSuffix(got[0].Path, ".exe") {
		t.Errorf("reported %q, want the .exe", got[0].Path)
	}
}

func TestFindDockerBinariesNone(t *testing.T) {
	env := envWith([]string{`C:\empty`}, nil, "")
	if got := version.FindDockerBinaries(env); len(got) != 0 {
		t.Errorf("found %+v, want none", got)
	}
}

func TestDockerContextPrecedence(t *testing.T) {
	// DOCKER_HOST wins and means no context is in play; then DOCKER_CONTEXT;
	// then config.json. This mirrors the docker CLI, and getting the order
	// wrong would make the report actively misleading.
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfg, []byte(`{"currentContext":"from-config"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	base := version.Env{DockerConfigDir: dir, Stat: fakeFS()}

	t.Run("DOCKER_HOST overrides", func(t *testing.T) {
		e := base
		e.Getenv = func(k string) string {
			if k == "DOCKER_HOST" {
				return "npipe:////./pipe/skrog_engine"
			}
			return ""
		}
		name, src := version.DockerContext(e)
		if name != "" {
			t.Errorf("context = %q, want empty when DOCKER_HOST is set", name)
		}
		if !strings.Contains(src, "DOCKER_HOST") {
			t.Errorf("source = %q, should name DOCKER_HOST", src)
		}
	})

	t.Run("DOCKER_CONTEXT beats config", func(t *testing.T) {
		e := base
		e.Getenv = func(k string) string {
			if k == "DOCKER_CONTEXT" {
				return "from-env"
			}
			return ""
		}
		name, src := version.DockerContext(e)
		if name != "from-env" || src != "DOCKER_CONTEXT" {
			t.Errorf("got (%q, %q), want (from-env, DOCKER_CONTEXT)", name, src)
		}
	})

	t.Run("config.json", func(t *testing.T) {
		e := base
		e.Getenv = func(string) string { return "" }
		name, src := version.DockerContext(e)
		if name != "from-config" {
			t.Errorf("context = %q, want from-config", name)
		}
		if !strings.Contains(src, "config.json") {
			t.Errorf("source = %q, should name config.json", src)
		}
	})

	t.Run("no config falls back to default", func(t *testing.T) {
		e := version.Env{
			DockerConfigDir: filepath.Join(dir, "nonexistent"),
			Getenv:          func(string) string { return "" },
		}
		name, _ := version.DockerContext(e)
		if name != "default" {
			t.Errorf("context = %q, want default", name)
		}
	})

	t.Run("malformed config falls back to default", func(t *testing.T) {
		bad := t.TempDir()
		if err := os.WriteFile(filepath.Join(bad, "config.json"), []byte("{not json"), 0o644); err != nil {
			t.Fatal(err)
		}
		e := version.Env{DockerConfigDir: bad, Getenv: func(string) string { return "" }}
		if name, _ := version.DockerContext(e); name != "default" {
			t.Errorf("context = %q, want default", name)
		}
	})
}

func TestWarnsOnPathShadowing(t *testing.T) {
	// The headline diagnostic: the skrog context is active but Docker
	// Desktop's binary runs first. Commands succeed while doing something the
	// user did not intend, which is why this needs saying out loud.
	dirs := []string{`C:\Program Files\Docker\Docker\resources\bin`, `C:\Skrog\bin`}
	present := []string{
		`C:\Program Files\Docker\Docker\resources\bin\docker.exe`,
		`C:\Skrog\bin\docker.exe`,
	}
	env := envWith(dirs, present, `C:\Skrog\bin`)
	env.Getenv = func(k string) string {
		if k == "DOCKER_CONTEXT" {
			return "skrog"
		}
		return ""
	}

	c := &version.Collector{App: "1.2.3", Env: env}
	r := c.Collect(context.Background())

	found := false
	for _, w := range r.Warnings {
		if strings.Contains(w, "resolves first on PATH") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %+v, want one about PATH shadowing", r.Warnings)
	}
}

func TestNoShadowingWarningWhenSkrogIsFirst(t *testing.T) {
	dirs := []string{`C:\Skrog\bin`, `C:\Program Files\Docker\Docker\resources\bin`}
	present := []string{
		`C:\Skrog\bin\docker.exe`,
		`C:\Program Files\Docker\Docker\resources\bin\docker.exe`,
	}
	env := envWith(dirs, present, `C:\Skrog\bin`)
	env.Getenv = func(k string) string {
		if k == "DOCKER_CONTEXT" {
			return "skrog"
		}
		return ""
	}

	r := (&version.Collector{App: "1.2.3", Env: env}).Collect(context.Background())
	for _, w := range r.Warnings {
		if strings.Contains(w, "resolves first on PATH") {
			t.Errorf("unexpected shadowing warning: %s", w)
		}
	}
}

func TestWarnsWhenNoDockerFound(t *testing.T) {
	env := envWith([]string{`C:\empty`}, nil, "")
	r := (&version.Collector{App: "1.2.3", Env: env}).Collect(context.Background())

	found := false
	for _, w := range r.Warnings {
		if strings.Contains(w, "no docker.exe") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %+v, want one about a missing docker.exe", r.Warnings)
	}
}

func TestWarnsWhenNoEngineInstalled(t *testing.T) {
	env := envWith([]string{`C:\bin`}, []string{`C:\bin\docker.exe`}, "")
	r := (&version.Collector{App: "1.2.3", Env: env}).Collect(context.Background())
	if r.Engine.Installed {
		t.Fatal("Engine.Installed true with no manifest")
	}
	found := false
	for _, w := range r.Warnings {
		if strings.Contains(w, "skrog install") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %+v, want one suggesting skrog install", r.Warnings)
	}
}

func TestAPIProbeIsBestEffort(t *testing.T) {
	// `skrog version` is what someone runs when things are broken, so an
	// unreachable engine must not fail the command.
	env := envWith([]string{`C:\bin`}, []string{`C:\bin\docker.exe`}, "")

	failing := &version.Collector{App: "1.0.0", Env: env,
		ProbeAPI: func(context.Context) (string, error) {
			return "", errors.New("engine not reachable")
		}}
	if r := failing.Collect(context.Background()); r.APIVersion != "" {
		t.Errorf("APIVersion = %q, want empty when the probe fails", r.APIVersion)
	}

	ok := &version.Collector{App: "1.0.0", Env: env,
		ProbeAPI: func(context.Context) (string, error) { return "1.55", nil }}
	if r := ok.Collect(context.Background()); r.APIVersion != "1.55" {
		t.Errorf("APIVersion = %q, want 1.55", r.APIVersion)
	}
}

func TestJSONIsStableForScripts(t *testing.T) {
	// CI scripts assert on this, so the field names are a contract.
	env := envWith([]string{`C:\bin`}, []string{`C:\bin\docker.exe`}, `C:\bin`)
	r := (&version.Collector{App: "1.2.3", Env: env}).Collect(context.Background())

	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var generic map[string]any
	if err := json.Unmarshal(b, &generic); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"app", "engine", "docker", "context", "contextSource"} {
		if _, ok := generic[key]; !ok {
			t.Errorf("JSON is missing the %q field: %s", key, b)
		}
	}
	engine := generic["engine"].(map[string]any)
	if _, ok := engine["installed"]; !ok {
		t.Errorf("engine object is missing %q", "installed")
	}
}

func TestWriteTextIncludesTheDecisiveFacts(t *testing.T) {
	dirs := []string{`C:\Program Files\Docker\Docker\resources\bin`, `C:\Skrog\bin`}
	present := []string{
		`C:\Program Files\Docker\Docker\resources\bin\docker.exe`,
		`C:\Skrog\bin\docker.exe`,
	}
	env := envWith(dirs, present, `C:\Skrog\bin`)
	env.Getenv = func(k string) string {
		if k == "DOCKER_CONTEXT" {
			return "skrog"
		}
		return ""
	}
	r := (&version.Collector{App: "1.2.3", Env: env}).Collect(context.Background())

	var sb strings.Builder
	if err := r.WriteText(&sb); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	out := sb.String()

	for _, want := range []string{
		"1.2.3",             // app version
		"skrog",             // context
		"docker-desktop",    // the competing binary is named
		`C:\Skrog\bin`,      // and so is ours
		"resolves first",    // the shadowing warning
		"the one that runs", // the legend explaining the marker
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q:\n%s", want, out)
		}
	}
}
