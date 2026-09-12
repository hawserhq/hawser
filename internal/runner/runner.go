// Package runner evaluates whether an unattended host — a CI runner, a build
// agent — is set up so the engine comes back after a reboot (#150).
//
// WSL2 cannot start from a Windows service, so an unattended machine needs an
// auto-logon session, a per-user autostart in that session, and the supervisor
// it launches. Each of those is configured by hand (docs/auto-logon-runner.md);
// this package checks the result, read-only and without elevation, so a dead
// runner is diagnosed in one command instead of by guessing.
//
// Privacy: the auto-logon account name is compared, never printed, and the
// password value is probed for existence only — its contents are never read.
package runner

import (
	"strings"
	"time"
)

// Facts is what Evaluate reads, gathered by the caller (registry, autostart,
// supervisor, engine). Pure input, so the rules are unit-tested exhaustively.
type Facts struct {
	// AutoLogonConfigured is Winlogon's AutoAdminLogon=1 with a DefaultUserName.
	AutoLogonConfigured bool
	// AutoLogonUser / AutoLogonDomain identify the auto-logon account. Kept
	// for comparison against the current account; never surfaced in output.
	AutoLogonUser   string
	AutoLogonDomain string
	// PlaintextPassword is true when Winlogon carries a DefaultPassword value —
	// the password stored in clear text, which the playbook says to avoid.
	PlaintextPassword bool
	// CurrentUser / CurrentDomain are the account running the check (and so the
	// one Skrog's per-user state and autostart belong to).
	CurrentUser   string
	CurrentDomain string
	// AutostartRegistered is the per-user Run entry that launches the supervisor.
	AutostartRegistered bool
	// SupervisorRunning is whether the single-instance lock is held now.
	SupervisorRunning bool
	// Engine is running | idle | stopped | not-installed.
	Engine string

	// Power describes the machine's sleep behaviour on mains power. A runner
	// that suspends mid-job fails it in a way that looks like a Skrog fault,
	// and `runner check` is the command an operator runs to satisfy themselves
	// the host is set up — so its silence on this read as approval (#268).
	Power PowerFacts
}

// PowerFacts is the sleep-related state, read from the active power scheme.
//
// Only the AC (plugged-in) values are checked. A runner on battery has bigger
// problems than Skrog can advise on, and a laptop's DC timeouts being short is
// correct behaviour rather than a misconfiguration.
type PowerFacts struct {
	// Known is false when the settings could not be read at all — a probe that
	// did not happen must never read as a probe that passed.
	Known bool
	// Reason explains an unknown reading, for the summary line.
	Reason string
	// StandbyAfter and HibernateAfter are the AC idle timeouts. Zero means
	// never, which is what a runner wants.
	StandbyAfter   time.Duration
	HibernateAfter time.Duration
	// OnBattery is true when the machine is not on mains power. Not a failure,
	// but it changes which timeouts actually apply.
	OnBattery bool
}

// Status is a finding's verdict.
type Status string

const (
	OK   Status = "ok"
	Warn Status = "warn"
	Fail Status = "fail"
)

// Finding is one checked aspect of the runner setup.
type Finding struct {
	Name    string `json:"name"`
	Status  Status `json:"status"`
	Summary string `json:"summary"`
	Remedy  string `json:"remedy,omitempty"`
}

const playbook = "docs/auto-logon-runner.md"

