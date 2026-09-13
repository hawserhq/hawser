package dockercli_test

import (
	"strings"
	"testing"

	"github.com/wslkit/skrog/internal/dockercli"
)

// The machine-PATH case is the whole point of #282: the advice that shipped
// told the user to open a new terminal and to order Skrog's directory ahead of
// the other one, and on a machine-PATH shadow neither is possible. Windows
// resolves the entire machine PATH before the entire user PATH, and Skrog only
// writes the user half.
func TestShadowAdviceDoesNotPromiseTheImpossible(t *testing.T) {
	advice := dockercli.ShadowAdvice(dockercli.ScopeMachine, `C:\Program Files\Docker`, "")

	for _, forbidden := range []string{"open a new terminal", "precedes", "Docker Desktop"} {
		if strings.Contains(strings.ToLower(advice), strings.ToLower(forbidden)) {
			t.Errorf("machine-PATH advice still says %q, which cannot work or is a guess:\n%s",
				forbidden, advice)
		}
	}
	for _, required := range []string{"system PATH", "administrator", `C:\Program Files\Docker`} {
		if !strings.Contains(advice, required) {
			t.Errorf("machine-PATH advice does not mention %q:\n%s", required, advice)
		}
	}
}

// A user-PATH shadow is the case the original advice was written for, and there
// it is right: a new terminal genuinely does pick up the change.
func TestShadowAdviceForAUserPathShadow(t *testing.T) {
	advice := dockercli.ShadowAdvice(dockercli.ScopeUser, `C:\Users\me\bin`, "")
	if !strings.Contains(advice, "open a new terminal") {
		t.Errorf("user-PATH advice should start with the thing that fixes it:\n%s", advice)
	}
	if strings.Contains(advice, "administrator") {
		t.Errorf("user-PATH advice should not send the user for elevation:\n%s", advice)
	}
}

// Neither PATH holds it, so something in the shell's own profile does. Saying
// "reorder your PATH" would send the user to a registry key that does not
// mention it.
func TestShadowAdviceWhenItIsOnNeitherPath(t *testing.T) {
	advice := dockercli.ShadowAdvice(dockercli.ScopeUnknown, `C:\tmp\docker`, "")
	if !strings.Contains(advice, "profile") {
		t.Errorf("unknown-scope advice should point at the shell profile:\n%s", advice)
	}
}

// The old text said "likely Docker Desktop" whatever was in the way, and was
// wrong on the machine that produced #282. Name it only when it was identified.
func TestShadowAdviceNamesOnlyWhatWasIdentified(t *testing.T) {
	cases := []struct {
		name, origin string
		wantNamed    bool
	}{
		{"identified", "docker-desktop", true},
		{"unrecognised", "unknown", false},
		{"not looked up", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			advice := dockercli.ShadowAdvice(dockercli.ScopeMachine, `C:\x`, tc.origin)
			named := strings.Contains(advice, tc.origin) && tc.origin != ""
			if named != tc.wantNamed {
				t.Errorf("origin %q: named=%v want %v:\n%s", tc.origin, named, tc.wantNamed, advice)
			}
			if !tc.wantNamed && strings.Contains(advice, "unknown") {
				t.Errorf("an unidentified docker should not be described as %q:\n%s", "unknown", advice)
			}
		})
	}
}
