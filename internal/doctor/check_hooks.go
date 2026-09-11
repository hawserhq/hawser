package doctor

import (
	"fmt"
	"strings"
)

// checkHooks reports third-party DLLs injected into Skrog's own process (#166).
//
// Endpoint-security agents load a DLL into every process and rewrite function
// prologues to route through their trampolines. Those trampolines assume a C
// thread stack; Go's goroutine stacks are small and movable with their own
// calling convention, and a hook that restores the wrong frame corrupts a Go
// stack. What the user sees is the supervisor dying with a Go runtime fatal
// error -- "unexpected return pc", "found pointer to free object", an access
// violation at an image-base-shaped address -- and nothing in the dump points
// at the cause.
//
// So this check exists to hand over the one fact the dump cannot: something
// else is inside this process. It is a Warn, never a Fail: these agents are
// mandatory on most managed machines, Skrog works with them the overwhelming
// majority of the time, and a module being loaded is not proof it broke
// anything.
func checkHooks() Check {
	c := Check{Name: "process-hooks", Title: "injected modules"}
	c.Run = func(f Facts) Result {
		if len(f.InjectedModules) == 0 {
			return result(c, OK, "no third-party modules injected into this process")
		}

		names := make([]string, 0, len(f.InjectedModules))
		detail := make([]string, 0, len(f.InjectedModules))
		for _, m := range f.InjectedModules {
			names = append(names, m.Name)
			detail = append(detail, "  "+m.Name+"  ("+m.Path+")")
		}

		r := result(c, Warn, fmt.Sprintf("%d third-party module(s) are loaded into this process (%s)",
			len(f.InjectedModules), strings.Join(names, ", ")))
		r.Detail = append(detail,
			"",
			"These are usually endpoint-security agents, and they usually cause no trouble.",
			"They are listed because they are the known cause of one hard-to-diagnose failure:",
			"the supervisor exiting with a Go runtime fatal error (a corrupted stack), which",
			"takes the docker pipe with it until it restarts.")
		r.Remedy = "nothing to do unless the supervisor is dying unexpectedly. If it is:\n" +
			"      1. `skrog logs --supervisor` and supervisor-stderr.log in the state dir — a\n" +
			"         \"fatal error: ...\" with no Skrog frame at the top is the signature.\n" +
			"      2. skrogw.exe already restarts it (about a second of downtime); check\n" +
			"         watchdog.log to see how often that is happening.\n" +
			"      3. ask whoever manages the agent for an exclusion for skrog.exe and\n" +
			"         skrogw.exe — that is the actual fix, and it is a policy change, not a\n" +
			"         code change.\n" +
			"      4. SKROG_NO_VSOCK=1 narrows the window at the cost of the slower transport.\n" +
			"      See https://github.com/wslkit/skrog/issues/166."
		return r
	}
	return c
}
