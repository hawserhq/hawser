package doctor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hawserhq/hawser/internal/version"
)

func TestWorst(t *testing.T) {
	if got := Worst(nil); got != OK {
		t.Errorf("empty: got %v, want OK", got)
	}
	results := []Result{
		{Status: OK}, {Status: Warn}, {Status: Skip},
	}
	if got := Worst(results); got != Warn {
		t.Errorf("got %v, want Warn", got)
	}
	results = append(results, Result{Status: Fail})
	if got := Worst(results); got != Fail {
		t.Errorf("got %v, want Fail", got)
	}
}

// report0 is a minimal, non-nil version.Report so Run can execute every check
// without a real machine.
func report0() *version.Report { return &version.Report{} }

func TestRunSmoke(t *testing.T) {
	reg := Registry()
	results := Run(reg, Facts{Report: report0()})
	if len(results) != len(reg) {
		t.Fatalf("got %d results, want %d", len(results), len(reg))
	}
	for _, r := range results {
		if r.Name == "" || r.Title == "" || r.StatusText == "" {
			t.Errorf("incomplete result: %+v", r)
		}
	}
}

func TestApplyFixesRunsFixAndReRuns(t *testing.T) {
	ran := false
	reg := []Check{{
		Name:  "x",
		Title: "X",
		Run: func(f Facts) Result {
			if ran {
				return Result{Name: "x", Title: "X", Status: OK, StatusText: "ok", Summary: "now ok"}
			}
			return Result{Name: "x", Title: "X", Status: Warn, StatusText: "warn", Summary: "not ok"}
		},
		Fix: func(ctx context.Context, f Facts) (string, error) { ran = true; return "did it", nil },
	}}
	results := Run(reg, Facts{})
	if results[0].Status != Warn {
		t.Fatalf("precondition: want Warn, got %v", results[0].Status)
	}
	fixed := ApplyFixes(context.Background(), reg, Facts{}, results)
	if fixed[0].Status != OK || fixed[0].Fixed != "did it" {
		t.Fatalf("after fix: %+v", fixed[0])
	}
}

func TestApplyFixesRecordsError(t *testing.T) {
	reg := []Check{{
		Name: "x", Title: "X",
		Run: func(f Facts) Result { return Result{Name: "x", Status: Fail, StatusText: "fail"} },
		Fix: func(ctx context.Context, f Facts) (string, error) { return "", errors.New("nope") },
	}}
	results := ApplyFixes(context.Background(), reg, Facts{}, Run(reg, Facts{}))
	if results[0].Status != Fail {
		t.Errorf("status should stay Fail, got %v", results[0].Status)
	}
	joined := strings.Join(results[0].Detail, " ")
	if !strings.Contains(joined, "fix failed") {
		t.Errorf("expected fix-failed detail, got %q", joined)
	}
}

func TestWriteJSONRoundTrips(t *testing.T) {
	results := []Result{{Name: "wsl", Title: "WSL2", Status: Warn, StatusText: "warn", Summary: "old"}}
	var buf bytes.Buffer
	if err := WriteJSON(&buf, "1.2.3", results); err != nil {
		t.Fatal(err)
	}
	var rep Report
	if err := json.Unmarshal(buf.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.App != "1.2.3" || rep.Worst != "warn" || len(rep.Results) != 1 || rep.Results[0].StatusText != "warn" {
		t.Fatalf("round-trip mismatch: %+v", rep)
	}
}

func TestWriteTextAndReport(t *testing.T) {
	results := []Result{
		{Title: "WSL2", Status: OK, StatusText: "ok", Summary: "fine"},
		{Title: "CLI", Status: Fail, StatusText: "fail", Summary: "no docker | none", Remedy: "install it"},
	}
	var txt bytes.Buffer
	if err := WriteText(&txt, "1.0", results); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(txt.String(), "no docker") || !strings.Contains(txt.String(), "problem") {
		t.Errorf("text output missing content:\n%s", txt.String())
	}

	var md bytes.Buffer
	if err := WriteMarkdownReport(&md, "1.0", results); err != nil {
		t.Fatal(err)
	}
	out := md.String()
	if !strings.Contains(out, "| CLI | fail |") {
		t.Errorf("markdown table missing row:\n%s", out)
	}
	if !strings.Contains(out, `no docker \| none`) {
		t.Errorf("markdown should escape pipes in cells:\n%s", out)
	}
	if !strings.Contains(out, "### remedies") {
		t.Errorf("markdown should list remedies:\n%s", out)
	}
}
