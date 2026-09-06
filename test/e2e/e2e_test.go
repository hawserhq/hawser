//go:build e2e

// Package e2e is the v0.1 acceptance suite (#11).
//
// It drives the real hawser.exe against the real published rootfs on a real
// Windows machine with WSL2 — the class of testing that found all ten of the
// defects the unit tests could not (see #38 for the tally). It deliberately
// shells out to the same binaries a user runs rather than importing internal
// packages: the product is the CLI contract, so that is what gets tested.
//
// Run on any Windows machine with WSL2 and a docker CLI:
//
//	cd test/e2e && go test -tags e2e -timeout 30m -v .
//
// The suite installs into an isolated distro and state dir, and removes both;
// a failure can leave the distro behind, in which case
// `hawser uninstall --state-dir <printed dir> --yes` cleans up.
package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	distro   = "hawser-e2e-suite"
	pipeName = `\\.\pipe\hawser-e2e-suite`
	// dockerHost matches pipeName in the form the docker CLI wants.
	dockerHost = "npipe:////./pipe/hawser-e2e-suite"
)

// state carries everything the ordered stages share.
type state struct {
	workDir  string // parent-scoped scratch: subtest TempDirs die with the subtest
	hawser   string // built hawser.exe
	docker   string // resolved docker CLI
	stateDir string
	dataDir  string
	proxy    *exec.Cmd
	proxyLog *os.File

	// baselines captured before anything runs, compared after teardown.
	wslProcsBefore int
	ddWorkedBefore bool
}

// TestAcceptance is one ordered scenario, not independent tests: install must
// precede use, use must precede uninstall, and the teardown assertions are
// meaningless unless the earlier stages actually ran. Sub-tests give per-stage
// reporting while a stage failure stops the sequence.
func TestAcceptance(t *testing.T) {
	if os.Getenv("OS") != "Windows_NT" {
		t.Skip("acceptance runs on Windows with WSL2")
	}
	s := &state{workDir: t.TempDir()}

	// Resolved up front so a missing CLI skips the suite rather than failing a
	// later stage: a skip inside a sub-stage does not stop the sequence.
	s.docker = findDocker()
	if s.docker == "" {
		t.Skip("no docker CLI found on PATH or in the usual install locations")
	}

	stages := []struct {
		name string
		fn   func(t *testing.T, s *state)
	}{
		{"Baselines", stageBaselines},
		{"BuildHawser", stageBuild},
		{"InstallFromPublishedRelease", stageInstall},
		{"StartProxy", stageProxy},
		{"DoctorReportsHealthy", stageDoctor},
		{"HelloWorld", stageHelloWorld},
		{"BindMountReadThroughContainer", stageBindMount},
		{"ExecInRunningContainer", stageExec},
		{"StdinPipeIntoContainer", stageStdinPipe},
		{"LogsFollowStreams", stageLogsFollow},
		{"ComposeStack", stageCompose},
		{"WSLIntegrateSharesEngine", stageWSLIntegrate},
		{"MigrateFromDesktop", stageMigrate},
		{"IdleStopAndOnDemandWake", stageIdle},
		{"InterruptedClientDoesNotWedgeBridge", stageInterrupt},
		{"VsockPathServedEverything", stageVsockServed},
		{"Uninstall", stageUninstall},
		{"NothingLeftBehind", stageClean},
		{"DockerDesktopStillWorks", stageDesktopIntact},
	}

	// Teardown runs even on failure, so a broken run does not strand a distro.
	t.Cleanup(func() { forceCleanup(s) })

	for _, st := range stages {
		if !t.Run(st.name, func(t *testing.T) { st.fn(t, s) }) {
			t.Fatalf("stage %s failed; skipping the rest", st.name)
		}
	}
}

// run executes a command and returns trimmed combined output.
func run(t *testing.T, timeout time.Duration, name string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(name, args...)
	done := make(chan struct{})
	var out []byte
	var err error
	go func() {
		out, err = cmd.CombinedOutput()
		close(done)
	}()
	select {
	case <-done:
		return strings.TrimSpace(string(out)), err
	case <-time.After(timeout):
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		<-done
		return strings.TrimSpace(string(out)), fmt.Errorf("%s timed out after %s", name, timeout)
	}
}

