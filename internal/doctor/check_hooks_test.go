package doctor

import (
	"strings"
	"testing"

	"github.com/hawserhq/hawser/internal/hooks"
)

func TestHooksNoneInjected(t *testing.T) {
	got := checkHooks().Run(Facts{})
	if got.Status != OK {
		t.Errorf("status = %v, want OK: %s", got.Status, got.Summary)
	}
	if got.Remedy != "" {
		t.Error("a clean process should carry no remedy")
	}
}

func TestHooksWarnsAndNamesTheModule(t *testing.T) {
	f := Facts{InjectedModules: []hooks.Module{
		{Name: "InProcessClient64.dll", Path: `C:\Program Files\SomeEDR\Agent\InProcessClient64.dll`},
	}}
	got := checkHooks().Run(f)
	// Never Fail: these agents are mandatory on managed machines and Hawser
	// works with them almost always. Naming it is the whole value.
	if got.Status != Warn {
		t.Errorf("status = %v, want Warn", got.Status)
	}
	if !strings.Contains(got.Summary, "InProcessClient64.dll") {
		t.Errorf("summary does not name the module: %q", got.Summary)
	}
	if !strings.Contains(strings.Join(got.Detail, "\n"), `C:\Program Files\SomeEDR`) {
		t.Errorf("detail does not show where it came from: %v", got.Detail)
	}
	// The remedy has to lead somewhere: the log to look at, the watchdog, and
	// the fact that the real fix is an exclusion, not a code change.
	for _, want := range []string{"supervisor-stderr.log", "watchdog.log", "exclusion", "issues/166"} {
		if !strings.Contains(got.Remedy, want) {
			t.Errorf("remedy is missing %q:\n%s", want, got.Remedy)
		}
	}
}

func TestHooksCountsEveryModule(t *testing.T) {
	f := Facts{InjectedModules: []hooks.Module{
		{Name: "a.dll", Path: `C:\Vendor\a.dll`},
		{Name: "b.dll", Path: `C:\Vendor\b.dll`},
	}}
	got := checkHooks().Run(f)
	if !strings.Contains(got.Summary, "2 third-party module") {
		t.Errorf("summary = %q, want a count of 2", got.Summary)
	}
	if !strings.Contains(got.Summary, "a.dll") || !strings.Contains(got.Summary, "b.dll") {
		t.Errorf("summary should name both: %q", got.Summary)
	}
}
