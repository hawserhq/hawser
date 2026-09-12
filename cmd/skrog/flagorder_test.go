package main

import (
	"flag"
	"io"
	"strings"
	"testing"
)

// `skrog snapshot restore golden --yes` is the recipe on docs/snapshots.md and
// it exited 2 without doing anything: Go's flag package stops at "restore", so
// --yes stayed in Args() and the command fell through to its usage text (#244).
func TestParseInterleavedAcceptsFlagsOnEitherSideOfTheVerb(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"flags after the verb (the documented recipe)", []string{"restore", "golden", "--yes"}},
		{"flags before the verb", []string{"--yes", "restore", "golden"}},
		{"flags on both sides", []string{"--json", "restore", "golden", "--yes"}},
		{"a flag between positionals", []string{"restore", "--yes", "golden"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("snapshot", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			yes := fs.Bool("yes", false, "")
			asJSON := fs.Bool("json", false, "")

			pos, err := parseInterleaved(fs, tc.args)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if !*yes {
				t.Error("--yes was not applied, so the command would refuse or prompt")
			}
			if got := strings.Join(pos, " "); got != "restore golden" {
				t.Errorf("positionals = %q, want \"restore golden\"", got)
			}
			_ = asJSON
		})
	}
}

// A genuinely bad flag must still be a usage error rather than being taken as
// a positional.
func TestParseInterleavedStillRejectsAnUnknownFlag(t *testing.T) {
	fs := flag.NewFlagSet("snapshot", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Bool("yes", false, "")
	if _, err := parseInterleaved(fs, []string{"restore", "golden", "--nope"}); err == nil {
		t.Error("an unknown flag was accepted")
	}
}
