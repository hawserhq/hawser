package doctor

import (
	"strings"
	"testing"
)

func TestWSLSizingReportsWhatIsSet(t *testing.T) {
	f := Facts{WSLSizing: WSLSizingInfo{
		Path:      `C:\Users\me\.wslconfig`,
		Effective: map[string]string{"memory": "4GB", "processors": "2"},
		HostBytes: 32 << 30,
	}}
	got := checkWSLSizing().Run(f)
	if got.Status != OK {
		t.Errorf("status = %v, want OK: %s", got.Status, got.Summary)
	}
	if !strings.Contains(got.Summary, "memory=4GB") || !strings.Contains(got.Summary, "processors=2") {
		t.Errorf("summary does not report the sizing: %q", got.Summary)
	}
	if !strings.Contains(strings.Join(got.Detail, "\n"), `.wslconfig`) {
		t.Errorf("detail should name the file the values came from: %v", got.Detail)
	}
}

func TestWSLSizingDoesNotJudgeALargeHost(t *testing.T) {
	// WSL's 50% default is the right answer on a big machine. A check that
	// warned whenever no limit was set would cry wolf on most workstations.
	f := Facts{WSLSizing: WSLSizingInfo{
		Path:      `C:\Users\me\.wslconfig`,
		Effective: map[string]string{},
		HostBytes: 32 << 30,
	}}
	got := checkWSLSizing().Run(f)
	if got.Status != OK {
		t.Errorf("status = %v, want OK on a 32 GiB host: %s", got.Status, got.Summary)
	}
	if got.Remedy != "" {
		t.Errorf("a healthy machine got a remedy: %q", got.Remedy)
	}
}

func TestWSLSizingWarnsOnASmallHostWithNoLimits(t *testing.T) {
	// 8 GiB with WSL's default: the engine plus a build can push the host into
	// swap, and the symptom is a slow laptop rather than an engine error.
	f := Facts{WSLSizing: WSLSizingInfo{
		Path:      `C:\Users\me\.wslconfig`,
		Effective: map[string]string{},
		HostBytes: 8 << 30,
	}}
	got := checkWSLSizing().Run(f)
	if got.Status != Warn {
		t.Fatalf("status = %v, want Warn: %s", got.Status, got.Summary)
	}
	if !strings.Contains(got.Summary, "8.0 GiB") || !strings.Contains(got.Summary, "4.0 GiB") {
		t.Errorf("summary should name the host size and the VM's likely share: %q", got.Summary)
	}
	// The remedy must be runnable, and must mention that the file is shared.
	for _, want := range []string{"skrog config set wsl.memory", "skrog wsl-config apply", "every WSL2 distro"} {
		if !strings.Contains(got.Remedy, want) {
			t.Errorf("remedy is missing %q:\n%s", want, got.Remedy)
		}
	}
}

func TestWSLSizingDoesNotJudgeWithoutHostRAM(t *testing.T) {
	// hostRAM() returns 0 when it cannot be read; guessing from nothing would
	// be worse than staying quiet.
	f := Facts{WSLSizing: WSLSizingInfo{Path: `C:\x\.wslconfig`, Effective: map[string]string{}}}
	if got := checkWSLSizing().Run(f); got.Status != OK {
		t.Errorf("status = %v, want OK when host RAM is unknown", got.Status)
	}
}

func TestWSLSizingWarnsAboutUnappliedChanges(t *testing.T) {
	// The trap this catches: someone ran `skrog config set wsl.memory 4GB`,
	// never ran apply, and believes the engine is capped when it is not.
	f := Facts{WSLSizing: WSLSizingInfo{
		Path:      `C:\Users\me\.wslconfig`,
		Effective: map[string]string{"memory": "8GB"},
		Pending:   []string{"memory=4GB", "processors=2"},
		HostBytes: 32 << 30,
	}}
	got := checkWSLSizing().Run(f)
	if got.Status != Warn {
		t.Fatalf("status = %v, want Warn: %s", got.Status, got.Summary)
	}
	if !strings.Contains(got.Summary, "not applied") {
		t.Errorf("summary = %q", got.Summary)
	}
	if !strings.Contains(strings.Join(got.Detail, "\n"), "memory=4GB") {
		t.Errorf("detail should name the pending changes: %v", got.Detail)
	}
	if !strings.Contains(got.Remedy, "skrog wsl-config apply") {
		t.Errorf("remedy = %q", got.Remedy)
	}
}

func TestWSLSizingSkipsOnAReadError(t *testing.T) {
	f := Facts{WSLSizing: WSLSizingInfo{Path: `C:\x\.wslconfig`, Err: "permission denied"}}
	got := checkWSLSizing().Run(f)
	if got.Status != Skip {
		t.Errorf("status = %v, want Skip", got.Status)
	}
	if !strings.Contains(got.Summary, "permission denied") {
		t.Errorf("summary should carry the reason: %q", got.Summary)
	}
}

func TestHumanIEC(t *testing.T) {
	cases := map[uint64]string{
		8 << 30:   "8.0 GiB",
		32 << 30:  "32 GiB",
		512 << 20: "512 MiB",
		900:       "900 B",
	}
	for in, want := range cases {
		if got := humanIEC(in); got != want {
			t.Errorf("humanIEC(%d) = %q, want %q", in, got, want)
		}
	}
}
