package doctor_test

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/wslkit/skrog/internal/doctor"
)

// docs/cli-json.md calls itself "the contract that tools build on", and it
// described this payload as an array of results with exit 1 meaning warn.
// Both were wrong (#238): it is an object, and a warning exits 0.
//
// Pinning the top-level keys here means a change to the shape fails a test
// whose name points at the document that has to change with it.
func TestDoctorJSONTopLevelShapeMatchesTheDocumentedContract(t *testing.T) {
	b, err := json.Marshal(doctor.Report{
		App:     "0.4.0",
		Worst:   "ok",
		Results: []doctor.Result{{Name: "n", Title: "t", StatusText: "ok", Summary: "s"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil {
		t.Fatalf("doctor --json is not a JSON object, which docs/cli-json.md documents it as: %v", err)
	}
	var got []string
	for k := range top {
		got = append(got, k)
	}
	sort.Strings(got)
	want := []string{"app", "results", "worst"}
	if len(got) != len(want) {
		t.Fatalf("top-level keys are %v, documented as %v — update docs/cli-json.md with this change", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("top-level keys are %v, documented as %v — update docs/cli-json.md with this change", got, want)
			break
		}
	}
}

// And the exit-code rule the doc now states: a warning is not a failure.
func TestWorstFoldsWarningsBelowFailure(t *testing.T) {
	warnOnly := []doctor.Result{{Status: doctor.OK}, {Status: doctor.Warn}, {Status: doctor.Skip}}
	if w := doctor.Worst(warnOnly); w == doctor.Fail {
		t.Error("a run with only warnings reports Fail, so `doctor` would exit 1 on a healthy machine")
	}
	withFail := append(warnOnly, doctor.Result{Status: doctor.Fail})
	if w := doctor.Worst(withFail); w != doctor.Fail {
		t.Errorf("Worst() = %v with a failing check, want Fail", w)
	}
}
