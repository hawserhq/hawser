// Spike B probe: answers whether WSL2 and dockerd can run from a Windows
// service with nobody logged in (issue #3).
//
// Runs either as a console program (for a baseline in the interactive session)
// or as a Windows service, and appends JSON lines to a log both SYSTEM and the
// interactive user can read. The heartbeat is the point: it records socket
// health every few seconds, so after logging off and back on the log shows
// what happened during the window when no user was present — which is the one
// thing you cannot observe live.
//
// Throwaway spike code. Not product quality, deliberately dependency-light.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

const (
	serviceName = "SkrogSpikeB"
	logDir      = `C:\ProgramData\skrog-spike-b`
)

// distro is the WSL distribution to probe, from SKROG_SPIKE_DISTRO.
func distro() string {
	if d := os.Getenv("SKROG_SPIKE_DISTRO"); d != "" {
		return d
	}
	return "skrog-spike-b"
}

type event struct {
	Time    string `json:"time"`
	Phase   string `json:"phase"`
	Mode    string `json:"mode"`
	User    string `json:"user,omitempty"`
	Session uint32 `json:"session,omitempty"`
	Detail  string `json:"detail,omitempty"`
	Output  string `json:"output,omitempty"`
	Err     string `json:"error,omitempty"`
	OK      *bool  `json:"ok,omitempty"`
}

var mode = "console"

