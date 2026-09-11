package main

import (
	"reflect"
	"testing"
)

func TestSplitSubcommand(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantSub  string
		wantRest []string
	}{
		{
			// The form the usage line advertises, and the one that used to be
			// rejected with a usage error.
			name:     "subcommand then flags",
			args:     []string{"tail", "-n", "5"},
			wantSub:  "tail",
			wantRest: []string{"-n", "5"},
		},
		{
			// The older spelling has to keep working: the flag parser picks
			// the subcommand out of the remaining arguments instead.
			name:     "flags then subcommand",
			args:     []string{"--json", "tail"},
			wantSub:  "",
			wantRest: []string{"--json", "tail"},
		},
		{
			name:     "subcommand alone",
			args:     []string{"tail"},
			wantSub:  "tail",
			wantRest: []string{},
		},
		{
			name:     "trace keeps its command after the separator",
			args:     []string{"trace", "--", "docker", "run", "-it", "x"},
			wantSub:  "trace",
			wantRest: []string{"--", "docker", "run", "-it", "x"},
		},
		{
			name:     "nothing at all",
			args:     nil,
			wantSub:  "",
			wantRest: nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sub, rest := splitSubcommand(c.args)
			if sub != c.wantSub {
				t.Errorf("sub = %q, want %q", sub, c.wantSub)
			}
			if !reflect.DeepEqual(rest, c.wantRest) && !(len(rest) == 0 && len(c.wantRest) == 0) {
				t.Errorf("rest = %v, want %v", rest, c.wantRest)
			}
		})
	}
}
