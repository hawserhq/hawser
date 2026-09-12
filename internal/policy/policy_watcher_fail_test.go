package policy_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wslkit/skrog/internal/policy"
)

// A rule file that has never parsed must refuse requests, not allow them.
//
// The watcher's error path keeps "the rules that were working" — but on the
// first load there are none, so it kept the zero Rules{}, which Empty()
// reports as "no policy" and EvaluateCreate allows everything through. That
// state survives a reboot: save a typo, restart, and admission control is off
// while the file on disk still looks enforced (#254).
//
// The package doc calls this "the one failure mode a guardrail must not have".
func TestUnparseablePolicyRefusesInsteadOfAllowingEverything(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, policy.FileName),
		[]byte("deny-privileged: yes\n  this is not valid yaml: ["), 0o644); err != nil {
		t.Fatal(err)
	}

	w := policy.NewWatcher(dir)

	if err := w.Unavailable(); err == nil {
		t.Error("Unavailable() is nil for a file that has never parsed")
	}

	// The request a policy like this exists to stop.
	body := map[string]any{"HostConfig": map[string]any{"Privileged": true}}
	reason, denied := w.DenyCreate(body)
	if !denied {
		t.Fatal("a --privileged create was allowed while the rule file could not be read")
	}
	for _, want := range []string{"cannot be read", "refused"} {
		if !strings.Contains(reason, want) {
			t.Errorf("the denial does not explain itself (%q missing):\n%s", want, reason)
		}
	}
}

// The two states must stay distinct: an absent file genuinely means "no rules",
// which is the default on a machine nobody has configured, and must keep
// allowing everything.
func TestAbsentPolicyStillAllowsEverything(t *testing.T) {
	w := policy.NewWatcher(t.TempDir())
	if err := w.Unavailable(); err != nil {
		t.Errorf("Unavailable() = %v for a machine with no policy file", err)
	}
	if _, denied := w.DenyCreate(map[string]any{
		"HostConfig": map[string]any{"Privileged": true},
	}); denied {
		t.Error("a machine with no policy file denied a request")
	}
}

// And a file that breaks *after* a good load keeps the rules that were
// working — the behaviour the error path was written for, which must survive
// this change.
func TestBrokenAfterAGoodLoadKeepsTheWorkingRules(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, policy.FileName)
	if err := os.WriteFile(p, []byte("deny-privileged: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := policy.NewWatcher(dir)
	if _, denied := w.DenyCreate(map[string]any{
		"HostConfig": map[string]any{"Privileged": true},
	}); !denied {
		t.Fatal("the good rule set did not deny --privileged")
	}

	if err := os.WriteFile(p, []byte("deny-privileged: true\nnonsense: ["), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := w.Unavailable(); err != nil {
		t.Errorf("a file that broke after a good load reports Unavailable: %v", err)
	}
	reason, denied := w.DenyCreate(map[string]any{
		"HostConfig": map[string]any{"Privileged": true},
	})
	if !denied {
		t.Error("the previously working rules were dropped when the file broke")
	}
	if strings.Contains(reason, "cannot be read") {
		t.Errorf("denied for the wrong reason; the working rules should still apply:\n%s", reason)
	}
}