// runEnv is run() with an explicit environment, for subprocesses that shell
// out to docker and thus need docker's credential helper on PATH (hostEnv adds
// docker's own directory). A real `hawser migrate` inherits a shell where
// Docker Desktop is on PATH; the suite must reproduce that for its subprocess.
func runEnv(t *testing.T, env []string, timeout time.Duration, name string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = env
	done := make(chan struct{})
	var out []byte
	var err error
	go func() { out, err = cmd.CombinedOutput(); close(done) }()
	select {
	case <-done:
		return strings.TrimSpace(string(out)), err
	case <-time.After(timeout):
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		<-done
		return strings.TrimSpace(string(out)), fmt.Errorf("%s timed out after %s", name, timeout)
	}
}

func must(t *testing.T, out string, err error, what string) string {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v\n%s", what, err, out)
	}
	return out
}

// dockerE runs docker against the suite's engine, never the user's default.
func dockerE(t *testing.T, s *state, timeout time.Duration, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(s.docker, args...)
	cmd.Env = s.dockerEnv()
	done := make(chan struct{})
	var out []byte
	var err error
	go func() {
		out, err = cmd.CombinedOutput()
		close(done)
	}()
	select {
	case <-done:
		return strings.TrimSpace(string(out)), err
	case <-time.After(timeout):
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		<-done
		return strings.TrimSpace(string(out)), fmt.Errorf("docker %s timed out after %s",
			strings.Join(args, " "), timeout)
	}
}

func wslProcCount(t *testing.T) int {
	t.Helper()
	out, _ := run(t, 30*time.Second, "tasklist", "/FI", "IMAGENAME eq wsl.exe", "/FO", "CSV", "/NH")
	if strings.Contains(out, "No tasks") {
		return 0
	}
	return len(strings.Split(out, "\n"))
}

func stageBaselines(t *testing.T, s *state) {
	if out, err := run(t, 30*time.Second, s.docker, "--version"); err != nil {
		t.Skipf("no docker CLI on PATH (%v: %s); the suite drives the engine through it", err, out)
	}
	s.wslProcsBefore = wslProcCount(t)
	t.Logf("wsl.exe baseline: %d", s.wslProcsBefore)

	// Whether Docker Desktop worked BEFORE decides whether the intact-after
	// stage can claim anything. Recorded, not required.
	if out, err := s.runDocker(t, 60*time.Second, "--context", "desktop-linux", "version", "--format", "{{.Server.Version}}"); err == nil && out != "" {
		s.ddWorkedBefore = true
		t.Logf("Docker Desktop serving version %s", out)
	} else {
		t.Log("Docker Desktop not responding before the suite; the intact-check will be skipped")
	}
}

func stageBuild(t *testing.T, s *state) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	s.hawser = filepath.Join(s.workDir, "hawser.exe")
	cmd := exec.Command("go", "build", "-o", s.hawser, "./cmd/hawser")
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building hawser.exe: %v\n%s", err, out)
	}
}

func stageInstall(t *testing.T, s *state) {
	s.stateDir = filepath.Join(s.workDir, "state")
	s.dataDir = filepath.Join(s.workDir, "data")

	// No rootfs flags: this installs what a user installs, verifying the
	// embedded manifest against the published release asset.
	out, err := run(t, 10*time.Minute, s.hawser, "install",
		"--distro", distro, "--state-dir", s.stateDir, "--data-dir", s.dataDir, "--headless")
	must(t, out, err, "hawser install")

	if !strings.Contains(out, "installed and running") {
		t.Errorf("install output does not report success:\n%s", out)
	}
}

func stageProxy(t *testing.T, s *state) {
	logPath := filepath.Join(s.stateDir, "proxy-e2e.log")
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	s.proxyLog = f

	// The bridge is `hawser supervise`, not `hawser proxy`: it is what real
	// installs run (autostart launches it), and it is where the idle/on-demand
	// behavior the IdleStop stage exercises lives. The suite's own pipe and
	// state dir keep a Hawser or Docker Desktop already on the machine
	// undisturbed — the state dir also scopes the supervisor's single-instance
	// mutex, so a real supervisor can coexist with the suite's.
	s.proxy = exec.Command(s.hawser, "supervise",
		"--distro", distro, "--state-dir", s.stateDir,
		"--pipe", pipeName, "--no-context")
	s.proxy.Stdout = f
	s.proxy.Stderr = f
	if err := s.proxy.Start(); err != nil {
		t.Fatalf("starting supervisor: %v", err)
	}

	// Up when the engine answers, not when the process exists.
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		if out, err := dockerE(t, s, 15*time.Second, "version", "--format", "{{.Server.Version}}"); err == nil && out != "" {
			t.Logf("engine answering: server %s", out)
			return
		}
		time.Sleep(2 * time.Second)
	}
	log, _ := os.ReadFile(logPath)
	t.Fatalf("engine never answered through the pipe. Proxy log:\n%s", log)
}

