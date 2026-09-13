package dockercli

import "fmt"

// ShadowAdvice explains what to do when some other docker.exe resolves ahead of
// the bundled one.
//
// It lives here rather than in either caller because `skrog cli install` and
// `skrog doctor` both say it. They said it differently, and both said something
// that could not work (#282): "open a new terminal" and "make sure Skrog's bin
// directory precedes it on PATH" are both impossible when the shadow is on the
// machine PATH, because Windows composes a process PATH as machine-then-user
// and Skrog only ever writes the user half.
//
// scope is where the shadowing entry lives, activeDir the directory holding the
// docker that currently wins, and origin what `skrog version` attributed it to
// -- empty or "unknown" when nothing recognised it. The old text said "likely
// Docker Desktop" regardless, and was wrong on a machine where the shadowing
// binary had simply been unzipped into Program Files.
//
// The scope is a parameter rather than something this function looks up,
// because `skrog doctor` calls it from a check, and doctor's checks are pure
// functions of the Facts gathered once up front -- reading the registry here
// would make them untestable without one.
func ShadowAdvice(scope Scope, activeDir, origin string) string {
	switch scope {
	case ScopeMachine:
		return fmt.Sprintf(
			"%s is on the system PATH, which Windows always resolves before your user PATH. "+
				"Skrog installs to the user PATH so it needs no elevation, which means it cannot be "+
				"ordered ahead of this one -- opening a new terminal will not help. "+
				"As an administrator, remove %s from the system PATH, or remove the docker.exe in it.",
			who(origin), activeDir)
	case ScopeUser:
		return fmt.Sprintf(
			"open a new terminal -- a PATH change only reaches shells started after it. "+
				"If it persists, %s sits ahead of Skrog's bin directory on your user PATH; "+
				"move Skrog's entry first, or remove %s.",
			who(origin), activeDir)
	default:
		return fmt.Sprintf(
			"open a new terminal -- a PATH change only reaches shells started after it. "+
				"If it persists, %s is ahead of Skrog's bin directory on PATH without being in "+
				"either the user or the system PATH, so something in this shell's profile is "+
				"putting %s there.",
			who(origin), activeDir)
	}
}

// who names the shadowing docker in the SUBJECT position of a sentence, in a
// way that stays true whether or not it was recognised. Saying "Docker Desktop"
// when the origin is unknown is a guess, and it is the guess that made the old
// advice useless.
//
// The empty origin is the common case, not an edge one: both callers pass ""
// whenever the active binary is not in the list version.FindDockerBinaries
// returns. It must never fall through to "", which would open the sentence with
// a space and no subject (#287).
//
// version.OriginLabel renders the same value for the *parenthetical* position
// ("(unrecognised install location)"), which does not read as a subject. The
// two are deliberately separate.
func who(origin string) string {
	if origin == "" || origin == "unknown" {
		return "that docker"
	}
	return origin
}
