package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/zcsizmadia/hawser/internal/config"
	"github.com/zcsizmadia/hawser/internal/engineconfig"
	"github.com/zcsizmadia/hawser/internal/hawserfile"
	"github.com/zcsizmadia/hawser/internal/profile"
	"github.com/zcsizmadia/hawser/internal/provision"
)

func runProfile(args []string) int {
	fs := flag.NewFlagSet("profile", flag.ContinueOnError)
	var (
		stateDir = fs.String("state-dir", "", "override Hawser's state directory")
		asJSON   = fs.Bool("json", false, "emit machine-readable JSON (list, show)")
	)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `usage: hawser profile                    list profiles (* = active)
       hawser profile create <name>      save the current settings as a profile
       hawser profile switch <name>      apply a profile's settings
       hawser profile show <name>        print a profile
       hawser profile delete <name>      remove a profile

A profile is a named set of the settings that change between networks — engine
registry mirrors, DNS, logging, the idle timeout, and lifecycle hooks (the
config keys `+"`hawser config`"+` manages). Switch profiles when you move between the
corporate VPN and home instead of hand-toggling each one; `+"`hawser status`"+` names
the active profile.

Switching applies the profile exactly: settings the profile does not set are
cleared, so the engine config always matches the named profile.

Exit codes: 0 ok, %d error, %d usage, %d no such profile / not installed.
`, exitError, exitUsage, exitNotFound)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	opts := optsWithResolvedStateDir(provision.Options{StateDir: *stateDir})
	m := &profile.Manager{StateDir: opts.StateDir}
	rest := fs.Args()

	switch {
	case len(rest) == 0 || (rest[0] == "list" && len(rest) == 1):
		return listProfiles(m, *asJSON)
	case rest[0] == "create" && len(rest) == 2:
		return createProfile(m, opts, rest[1])
	case rest[0] == "switch" && len(rest) == 2:
		return switchProfile(m, opts, rest[1])
	case rest[0] == "show" && len(rest) == 2:
		return showProfile(m, rest[1], *asJSON)
	case rest[0] == "delete" && len(rest) == 2:
		return deleteProfile(m, rest[1])
	default:
		fs.Usage()
		return exitUsage
	}
}

func listProfiles(m *profile.Manager, asJSON bool) int {
	names, err := m.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
		return exitError
	}
	active := m.Active()
	if asJSON {
		out := profileListJSON{Active: active, Profiles: []profileEntryJSON{}}
		for _, n := range names {
			out.Profiles = append(out.Profiles, profileEntryJSON{Name: n, Active: n == active})
		}
		return emitJSON(out)
	}
	if len(names) == 0 {
		fmt.Println("no profiles; `hawser profile create <name>` saves the current settings as one")
		return exitOK
	}
	for _, n := range names {
		marker := "  "
		if n == active {
			marker = "* "
		}
		fmt.Printf("%s%s\n", marker, n)
	}
	return exitOK
}

func createProfile(m *profile.Manager, opts provision.Options, name string) int {
	if err := profile.ValidName(name); err != nil {
		fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
		return exitUsage
	}
	f := snapshotSettings(opts)
	if err := m.Save(name, f); err != nil {
		fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
		return exitError
	}
	fmt.Printf("saved profile %q from the current settings\n", name)
	return exitOK
}

func switchProfile(m *profile.Manager, opts provision.Options, name string) int {
	f, err := m.Load(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
		return exitNotFound
	}
	if err := applyProfile(context.Background(), f, opts); err != nil {
		fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
		return exitError
	}
	if err := m.SetActive(name); err != nil {
		fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
		return exitError
	}
	fmt.Printf("switched to profile %q\n", name)
	return exitOK
}

func showProfile(m *profile.Manager, name string, asJSON bool) int {
	f, err := m.Load(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
		return exitNotFound
	}
	if asJSON {
		// Same document as the YAML: hawserfile.File carries matching json tags.
		return emitJSON(f)
	}
	b, err := f.Marshal()
	if err != nil {
		fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
		return exitError
	}
	os.Stdout.Write(b)
	return exitOK
}

func deleteProfile(m *profile.Manager, name string) int {
	if err := m.Delete(name); err != nil {
		fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
		return exitNotFound
	}
	fmt.Printf("deleted profile %q\n", name)
	return exitOK
}

// snapshotSettings captures the current settable settings into a profile file:
// the idle timeout, lifecycle hooks, and engine daemon.json keys. Engine keys
// need an installed engine; the rest work regardless.
func snapshotSettings(opts provision.Options) hawserfile.File {
	f := hawserfile.File{}
	if v, err := config.Get(opts.StateDir, config.KeyIdleTimeout); err == nil {
		f.IdleTimeout = v
	}
	hooks := map[string]string{}
	for _, k := range config.HookKeys() {
		if v, err := config.Get(opts.StateDir, k); err == nil && v != "" {
			hooks[trimHookPrefix(k)] = v
		}
	}
	if len(hooks) > 0 {
		f.Hooks = hooks
	}
	if em, ok := engineManager(opts); ok {
		if eng, err := em.List(context.Background()); err == nil {
			set := map[string]string{}
			for full, v := range eng {
				if v != "" {
					set[engineconfig.StripPrefix(full)] = v
				}
			}
			if len(set) > 0 {
				f.Engine = set
			}
		}
	}
	return f
}

// applyProfile makes the current settings match the profile exactly: the idle
// timeout and every hook and engine key are set to the profile's value, and the
// ones the profile does not set are cleared. That is what makes a profile a
// network's config rather than an overlay on whatever was there before.
func applyProfile(ctx context.Context, f hawserfile.File, opts provision.Options) error {
	sd := opts.StateDir

	idle := f.IdleTimeout
	if idle == "" {
		idle = "off"
	}
	if err := config.Set(sd, config.KeyIdleTimeout, idle); err != nil {
		return fmt.Errorf("idle-timeout: %w", err)
	}

	for _, key := range config.HookKeys() {
		if err := config.Set(sd, key, f.Hooks[trimHookPrefix(key)]); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}

	// Engine keys are replaced as one batch (one engine bounce), and only when
	// something actually differs — switching between two profiles with the same
	// engine config must not needlessly restart the engine.
	em, ok := engineManager(opts)
	if !ok {
		if len(f.Engine) > 0 {
			return fmt.Errorf("profile sets engine keys but no engine is installed")
		}
		return nil
	}
	current, err := em.List(ctx)
	if err != nil {
		return fmt.Errorf("reading engine config: %w", err)
	}
	desired := map[string]string{}
	changed := false
	for _, key := range engineconfig.Keys() {
		want := f.Engine[key]
		desired[key] = want
		if current[engineconfig.Prefix+key] != want {
			changed = true
		}
	}
	if changed {
		if _, err := em.SetMany(ctx, desired); err != nil {
			return fmt.Errorf("engine settings: %w", err)
		}
	}
	return nil
}
