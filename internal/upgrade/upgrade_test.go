package upgrade

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fixedReleases is a ReleaseLister that answers from the test.
type fixedReleases struct {
	latest string
	err    error
}

func (f fixedReleases) LatestApp(context.Context) (string, error) {
	return f.latest, f.err
}

// stream pulls one stream out of a report by name.
func stream(t *testing.T, r Report, name string) Stream {
	t.Helper()
	for _, s := range r.Streams {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no %q stream in %+v", name, r.Streams)
	return Stream{}
}

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
		why  string
	}{
		{"0.4.0", "0.3.0", 1, ""},
		{"0.3.0", "0.4.0", -1, ""},
		{"0.3.0", "0.3.0", 0, ""},
		{"v0.4.0", "0.3.0", 1, "a leading v is noise"},
		{"0.10.0", "0.9.0", 1, "segments are numbers, not text"},
		{"1.0", "1.0.0", 0, "a missing segment is zero"},
		{"1.0.1", "1.0", 1, ""},
		// The packaging revision sorts AFTER the bare version, which is the
		// opposite of how semver treats a pre-release suffix. Getting this
		// backwards would tell everyone on 29.8.0-1 to downgrade.
		{"29.8.0-1", "29.8.0", 1, "a revision is newer than the bare version"},
		{"29.8.0-2", "29.8.0-1", 1, "revisions order numerically"},
		{"29.8.0-1", "29.7.2-4", 1, "the version outranks the revision"},
		{"29.8.0", "29.8.0-1", -1, ""},
	}
	for _, c := range cases {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d %s", c.a, c.b, got, c.want, c.why)
		}
	}
}