// stageDoctor runs `hawser doctor --json` against the live install and asserts
// the checks that describe a healthy engine all pass. It does not assert the
// overall exit code: host-specific checks (which docker.exe is on PATH, whether
// a credential helper resolves, the machine's default docker context) depend on
// the developer's or CI runner's environment, not on Hawser, so pinning them
// would make the suite flaky. The engine-shaped checks are what doctor owns.
func stageDoctor(t *testing.T, s *state) {
	// doctor exits non-zero when any check fails (e.g. no docker.exe on the CI
	// runner's PATH), but still writes the full JSON report to stdout, so parse
	// the output regardless of the exit error.
	out, _ := run(t, 60*time.Second, s.hawser, "doctor", "--json", "--state-dir", s.stateDir)
	var rep struct {
		Results []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("doctor --json unparseable: %v\n%s", err, out)
	}

	status := map[string]string{}
	for _, r := range rep.Results {
		status[r.Name] = r.Status
	}

	// With the supervisor up and the engine answering, these must be ok.
	for _, name := range []string{"wsl", "engine", "supervisor"} {
		if status[name] != "ok" {
			t.Errorf("doctor check %q = %q, want ok\nfull report:\n%s", name, status[name], out)
		}
	}
	// Disk should never fail on a machine that just installed the engine.
	if status["disk"] == "fail" {
		t.Errorf("doctor disk check failed unexpectedly\nfull report:\n%s", out)
	}
}

func stageHelloWorld(t *testing.T, s *state) {
	out, err := dockerE(t, s, 5*time.Minute, "run", "--rm", "hello-world")
	must(t, out, err, "docker run hello-world")
	if !strings.Contains(out, "Hello from Docker!") {
		t.Errorf("unexpected hello-world output:\n%s", out)
	}
}

