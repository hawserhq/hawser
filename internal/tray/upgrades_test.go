package tray

import (
	"strings"
	"testing"

	"github.com/wslkit/skrog/internal/upgrade"
)

func TestSummarizeNamesWhatIsBehind(t *testing.T) {
	// Naming the components beats counting them: "3 updates available" makes
	// someone click to find out whether they care.
	rep := upgrade.Report{Streams: []upgrade.Stream{
		{Name: "app", Current: "0.3.0", Latest: "0.4.0", Status: upgrade.StatusAvailable},
		{Name: "engine", Current: "29.8.0", Latest: "29.8.0", Status: upgrade.StatusCurrent},
		{Name: "cli", Current: "29.7.2", Latest: "29.8.0", Status: upgrade.StatusAvailable},
	}}
	got := summarize(rep)

	if !got.Available {
		t.Error("Available = false with two upgrades waiting")
	}
	for _, want := range []string{"skrog 0.4.0", "docker CLI 29.8.0"} {
		if !strings.Contains(got.Summary, want) {
			t.Errorf("summary %q is missing %q", got.Summary, want)
		}
	}
	if strings.Contains(got.Summary, "engine") {
		t.Errorf("summary %q mentions a component that is current", got.Summary)
	}
}

func TestSummarizeUsesUserFacingNames(t *testing.T) {
	// "app" and "cli" are implementation words.
	rep := upgrade.Report{Streams: []upgrade.Stream{
		{Name: "app", Latest: "0.4.0", Status: upgrade.StatusAvailable},
	}}
	if got := summarize(rep).Summary; !strings.HasPrefix(got, "skrog 0.4.0") {
		t.Errorf("summary = %q, want it to start with the product name", got)
	}
}

func TestSummarizeSaysUpToDate(t *testing.T) {
	rep := upgrade.Report{Streams: []upgrade.Stream{
		{Name: "app", Status: upgrade.StatusCurrent},
		{Name: "engine", Status: upgrade.StatusCurrent},
		{Name: "cli", Status: upgrade.StatusCurrent},
	}}
	got := summarize(rep)
	if got.Available {
		t.Error("Available = true with everything current")
	}
	if !strings.Contains(got.Summary, "up to date") {
		t.Errorf("summary = %q", got.Summary)
	}
}

func TestSummarizeNeverCallsUnknownUpToDate(t *testing.T) {
	// Offline, or the releases API unreachable. Reporting "up to date" here
	// would be the same class of lie as the audit log claiming to be on.
	rep := upgrade.Report{Streams: []upgrade.Stream{
		{Name: "app", Status: upgrade.StatusUnknown, Note: "offline"},
		{Name: "engine", Status: upgrade.StatusCurrent},
		{Name: "cli", Status: upgrade.StatusCurrent},
	}}
	got := summarize(rep)
	if got.Available {
		t.Error("Available = true with nothing actually upgradable")
	}
	if strings.Contains(got.Summary, "up to date") {
		t.Errorf("summary = %q; an unknown stream is not 'up to date'", got.Summary)
	}
	if !strings.Contains(got.Summary, "could not check") {
		t.Errorf("summary = %q, want it to say the check was incomplete", got.Summary)
	}
}

func TestSummarizeIgnoresNotInstalled(t *testing.T) {
	// A component that was never installed is not an upgrade, and the tray
	// must not offer to open the releases page for it.
	rep := upgrade.Report{Streams: []upgrade.Stream{
		{Name: "app", Status: upgrade.StatusCurrent},
		{Name: "engine", Status: upgrade.StatusCurrent},
		{Name: "cli", Status: upgrade.StatusNotInstalled},
	}}
	if got := summarize(rep); got.Available {
		t.Errorf("Available = true for a component that is merely absent (%q)", got.Summary)
	}
}

func TestParseUpgradesRejectsNonsense(t *testing.T) {
	if _, err := parseUpgrades([]byte("not json")); err == nil {
		t.Error("no error on unparseable output")
	}
	if _, err := parseUpgrades([]byte(`{"streams":[]}`)); err == nil {
		t.Error("no error on a report with no components")
	}
}

func TestParseUpgradesReadsTheCLIShape(t *testing.T) {
	// Pinned against the real `skrog upgrade --json` output, so a change to
	// the CLI's shape breaks here rather than silently in the tray.
	const out = `{
	  "streams": [
	    {"name":"app","current":"0.3.0","latest":"0.4.0","status":"available",
	     "command":"download from https://github.com/wslkit/skrog/releases"},
	    {"name":"engine","current":"29.8.0","latest":"29.8.0","status":"current"},
	    {"name":"cli","current":"29.8.0","latest":"29.8.0","status":"current"}
	  ],
	  "notes": ["the engines ... are pinned in this build's manifest"],
	  "offline": false,
	  "checkedAt": "2026-09-11T12:28:33Z"
	}`
	rep, err := parseUpgrades([]byte(out))
	if err != nil {
		t.Fatalf("parseUpgrades: %v", err)
	}
	if len(rep.Streams) != 3 {
		t.Fatalf("got %d streams, want 3", len(rep.Streams))
	}
	got := summarize(rep)
	if !got.Available || !strings.Contains(got.Summary, "skrog 0.4.0") {
		t.Errorf("summary = %+v", got)
	}
}
