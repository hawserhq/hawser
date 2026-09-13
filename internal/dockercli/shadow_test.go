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

// #289: mutating `who` so the empty origin falls through left every other test
// passing, while the message became " is on the system PATH..." -- a sentence
// with no subject. And "" is what both production callers usually pass.
func TestShadowAdviceAlwaysHasASubject(t *testing.T) {
	for _, origin := range []string{"", "unknown"} {
		advice := dockercli.ShadowAdvice(dockercli.ScopeMachine, `C:\x`, origin)
		if !strings.Contains(advice, "that docker") {
			t.Errorf("origin %q: no subject in %q", origin, advice)
		}
		if strings.HasPrefix(advice, " ") {
			t.Errorf("origin %q: advice opens with a space, so the subject is missing: %q",
				origin, advice)
		}
	}
}

// #289: moving the ScopeUser branch so user scope fell through to the profile
// text left all four original tests passing -- they only asserted things the
// unknown-scope text also satisfies. Pin what is distinctive about each branch.
func TestShadowAdviceBranchesAreDistinguishable(t *testing.T) {
	machine := dockercli.ShadowAdvice(dockercli.ScopeMachine, `C:\m`, "")
	user := dockercli.ShadowAdvice(dockercli.ScopeUser, `C:\u`, "")
	unknown := dockercli.ShadowAdvice(dockercli.ScopeUnknown, `C:\n`, "")

	if machine == user || user == unknown || machine == unknown {
		t.Fatal("two scopes produce the same advice; the branch is not doing anything")
	}
	// The user branch's whole point is the reorder instruction.
	if !strings.Contains(user, "move Skrog's entry first") {
		t.Errorf("user-scope advice lost its reorder instruction: %q", user)
	}
	if strings.Contains(unknown, "move Skrog's entry first") {
		t.Errorf("unknown-scope advice should not tell the user to reorder a PATH it is not on: %q", unknown)
	}
	// Positive assertion rather than forbidding "open a new terminal": the old
	// check failed on a rewording that meant the same thing (#289).
	if !strings.Contains(machine, "will not help") {
		t.Errorf("machine-scope advice must say a new terminal will not help: %q", machine)
	}
}