func stageBindMount(t *testing.T, s *state) {
	// The #7 chain end to end: a Windows path, rewritten by the proxy,
	// automounted by WSL, read by a container. Every link has failed at least
	// once in isolation; this is the only test that exercises them together.
	dir := t.TempDir()
	const proof = "bind-mount-proof-e2e"
	if err := os.WriteFile(filepath.Join(dir, "proof.txt"), []byte(proof), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := dockerE(t, s, 3*time.Minute, "run", "--rm",
		"-v", dir+":/data:ro", "alpine:latest", "cat", "/data/proof.txt")
	must(t, out, err, "bind mount read")
	if !strings.Contains(out, proof) {
		t.Errorf("container read %q, want %q — path translation or automount broke", out, proof)
	}
}

func stageExec(t *testing.T, s *state) {
	if out, err := dockerE(t, s, 2*time.Minute, "run", "-d", "--name", "e2e-exec",
		"alpine:latest", "sleep", "300"); err != nil {
		t.Fatalf("starting container: %v\n%s", err, out)
	}
	defer dockerE(t, s, time.Minute, "rm", "-f", "e2e-exec")

	out, err := dockerE(t, s, time.Minute, "exec", "e2e-exec", "sh", "-c", "echo exec-$(hostname)")
	must(t, out, err, "docker exec")
	if !strings.HasPrefix(out, "exec-") {
		t.Errorf("exec output = %q", out)
	}
	// A real interactive TTY (exec -it, Ctrl-C on logs -f) cannot be driven
	// from a test binary without a ConPTY harness; that stays the documented
	// manual check from #11.
}

func stageStdinPipe(t *testing.T, s *state) {
	// #57: piping into an interactive container must return, not hang. The CLI
	// signals stdin-EOF by half-closing its named-pipe connection, which only
	// works because the bridge serves the pipe in message mode; a byte-mode
	// pipe swallows the CloseWrite and the container blocks on stdin forever.
	// The bounded timeout in dockerStdin turns a regression into a failure
	// rather than a hung suite.
	const payload = "line-one\nline-two\nline-three\n"

	got, err := dockerStdin(t, s, payload, 45*time.Second, "run", "-i", "--rm", "alpine:latest", "cat")
	must(t, got, err, "docker run -i cat (stdin round-trip)")
	if got != strings.TrimRight(payload, "\n") {
		t.Errorf("run -i cat returned %q, want the piped payload", got)
	}

	// exec -i into a running container is the same half-close path.
	if out, err := dockerE(t, s, 2*time.Minute, "run", "-d", "--name", "e2e-stdin",
		"alpine:latest", "sleep", "300"); err != nil {
		t.Fatalf("starting container: %v\n%s", err, out)
	}
	defer dockerE(t, s, time.Minute, "rm", "-f", "e2e-stdin")

	got, err = dockerStdin(t, s, payload, 45*time.Second, "exec", "-i", "e2e-stdin", "wc", "-l")
	must(t, got, err, "docker exec -i wc -l (stdin round-trip)")
	if strings.TrimSpace(got) != "3" {
		t.Errorf("exec -i wc -l counted %q lines, want 3 (stdin EOF did not propagate)", got)
	}
}

// dockerStdin runs docker against the suite's engine with a string on stdin,
// bounded by timeout so a stdin-EOF regression fails instead of hanging.
func dockerStdin(t *testing.T, s *state, stdin string, timeout time.Duration, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(s.docker, args...)
	cmd.Env = s.dockerEnv()
	cmd.Stdin = strings.NewReader(stdin)
	done := make(chan struct{})
	var out []byte
	var err error
	go func() { out, err = cmd.CombinedOutput(); close(done) }()
	select {
	case <-done:
		return strings.TrimSpace(string(out)), err
	case <-time.After(timeout):
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		<-done
		return strings.TrimSpace(string(out)), fmt.Errorf("docker %s timed out after %s (stdin EOF likely not propagating)",
			strings.Join(args, " "), timeout)
	}
}

func stageLogsFollow(t *testing.T, s *state) {
	if out, err := dockerE(t, s, 2*time.Minute, "run", "-d", "--name", "e2e-logs",
		"alpine:latest", "sh", "-c", "i=0; while true; do echo line-$i; i=$((i+1)); sleep 1; done"); err != nil {
		t.Fatalf("starting logger: %v\n%s", err, out)
	}
	defer dockerE(t, s, time.Minute, "rm", "-f", "e2e-logs")

	// Follow for a bounded window; receiving multiple distinct lines proves
	// incremental streaming rather than buffer-until-close.
	cmd := exec.Command(s.docker, "logs", "-f", "e2e-logs")
	cmd.Env = s.dockerEnv()
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf
	outPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()

	buf := make([]byte, 4096)
	collected := ""
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && strings.Count(collected, "line-") < 3 {
		n, err := outPipe.Read(buf)
		if n > 0 {
			collected += string(buf[:n])
		}
		if err != nil {
			break
		}
	}
	if got := strings.Count(collected, "line-"); got < 3 {
		t.Errorf("logs -f streamed %d lines in 20s, want >= 3:\n%s", got, collected)
	}
}

func stageCompose(t *testing.T, s *state) {
	if _, err := run(t, 30*time.Second, s.docker, "compose", "version"); err != nil {
		t.Skip("docker compose plugin not available on this machine")
	}

	dir := t.TempDir()
	// Healthcheck-gated depends_on and a Windows-path bind: the two compose
	// behaviours PLAN §05 names, in the smallest stack that has both.
	compose := `
services:
  db:
    image: alpine:latest
    command: sh -c "touch /tmp/ready && sleep 300"
    healthcheck:
      test: ["CMD", "test", "-f", "/tmp/ready"]
      interval: 2s
      retries: 15
  app:
    image: alpine:latest
    depends_on:
      db:
        condition: service_healthy
    volumes:
      - ./shared:/shared
    command: sh -c "cat /shared/input.txt && echo compose-ran > /shared/output.txt"
`
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "shared"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "shared", "input.txt"), []byte("compose-input"), 0o644); err != nil {
		t.Fatal(err)
	}

	composeCmd := func(timeout time.Duration, args ...string) (string, error) {
		cmd := exec.Command(s.docker, append([]string{"compose"}, args...)...)
		cmd.Dir = dir
		cmd.Env = s.dockerEnv()
		done := make(chan struct{})
		var out []byte
		var err error
		go func() { out, err = cmd.CombinedOutput(); close(done) }()
		select {
		case <-done:
			return strings.TrimSpace(string(out)), err
		case <-time.After(timeout):
			if cmd.Process != nil {
				cmd.Process.Kill()
			}
			<-done
			return strings.TrimSpace(string(out)), fmt.Errorf("compose timed out after %s", timeout)
		}
	}

	defer composeCmd(3*time.Minute, "down", "--timeout", "5")

	out, err := composeCmd(5*time.Minute, "up", "--abort-on-container-exit", "--exit-code-from", "app")
	must(t, out, err, "compose up")

	got, err := os.ReadFile(filepath.Join(dir, "shared", "output.txt"))
	if err != nil {
		t.Fatalf("app never wrote through the bind mount: %v\ncompose output:\n%s", err, out)
	}
	if !strings.Contains(string(got), "compose-ran") {
		t.Errorf("output.txt = %q", got)
	}
}

