package main

import "flag"

// parseInterleaved parses a flag set where flags may appear on either side of
// the positional arguments, returning the positionals in order.
//
// Go's flag package stops at the first non-flag token, so
// `skrog snapshot restore golden --yes` leaves `--yes` sitting in Args() and
// the command falls through to its usage text — the documented recipe on
// docs/snapshots.md exits 2 without doing anything (#244).
//
// docs/cli-json.md states the rule ("flags come before the verb"), but a rule
// a user has to know is a worse answer than accepting both, and `skrog
// relocate` already accepts both with an inline version of this. One loop
// handles any interleaving: parse, take the next positional, parse again.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}