func TestIsRelease(t *testing.T) {
	// Every false case here is a build a developer is actively testing, and
	// each one parses as 0.0.0 — below every release. Without this check they
	// would all be told an upgrade is available.
	for _, v := range []string{"0.3.0", "v0.3.0", "1.0.0"} {
		if !IsRelease(v) {
			t.Errorf("IsRelease(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"dev", "0.0.0-ci", "0.0.0-dryrun", "0.0.0", ""} {
		if IsRelease(v) {
			t.Errorf("IsRelease(%q) = true, want false", v)
		}
	}
}

func TestCheckReportsAnAppUpgrade(t *testing.T) {
	c := &Checker{
		App:          "0.3.0",
		EngineLatest: "29.8.0-1",
		CLILatest:    "29.8.0",
		Installed:    Installed{EngineVersion: "29.8.0-1", CLIVersion: "29.8.0"},
		Releases:     fixedReleases{latest: "0.4.0"},
	}
	rep := c.Check(context.Background())

	app := stream(t, rep, "app")
	if app.Status != StatusAvailable || app.Latest != "0.4.0" {
		t.Errorf("app = %+v, want 0.4.0 available", app)
	}
	if stream(t, rep, "engine").Status != StatusCurrent {
		t.Error("engine should be current")
	}
	if !rep.Available() {
		t.Error("Available() = false with an app upgrade waiting")
	}
}

func TestTheCouplingIsStatedWhenTheAppIsBehind(t *testing.T) {
	// The whole reason this command exists: the engines `engine upgrade` can
	// reach come from the app binary's manifest, so "engine: current" is only
	// true of THIS build. Saying so is the point.
	c := &Checker{
		App:          "0.3.0",
		EngineLatest: "29.8.0-1",
		CLILatest:    "29.8.0",
		Installed:    Installed{EngineVersion: "29.8.0-1", CLIVersion: "29.8.0"},
		Releases:     fixedReleases{latest: "0.4.0"},
	}
	rep := c.Check(context.Background())

	joined := strings.Join(rep.Notes, "\n")
	if !strings.Contains(joined, "pinned in this build's manifest") {
		t.Errorf("no coupling note in %q", joined)
	}
	if !strings.Contains(joined, "upgrade hawser first") {
		t.Errorf("the note must say which order to do it in, got %q", joined)
	}
}

func TestTheCouplingIsSilentWhenTheAppIsCurrent(t *testing.T) {
	// A warning printed every time is a warning nobody reads.
	c := &Checker{
		App:          "0.4.0",
		EngineLatest: "29.8.0-1",
		CLILatest:    "29.8.0",
		Installed:    Installed{EngineVersion: "29.8.0-1", CLIVersion: "29.8.0"},
		Releases:     fixedReleases{latest: "0.4.0"},
	}
	rep := c.Check(context.Background())
	for _, n := range rep.Notes {
		if strings.Contains(n, "pinned in this build's manifest") {
			t.Errorf("coupling note shown while the app is current: %q", n)
		}
	}
	if rep.Available() {
		t.Error("Available() = true with everything current")
	}
}

func TestOfflineStillAnswersTheLocalStreams(t *testing.T) {
	// Both manifests are compiled into the binary, so an air-gapped install
	// gets real engine and CLI answers — and an app stream that says it did
	// not look, rather than one that says "current".
	c := &Checker{
		App:          "0.3.0",
		EngineLatest: "29.8.0-1",
		CLILatest:    "29.8.0",
		Installed:    Installed{EngineVersion: "29.7.2-4", CLIVersion: "29.8.0"},
		Releases:     nil,
	}
	rep := c.Check(context.Background())

	if !rep.Offline {
		t.Error("Offline = false with no release lister")
	}
	app := stream(t, rep, "app")
	if app.Status != StatusUnknown {
		t.Errorf("app status = %q offline, want unknown — never report 'current' for 'did not look'", app.Status)
	}
	eng := stream(t, rep, "engine")
	if eng.Status != StatusAvailable || eng.Latest != "29.8.0-1" {
		t.Errorf("engine = %+v, want 29.8.0-1 available offline", eng)
	}
}

func TestAFailedLookupIsUnknownNotCurrent(t *testing.T) {
	c := &Checker{
		App:          "0.3.0",
		EngineLatest: "29.8.0-1",
		CLILatest:    "29.8.0",
		Installed:    Installed{EngineVersion: "29.8.0-1", CLIVersion: "29.8.0"},
		Releases:     fixedReleases{err: errors.New("dial tcp: no such host")},
	}
	rep := c.Check(context.Background())

	app := stream(t, rep, "app")
	if app.Status != StatusUnknown {
		t.Errorf("app status = %q after a failed lookup, want unknown", app.Status)
	}
	if !strings.Contains(app.Note, "no such host") {
		t.Errorf("the reason should reach the user, got %q", app.Note)
	}
	// The two local streams are still correct and still worth printing.
	if stream(t, rep, "engine").Status != StatusCurrent {
		t.Error("a failed app lookup must not poison the engine answer")
	}
}

func TestADevBuildIsNotToldToUpgrade(t *testing.T) {
	c := &Checker{
		App:          "dev",
		EngineLatest: "29.8.0-1",
		CLILatest:    "29.8.0",
		Installed:    Installed{EngineVersion: "29.8.0-1", CLIVersion: "29.8.0"},
		Releases:     fixedReleases{latest: "0.4.0"},
	}
	rep := c.Check(context.Background())

	app := stream(t, rep, "app")
	if app.Status == StatusAvailable {
		t.Error("a source build was told to upgrade")
	}
	if !strings.Contains(app.Note, "not a release build") {
		t.Errorf("note = %q, want an explanation", app.Note)
	}
	if rep.Available() {
		t.Error("Available() = true for a dev build with everything else current")
	}
}

func TestNotInstalledStreams(t *testing.T) {
	c := &Checker{
		App:          "0.3.0",
		EngineLatest: "29.8.0-1",
		CLILatest:    "29.8.0",
		Installed:    Installed{},
		Releases:     fixedReleases{latest: "0.3.0"},
	}
	rep := c.Check(context.Background())

	eng := stream(t, rep, "engine")
	if eng.Status != StatusNotInstalled || eng.Command != "hawser install" {
		t.Errorf("engine = %+v, want not-installed with an install command", eng)
	}
	cli := stream(t, rep, "cli")
	if cli.Status != StatusNotInstalled {
		t.Errorf("cli = %+v, want not-installed", cli)
	}
	// Nothing is *upgradable*: a missing component is not an upgrade, and the
	// exit code must not claim one.
	if rep.Available() {
		t.Error("Available() = true when the components are merely absent")
	}
}

func TestEngineUpgradeIsOfferedWhenTheManifestIsAhead(t *testing.T) {
	c := &Checker{
		App:          "0.3.0",
		EngineLatest: "29.8.0-1",
		CLILatest:    "29.8.0",
		Installed:    Installed{EngineVersion: "29.7.2-4", CLIVersion: "29.7.2"},
		Releases:     fixedReleases{latest: "0.3.0"},
	}
	rep := c.Check(context.Background())

	eng := stream(t, rep, "engine")
	if eng.Status != StatusAvailable || eng.Command != "hawser engine upgrade" {
		t.Errorf("engine = %+v, want an upgrade with its command", eng)
	}
	cli := stream(t, rep, "cli")
	if cli.Status != StatusAvailable || cli.Command != "hawser cli install" {
		t.Errorf("cli = %+v, want an upgrade with its command", cli)
	}
}

func TestWriteTextIsReadable(t *testing.T) {
	c := &Checker{
		App:          "0.3.0",
		EngineLatest: "29.8.0-1",
		CLILatest:    "29.8.0",
		Installed:    Installed{EngineVersion: "29.8.0-1", CLIVersion: "29.8.0"},
		Releases:     fixedReleases{latest: "0.4.0"},
	}
	var b strings.Builder
	if err := c.Check(context.Background()).WriteText(&b); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"app", "0.3.0", "0.4.0 available", "engine", "(current)", "note:"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}