func stageIdle(t *testing.T, s *state) {
	// The #41 story end to end: configure a short idle timeout, watch the
	// supervisor stop the quiet engine (status says idle, exit 0 — "stopped
	// by design" is not an error), then have one docker command wake it.
	out, err := run(t, 30*time.Second, s.hawser, "config",
		"--state-dir", s.stateDir, "set", "idle-timeout", "15s")
	must(t, out, err, "hawser config set idle-timeout")
	defer func() {
		out, err := run(t, 30*time.Second, s.hawser, "config",
			"--state-dir", s.stateDir, "set", "idle-timeout", "off")
		must(t, out, err, "hawser config set idle-timeout off")
	}()

	statusJSON := func() (engine string, exit int) {
		t.Helper()
		out, err := run(t, 30*time.Second, s.hawser, "status", "--state-dir", s.stateDir, "--json")
		if err != nil {
			return "", 1
		}
		var st struct {
			Engine string `json:"engine"`
		}
		if jerr := json.Unmarshal([]byte(out), &st); jerr != nil {
			t.Fatalf("status --json unparseable: %v\n%s", jerr, out)
		}
		return st.Engine, 0
	}

	// Idle within: 15s quiet + a few 3s ticks + the stop itself. Nothing here
	// touches the engine while polling — a docker call each iteration would
	// itself reset the quiet window ("open client connections") and never let
	// the engine idle; status reads host-side files only.
	deadline := time.Now().Add(90 * time.Second)
	for {
		engine, _ := statusJSON()
		if engine == "idle" {
			break
		}
		if time.Now().After(deadline) {
			// The supervisor logs why each idle stop was deferred, including —
			// when a container vetoes — the names busyProbe saw.
			logBytes, _ := os.ReadFile(filepath.Join(s.stateDir, "proxy-e2e.log"))
			tail := string(logBytes)
			if len(tail) > 4000 {
				tail = tail[len(tail)-4000:]
			}
			ps, _ := dockerE(t, s, 30*time.Second, "ps", "-a", "--format", "{{.Names}} {{.Status}}")
			t.Fatalf("engine did not idle-stop within 90s; status reports %q\ncontainers (ps -a):\n%s\nsupervisor log tail:\n%s", engine, ps, tail)
		}
		time.Sleep(2 * time.Second)
	}
	t.Log("engine idle-stopped; status reports it as such")

	// `hawser status` must treat idle as healthy: exit 0.
	if out, err := run(t, 30*time.Second, s.hawser, "status", "--state-dir", s.stateDir); err != nil {
		t.Errorf("status exited non-zero on an idle engine (stopped by design is not broken):\n%s", out)
	}

	// The whole point of idling is reclaiming the VM's RAM, and #82 found the
	// health probe itself booting the stopped distro on every tick and status
	// poll. Hammer status a few times, give a couple of supervisor ticks a
	// chance to misbehave, then require the distro to still be Stopped.
	for i := 0; i < 3; i++ {
		run(t, 30*time.Second, s.hawser, "status", "--state-dir", s.stateDir, "--json")
	}
	time.Sleep(7 * time.Second) // > two supervisor ticks
	list, _ := run(t, 30*time.Second, "wsl.exe", "--list", "--verbose")
	for _, line := range strings.Split(strings.ReplaceAll(list, "\x00", ""), "\n") {
		if strings.Contains(line, distro) && strings.Contains(line, "Running") {
			t.Errorf("distro %q is running again while idle-stopped — a health probe booted it (#82): %s", distro, strings.TrimSpace(line))
		}
	}

	// One docker command is the wake-up: it pays the cold start and works.
	began := time.Now()
	out, err = dockerE(t, s, 2*time.Minute, "version", "--format", "{{.Server.Version}}")
	must(t, out, err, "docker version against an idle engine")
	t.Logf("cold start: docker version answered %q in %s", out, time.Since(began).Round(100*time.Millisecond))

	if engine, _ := statusJSON(); engine != "running" {
		t.Errorf("engine is %q after the on-demand wake, want running", engine)
	}
}