func logEvent(e event) {
	e.Time = time.Now().Format(time.RFC3339)
	e.Mode = mode
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(logDir, "probe.log"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	b, _ := json.Marshal(e)
	f.Write(append(b, '\n'))
}

func boolp(b bool) *bool { return &b }

// identity records who we are and which session we are in. Session 0 is the
// non-interactive services session — seeing it here is the whole point.
func identity() (string, uint32) {
	var user string
	if t, err := windows.OpenCurrentProcessToken(); err == nil {
		defer t.Close()
		if u, err := t.GetTokenUser(); err == nil {
			if account, domain, _, err := u.User.Sid.LookupAccount(""); err == nil {
				user = domain + `\` + account
			} else {
				user = u.User.Sid.String()
			}
		}
	}
	// Session 0 is the services session; an interactive login is 1 or higher.
	var session uint32
	windows.ProcessIdToSessionId(uint32(os.Getpid()), &session)
	return user, session
}

// run executes a command and returns combined output, trimmed for the log.
func run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	s := strings.TrimSpace(string(out))
	// wsl.exe emits UTF-16; strip NULs so the log stays readable.
	s = strings.ReplaceAll(s, "\x00", "")
	if len(s) > 4000 {
		s = s[:4000] + "...(truncated)"
	}
	return s, err
}

// probe runs the questions the spike must answer, in order of increasing
// dependence on the previous one succeeding.
func probe() {
	user, session := identity()
	logEvent(event{Phase: "identity", User: user, Session: session,
		Detail: fmt.Sprintf("session %d (0 = non-interactive services session)", session)})

	// 1. Does wsl.exe run at all in this context?
	out, err := run("wsl.exe", "--status")
	logEvent(event{Phase: "wsl-status", Output: out, Err: errString(err), OK: boolp(err == nil)})

	// 2. Which distros does THIS account see? Registration lives in HKCU, so a
	// service account may see none even though the interactive user has several.
	// If this comes back empty, `skrog install` must import the distro as the
	// service account rather than as the installing user.
	out, err = run("wsl.exe", "--list", "--verbose")
	logEvent(event{Phase: "wsl-list", Output: out, Err: errString(err), OK: boolp(err == nil)})

	d := distro()
	visible := err == nil && strings.Contains(out, d)
	logEvent(event{Phase: "distro-visible", Detail: d, OK: boolp(visible)})

	// Registration is per-user, so a service account will not see a distro the
	// interactive user imported. Rather than importing it from outside (which
	// needs stored-credential rights the account does not have), the service
	// imports its own — which is also what `skrog install --headless` would
	// have to do in production, so it is the more useful thing to measure.
	var importOut string
	if !visible && os.Getenv("SKROG_SPIKE_ROOTFS") != "" {
		_, importOut = selfImport(d)
		out, err = run("wsl.exe", "--list", "--verbose")
		logEvent(event{Phase: "distro-visible-after-import", Detail: d,
			Output: out, OK: boolp(err == nil && strings.Contains(out, d))})
		visible = err == nil && strings.Contains(out, d)
	}

	if !visible {
		// Distinguish the causes rather than assuming one. The first run of
		// this spike hit WSL_E_LOCAL_SYSTEM_NOT_SUPPORTED under LocalSystem,
		// which is an outright refusal by WSL and nothing to do with per-user
		// registration - reporting it as registration would have recorded the
		// wrong conclusion in the issue.
		logEvent(event{Phase: "conclusion", Detail: diagnose(out, importOut), OK: boolp(false)})
		return
	}

	// 3. Can we start the distro and reach a shell?
	out, err = run("wsl.exe", "-d", d, "-u", "root", "--exec", "echo", "alive")
	logEvent(event{Phase: "distro-exec", Output: out, Err: errString(err), OK: boolp(err == nil)})
	if err != nil {
		return
	}

	// 4. Start dockerd if it is not already running.
	out, _ = run("wsl.exe", "-d", d, "-u", "root", "--exec", "sh", "-c", "pgrep dockerd || echo none")
	if strings.Contains(out, "none") {
		go run("wsl.exe", "-d", d, "-u", "root", "--exec", "sh", "-c",
			"dockerd >/var/log/dockerd.log 2>&1")
		logEvent(event{Phase: "dockerd-start", Detail: "launched"})
	} else {
		logEvent(event{Phase: "dockerd-start", Detail: "already running: " + out})
	}

	// 5. Wait for the socket, recording how long it took.
	start := time.Now()
	for i := 0; i < 60; i++ {
		if _, err := run("wsl.exe", "-d", d, "-u", "root", "--exec",
			"test", "-S", "/var/run/docker.sock"); err == nil {
			logEvent(event{Phase: "socket-up",
				Detail: fmt.Sprintf("after %s", time.Since(start).Round(time.Millisecond)),
				OK:     boolp(true)})
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	out, _ = run("wsl.exe", "-d", d, "-u", "root", "--exec", "tail", "-30", "/var/log/dockerd.log")
	logEvent(event{Phase: "socket-up", Output: out, OK: boolp(false)})
}

// diagnose names the actual cause of a failure instead of guessing one.
//
// Written as a table because the spike has already produced three different
// causes that all surface as "the distro is not visible", and recording the
// wrong one in the issue is worse than recording nothing. Each entry is a
// signature this spike has genuinely observed.
func diagnose(listOut, importOut string) string {
	all := listOut + "\n" + importOut
	signatures := []struct {
		match  []string
		detail string
	}{
		{
			match: []string{"WSL_E_LOCAL_SYSTEM_NOT_SUPPORTED", "as local system is not supported"},
			detail: "WSL refuses to run as LocalSystem (WSL_E_LOCAL_SYSTEM_NOT_SUPPORTED). " +
				"That account can never work, regardless of registration. A dedicated " +
				"service account is the only viable pattern.",
		},
		{
			match: []string{"0x80070569", "not been granted the requested logon type"},
			detail: "creating the WSL utility VM failed with ERROR_LOGON_TYPE_NOT_GRANTED " +
				"(HCS/0x80070569). WSL itself runs under this account, so the blocker is a " +
				"missing logon right, not the architecture: the Host Compute Service builds " +
				"the VM against a user token needing batch (and possibly interactive) logon " +
				"rights. Whichever are required become documented v0.2 installer steps.",
		},
		{
			match: []string{"0x80070005", "Access is denied"},
			detail: "access denied while registering the distro - check that the account can " +
				"write the VHDX directory and read the rootfs.",
		},
		{
			match: []string{"no installed distributions"},
			detail: "this account has no distros registered, confirming per-user registration " +
				"(HKCU\\...\\Lxss). The import must run as the service account, which is what " +
				"self-import tests.",
		},
	}
	for _, s := range signatures {
		for _, m := range s.match {
			if strings.Contains(all, m) {
				return s.detail
			}
		}
	}
	return "target distro not visible to this account, cause not recognized - " +
		"read the wsl-list and self-import-result output above"
}

// selfImport registers the distro under the account this process runs as.
//
// This is the decisive measurement of the spike: whether a non-interactive
// service account in session 0 can provision a WSL distro at all. If it can,
// the dedicated-account pattern works and `skrog install --headless` knows
// what it has to do; if it cannot, session-0 operation is off the table.
func selfImport(d string) (bool, string) {
	rootfs := os.Getenv("SKROG_SPIKE_ROOTFS")
	vhd := os.Getenv("SKROG_SPIKE_VHD")
	if vhd == "" {
		vhd = filepath.Join(logDir, d)
	}
	if err := os.MkdirAll(vhd, 0o755); err != nil {
		logEvent(event{Phase: "self-import", Err: err.Error(), OK: boolp(false),
			Detail: "could not create the VHDX directory"})
		return false, err.Error()
	}

	logEvent(event{Phase: "self-import", Detail: "importing " + d + " from " + rootfs})
	out, err := run("wsl.exe", "--import", d, vhd, rootfs, "--version", "2")
	logEvent(event{Phase: "self-import-result", Output: out, Err: errString(err),
		OK: boolp(err == nil)})
	return err == nil, out + " " + errString(err)
}

// heartbeat records socket health until told to stop. Reading these lines after
// logging back in is how the no-login window gets evaluated.
func heartbeat(stop <-chan struct{}) {
	d := distro()
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			logEvent(event{Phase: "heartbeat-stop"})
			return
		case <-t.C:
			_, err := run("wsl.exe", "-d", d, "-u", "root", "--exec",
				"test", "-S", "/var/run/docker.sock")
			logEvent(event{Phase: "heartbeat", OK: boolp(err == nil), Err: errString(err)})
		}
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type spikeService struct{}

func (spikeService) Execute(args []string, r <-chan svc.ChangeRequest, s chan<- svc.Status) (bool, uint32) {
	s <- svc.Status{State: svc.StartPending}
	stop := make(chan struct{})

	logEvent(event{Phase: "service-start"})
	go func() {
		probe()
		heartbeat(stop)
	}()

	s <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for c := range r {
		switch c.Cmd {
		case svc.Interrogate:
			s <- c.CurrentStatus
		case svc.Stop, svc.Shutdown:
			logEvent(event{Phase: "service-stop", Detail: fmt.Sprintf("cmd %d", c.Cmd)})
			close(stop)
			s <- svc.Status{State: svc.StopPending}
			return false, 0
		}
	}
	return false, 0
}

func main() {
	isService, err := svc.IsWindowsService()
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot determine service context:", err)
		os.Exit(1)
	}

	if isService {
		mode = "service"
		if err := svc.Run(serviceName, spikeService{}); err != nil {
			logEvent(event{Phase: "service-error", Err: err.Error()})
			os.Exit(1)
		}
		return
	}

	// Console mode: the interactive baseline to compare the service run against.
	mode = "console"
	fmt.Println("running probe in console mode; results ->", filepath.Join(logDir, "probe.log"))
	probe()
	fmt.Println("done")
}
