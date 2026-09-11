package config

import (
	"strings"
	"testing"
)

func TestAppliesAnswersForEveryKey(t *testing.T) {
	// A key added without an answer here would silently fall through to "in
	// effect now", which is exactly the wrong direction to be wrong in: it is
	// the claim that was false about the audit log for two releases.
	valid := map[string]bool{
		AppliesNow:     true,
		AppliesOnStart: true,
		AppliesOnUse:   true,
	}
	for _, k := range Keys() {
		got := Applies(k)
		if !valid[got] {
			t.Errorf("Applies(%q) = %q, which is none of the three answers", k, got)
		}
	}
}

func TestAppliesMatchesWhatTheSupervisorDoes(t *testing.T) {
	// The contract, written down. Each expectation names the mechanism, so a
	// change to one without the other fails here rather than in a user's
	// terminal.
	cases := []struct {
		key  string
		want string
		why  string
	}{
		{KeyAudit, AppliesNow, "audit.Switch follows the setting per request"},
		{KeyIdleTimeout, AppliesNow, "the supervisor reads it per health tick"},
		{KeyHookPostStart, AppliesNow, "hookRunner reads the path when the event fires"},
		{KeyProxy, AppliesOnStart, "engineAdapter.Start re-reads it and dockerd must restart"},
		{KeyNoProxy, AppliesOnStart, "same path as the proxy"},
		{KeyImportHostCAs, AppliesOnStart, "the CA bundle is injected at engine start"},
		{KeyGPU, AppliesOnStart, "the CDI spec is installed at engine start"},
		{KeyVerifySignature, AppliesOnUse, "only `hawser install` reads it"},
		{KeyDiskWarnBelow, AppliesOnUse, "only `hawser doctor` reads it"},
	}
	for _, c := range cases {
		if got := Applies(c.key); got != c.want {
			t.Errorf("Applies(%q) = %q, want %q (%s)", c.key, got, c.want, c.why)
		}
	}
}

func TestAppliesCoversEveryHookKey(t *testing.T) {
	for _, k := range HookKeys() {
		if got := Applies(k); got != AppliesNow {
			t.Errorf("Applies(%q) = %q, want %q — hooks are read when the event fires",
				k, got, AppliesNow)
		}
	}
}

func TestAppliesSentencesReadAsSentences(t *testing.T) {
	// They are printed straight after "key = value", so a trailing period or
	// a capital would look wrong there.
	for _, s := range []string{AppliesNow, AppliesOnStart, AppliesOnUse} {
		if strings.HasSuffix(s, ".") {
			t.Errorf("%q ends with a period", s)
		}
		if s == "" || strings.ToLower(s[:1]) != s[:1] {
			t.Errorf("%q should start lower-case", s)
		}
	}
}