func stageWSLIntegrate(t *testing.T, s *state) {
	// #42 end to end, against a real second distro: the engine socket is
	// shared VM-wide at /mnt/wsl/<distro>/docker.sock, wsl-integrate wires a
	// target distro's DOCKER_HOST to it, --remove unwires cleanly.
	list, _ := run(t, 30*time.Second, "wsl.exe", "--list", "--quiet")
	if !strings.Contains(strings.ReplaceAll(list, "\x00", ""), "Ubuntu") {
		t.Skip("no Ubuntu distro to integrate against")
	}
	// Never clobber a real integration: if the user's Ubuntu already carries
	// the profile script, this stage's --remove would delete theirs.
	if _, err := run(t, 30*time.Second, "wsl.exe", "-d", "Ubuntu",
		"--", "test", "-f", "/etc/profile.d/hawser.sh"); err == nil {
		t.Skip("Ubuntu already has a hawser integration; not touching it")
	}

	// The engine's socket must be visible from the OTHER distro — that is
	// the whole point of the /mnt/wsl bind.
	sock := "/mnt/wsl/" + distro + "/docker.sock"
	if out, err := run(t, 30*time.Second, "wsl.exe", "-d", "Ubuntu",
		"--", "test", "-S", sock); err != nil {
		t.Fatalf("shared socket %s not visible from Ubuntu: %v\n%s", sock, err, out)
	}
	// And it must actually answer, as the ORDINARY user (not root): the point
	// of the share is docker-without-sudo, which needs the 0666 the
	// provisioner sets. A dead inode also passes test -S, so this is the real
	// check. Skip only when curl is genuinely absent (exit 127), not when it
	// runs and fails.
	if _, err := run(t, 30*time.Second, "wsl.exe", "-d", "Ubuntu", "--", "sh", "-c", "command -v curl"); err != nil {
		t.Log("curl absent in Ubuntu; socket presence checked but not exercised")
	} else {
		out, err := run(t, 30*time.Second, "wsl.exe", "-d", "Ubuntu", "--",
			"curl", "-s", "--max-time", "10", "--unix-socket", sock, "http://localhost/_ping")
		if err != nil || out != "OK" {
			t.Errorf("engine ping from Ubuntu as the ordinary user = %q (%v); want OK — the shared socket must be user-accessible, not root-only", out, err)
		}
	}

	out, err := run(t, time.Minute, s.hawser, "wsl-integrate", "--state-dir", s.stateDir, "Ubuntu")
	must(t, out, err, "hawser wsl-integrate Ubuntu")
	defer run(t, time.Minute, s.hawser, "wsl-integrate", "--state-dir", s.stateDir, "--remove", "Ubuntu")

	if out, err := run(t, 30*time.Second, "wsl.exe", "-d", "Ubuntu",
		"--", "cat", "/etc/profile.d/hawser.sh"); err != nil || !strings.Contains(out, sock) {
		t.Errorf("profile script wrong or missing (%v):\n%s", err, out)
	}

	out, err = run(t, time.Minute, s.hawser, "wsl-integrate", "--state-dir", s.stateDir, "--remove", "Ubuntu")
	must(t, out, err, "hawser wsl-integrate --remove")
	if _, err := run(t, 30*time.Second, "wsl.exe", "-d", "Ubuntu",
		"--", "test", "-f", "/etc/profile.d/hawser.sh"); err == nil {
		t.Error("profile script still present after --remove")
	}
}