// Evaluate applies the runner rules. Order tells the story a dead runner needs:
// does the machine log in, as the right account, safely; does that session start
// the supervisor; is it running; is the engine there.
func Evaluate(f Facts) []Finding {
	var out []Finding
	add := func(name string, st Status, summary, remedy string) {
		out = append(out, Finding{Name: name, Status: st, Summary: summary, Remedy: remedy})
	}

	if f.AutoLogonConfigured {
		add("autologon", OK, "auto-logon is configured", "")
		if f.CurrentUser != "" && f.AutoLogonUser != "" && !sameAccount(f) {
			add("autologon-account", Fail,
				"auto-logon signs in a different account than the one running this check",
				"Skrog's autostart and state are per-user: install and `skrog autostart enable` "+
					"as the auto-logon account, or point auto-logon at this account ("+playbook+" §3).")
		} else if f.CurrentUser != "" && f.AutoLogonUser != "" {
			add("autologon-account", OK, "auto-logon uses this account", "")
		}
		if f.PlaintextPassword {
			add("autologon-password", Warn,
				"the auto-logon password is stored in clear text in the registry (Winlogon\\DefaultPassword)",
				"use Sysinternals Autologon, which stores it as an LSA secret, then delete the "+
					"DefaultPassword registry value ("+playbook+" §3).")
		} else {
			add("autologon-password", OK, "no clear-text auto-logon password in the registry", "")
		}
	} else {
		add("autologon", Fail,
			"auto-logon is not configured; after a reboot nobody is logged in and the engine cannot start",
			"WSL2 needs an interactive session: set up auto-logon for the runner account with "+
				"Sysinternals Autologon ("+playbook+" §1–3).")
	}

	if f.AutostartRegistered {
		add("autostart", OK, "logon autostart is registered; the supervisor starts with the session", "")
	} else {
		add("autostart", Fail,
			"no logon autostart; the session will start but the supervisor will not",
			"run `skrog autostart enable` as the auto-logon account (needs skrogw.exe beside skrog.exe).")
	}

	if f.SupervisorRunning {
		add("supervisor", OK, "supervisor is running", "")
	} else {
		add("supervisor", Fail, "supervisor is not running",
			"run `skrog start`, or sign out and back in so the autostart launches it.")
	}

	// Warn, never fail. Plenty of runners are desktops that will never sleep,
	// and a check that fails on a healthy host is a check people learn to
	// ignore — which would cost more than this finding is worth.
	//
	// A caller that supplied nothing at all gets no finding, rather than a
	// fabricated "unknown": ReadPower never returns the zero value (it sets
	// either Known or Reason), so the empty case means "this caller did not
	// probe", which is not the same claim as "the probe failed". Both callers
	// do probe, so in practice the finding is always present.
	switch {
	case f.Power == (PowerFacts{}):
		// Not probed by this caller.
	case !f.Power.Known:
		add("power", Warn,
			"could not read the power settings, so whether this machine sleeps is unknown: "+f.Power.Reason,
			"check it by hand with `powercfg /q`, or set the timeouts as below.")
	case f.Power.StandbyAfter == 0 && f.Power.HibernateAfter == 0:
		summary := "the machine does not sleep or hibernate on mains power"
		if f.Power.OnBattery {
			summary += " (currently on battery, where its own timeouts apply)"
		}
		add("power", OK, summary, "")
	default:
		add("power", Warn,
			"the machine sleeps on mains power ("+sleepSummary(f.Power)+"), which suspends a job mid-run",
			"powercfg /change standby-timeout-ac 0 && powercfg /change hibernate-timeout-ac 0")
	}

	switch f.Engine {
	case "running":
		add("engine", OK, "engine is running", "")
	case "idle":
		add("engine", OK, "engine is idle; it wakes on the next docker command", "")
	case "stopped":
		add("engine", Fail, "engine is stopped", "run `skrog start`.")
	default:
		add("engine", Fail, "no engine installed", "run `skrog install --headless` as the runner account.")
	}
	return out
}

// Ready is true when nothing failed; warnings do not block a runner.
func Ready(findings []Finding) bool {
	for _, f := range findings {
		if f.Status == Fail {
			return false
		}
	}
	return true
}

// sameAccount compares the auto-logon account with the current one. Usernames
// are compared case-insensitively; domains only when both are known and
// neither is "." (Winlogon's spelling for the local machine).
func sameAccount(f Facts) bool {
	if !strings.EqualFold(f.AutoLogonUser, f.CurrentUser) {
		return false
	}
	ad, cd := strings.TrimSpace(f.AutoLogonDomain), strings.TrimSpace(f.CurrentDomain)
	if ad == "" || cd == "" || ad == "." || cd == "." {
		return true
	}
	return strings.EqualFold(ad, cd)
}

// sleepSummary names the timeouts that are actually set, so the warning says
// which one to change rather than making the operator look both up.
func sleepSummary(p PowerFacts) string {
	var parts []string
	if p.StandbyAfter > 0 {
		parts = append(parts, "sleeps after "+p.StandbyAfter.String())
	}
	if p.HibernateAfter > 0 {
		parts = append(parts, "hibernates after "+p.HibernateAfter.String())
	}
	return strings.Join(parts, ", ")
}