func stageMigrate(t *testing.T, s *state) {
	// #43 end to end against real Docker Desktop: seed a distinctive image and
	// a volume with known contents in Desktop, migrate ONLY those into the
	// Hawser engine (--only keeps this from copying the developer's whole
	// Desktop), and prove they arrived intact while Desktop is untouched.
	if !s.ddWorkedBefore {
		t.Skip("Docker Desktop was not serving before the suite; nothing to migrate from")
	}
	const (
		probeImg = "alpine:3.19"
		probeVol = "hawser-e2e-migrate-probe"
		marker   = "hawser-e2e-migrate-marker"
	)
	// Seed Desktop. Cleaned up regardless of outcome.
	if out, err := s.runDocker(t, 3*time.Minute, "--context", "desktop-linux", "pull", probeImg); err != nil {
		t.Skipf("cannot pull %s into Desktop (%v): %s", probeImg, err, out)
	}
	s.runDocker(t, 30*time.Second, "--context", "desktop-linux", "volume", "rm", "-f", probeVol)
	vout, verr := s.runDocker(t, 30*time.Second, "--context", "desktop-linux", "volume", "create", probeVol)
	must(t, vout, verr, "create source volume")
	defer s.runDocker(t, 30*time.Second, "--context", "desktop-linux", "volume", "rm", "-f", probeVol)
	sout, serr := s.runDocker(t, time.Minute, "--context", "desktop-linux", "run", "--rm",
		"-v", probeVol+":/d", probeImg, "sh", "-c", "echo "+marker+" > /d/marker.txt")
	must(t, sout, serr, "seed source volume")

	// Migrate just the probe items into the suite's engine. hostEnv puts
	// docker's dir on PATH so the credential helper resolves for the pull of
	// the tar-helper image — the environment a real migrate already has.
	out, err := runEnv(t, s.hostEnv(), 5*time.Minute, s.hawser, "migrate", "--from-desktop",
		"--only", probeImg, "--only", probeVol, "--state-dir", s.stateDir,
		"--docker", s.docker, "--docker-host", dockerHost)
	must(t, out, err, "hawser migrate")

	// Image arrived.
	if got, err := dockerE(t, s, time.Minute, "images", probeImg, "--format", "{{.Repository}}:{{.Tag}}"); err != nil || got == "" {
		t.Errorf("migrated image not on the Hawser engine: %q (%v)", got, err)
	}
	// Volume arrived with its contents intact — the real proof, not just presence.
	got, err := dockerE(t, s, time.Minute, "run", "--rm", "-v", probeVol+":/d", probeImg, "cat", "/d/marker.txt")
	if err != nil || got != marker {
		t.Errorf("migrated volume content = %q (%v), want %q", got, err, marker)
	}

	// Desktop is untouched: the source volume and its data still there.
	if src, err := s.runDocker(t, time.Minute, "--context", "desktop-linux", "run", "--rm",
		"-v", probeVol+":/d", probeImg, "cat", "/d/marker.txt"); err != nil || src != marker {
		t.Errorf("source volume changed by migration: %q (%v); it must be read-only", src, err)
	}

	// Re-running is a no-op (everything already present), and idempotent.
	out, err = run(t, 2*time.Minute, s.hawser, "migrate", "--from-desktop",
		"--only", probeImg, "--only", probeVol, "--state-dir", s.stateDir,
		"--docker", s.docker, "--docker-host", dockerHost)
	must(t, out, err, "hawser migrate (second run)")
	if !strings.Contains(out, "Nothing to migrate") {
		t.Errorf("second migrate should be a no-op, got:\n%s", out)
	}
}

func stageInterrupt(t *testing.T, s *state) {
	// The #35 shape: kill a client mid-request, then prove the bridge still
	// answers AND that nothing leaked. Under the v0.1 socat relay this stage
	// could only assert responsiveness (the leak was bounded by an idle
	// timeout, not fixed); with the vsock agent (#40) both ends of the
	// transport are owned, a killed client's connection tears down
	// explicitly, and the wsl.exe count must return to its pre-interrupt
	// value — the regression #35 asked for.
	before := wslProcCount(t)

	long := exec.Command(s.docker, "run", "--rm", "alpine:latest", "sleep", "30")
	long.Env = s.dockerEnv()
	if err := long.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Second)
	long.Process.Kill()
	long.Wait()

	out, err := dockerE(t, s, time.Minute, "version", "--format", "{{.Server.Version}}")
	must(t, out, err, "engine after an interrupted client")
	if out == "" {
		t.Error("engine did not answer after a client was killed mid-run")
	}

	// Teardown is not instantaneous; give it a moment, but far less than the
	// 5-minute socat idle timeout that used to be the only backstop.
	deadline := time.Now().Add(30 * time.Second)
	after := wslProcCount(t)
	for after > before && time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		after = wslProcCount(t)
	}
	if after > before {
		t.Errorf("wsl.exe count %d did not return to the pre-interrupt %d; the interrupted client leaked its relay", after, before)
	}
}

func stageVsockServed(t *testing.T, s *state) {
	// The suite installed from the published release, so this asserts the
	// shipping artifact chain end to end: the rootfs carries hawser-agent,
	// the proxy's vsock dialer reached it, and no connection needed the socat
	// fallback. The fallback logs one edge-triggered warning the moment it is
	// first used; its absence over a suite's worth of traffic means the fast
	// path served everything.
	logBytes, err := os.ReadFile(filepath.Join(s.stateDir, "proxy-e2e.log"))
	if err != nil {
		t.Fatalf("reading proxy log: %v", err)
	}
	if strings.Contains(string(logBytes), "fallback transport") {
		t.Error("proxy degraded to the socat fallback; the published rootfs should carry a reachable hawser-agent")
	}
}

func stageUninstall(t *testing.T, s *state) {
	// The proxy must not outlive the engine it serves.
	if s.proxy != nil && s.proxy.Process != nil {
		s.proxy.Process.Kill()
		s.proxy.Wait()
	}
	if s.proxyLog != nil {
		s.proxyLog.Close()
	}

	out, err := run(t, 5*time.Minute, s.hawser, "uninstall", "--state-dir", s.stateDir, "--yes")
	must(t, out, err, "hawser uninstall")
}

func stageClean(t *testing.T, s *state) {
	out, _ := run(t, 30*time.Second, "wsl.exe", "--list", "--quiet")
	if strings.Contains(strings.ReplaceAll(out, "\x00", ""), distro) {
		t.Errorf("distro %q still registered after uninstall", distro)
	}
	if _, err := os.Stat(s.dataDir); !os.IsNotExist(err) {
		t.Errorf("data dir still present: %s", s.dataDir)
	}
	if _, err := os.Stat(filepath.Join(s.stateDir, "manifest.json")); !os.IsNotExist(err) {
		t.Error("install manifest still present after uninstall")
	}

	// The bounded #35 leak means "returns to baseline" cannot be asserted yet;
	// what can be is that the suite did not permanently double the population.
	after := wslProcCount(t)
	t.Logf("wsl.exe count: before=%d after=%d", s.wslProcsBefore, after)
	if after > s.wslProcsBefore+3 {
		t.Errorf("wsl.exe count grew from %d to %d; relay processes are leaking beyond the bound",
			s.wslProcsBefore, after)
	}
}

func stageDesktopIntact(t *testing.T, s *state) {
	if !s.ddWorkedBefore {
		t.Skip("Docker Desktop was not serving before the suite; nothing to compare against")
	}
	out, err := s.runDocker(t, 2*time.Minute, "--context", "desktop-linux", "run", "--rm", "hello-world")
	if err != nil {
		t.Fatalf("Docker Desktop broken after the suite: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Hello from Docker!") {
		t.Errorf("Docker Desktop output unexpected:\n%s", out)
	}
}

// forceCleanup removes whatever a failed run left, using the binary itself so
// the cleanup path is also the product's own.
func forceCleanup(s *state) {
	if s.proxy != nil && s.proxy.Process != nil {
		s.proxy.Process.Kill()
		s.proxy.Wait()
	}
	if s.proxyLog != nil {
		s.proxyLog.Close()
	}
	if s.hawser != "" && s.stateDir != "" {
		exec.Command(s.hawser, "uninstall", "--state-dir", s.stateDir, "--yes").Run()
	}
	exec.Command("wsl.exe", "--unregister", distro).Run()
}

// findDocker resolves the docker CLI: PATH first, then where Docker Desktop
// installs it. Test environments (notably Git Bash) often lack the PATH entry
// while the binary is right there.
func findDocker() string {
	if p, err := exec.LookPath("docker"); err == nil {
		return p
	}
	for _, c := range []string{
		filepath.Join(os.Getenv("ProgramFiles"), "Docker", "Docker", "resources", "bin", "docker.exe"),
	} {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

// dockerEnv builds the child environment for docker invocations: the suite's
// engine selected, and the CLI's own directory on PATH. The latter matters
// when docker was found outside PATH — its credential helper
// (docker-credential-wincred) lives next to it and is resolved via PATH, and
// without it every pull fails with "error getting credentials". The same fact
// is why #9 bundles the helper alongside the CLI.
func (s *state) dockerEnv() []string {
	env := append(os.Environ(),
		"DOCKER_HOST="+dockerHost,
		"DOCKER_CONTEXT=",
	)
	dir := filepath.Dir(s.docker)
	for i, kv := range env {
		if strings.HasPrefix(strings.ToUpper(kv), "PATH=") {
			env[i] = kv + string(os.PathListSeparator) + dir
			return env
		}
	}
	return append(env, "PATH="+dir)
}

// hostEnv is the environment for docker commands aimed at the machine's own
// engines (Docker Desktop), not the suite's: PATH gains the CLI's directory so
// its credential helper resolves, and no DOCKER_HOST override is applied.
func (s *state) hostEnv() []string {
	env := os.Environ()
	dir := filepath.Dir(s.docker)
	for i, kv := range env {
		if strings.HasPrefix(strings.ToUpper(kv), "PATH=") {
			env[i] = kv + string(os.PathListSeparator) + dir
			return env
		}
	}
	return append(env, "PATH="+dir)
}

// runDocker runs the docker CLI with hostEnv, for Desktop-facing checks.
func (s *state) runDocker(t *testing.T, timeout time.Duration, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(s.docker, args...)
	cmd.Env = s.hostEnv()
	done := make(chan struct{})
	var out []byte
	var err error
	go func() { out, err = cmd.CombinedOutput(); close(done) }()
	select {
	case <-done:
		return strings.TrimSpace(string(out)), err
	case <-time.After(timeout):
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		<-done
		return strings.TrimSpace(string(out)), fmt.Errorf("docker timed out after %s", timeout)
	}
}
