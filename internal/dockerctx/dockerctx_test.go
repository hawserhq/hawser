package dockerctx_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hawserhq/hawser/internal/dockerctx"
)

// fakeDocker records docker CLI invocations and replays canned output, so every
// argument list is asserted without a docker binary present.
type fakeDocker struct {
	calls   [][]string
	replies map[string]reply
}

type reply struct {
	out string
	err error
}

func newFakeDocker() *fakeDocker {
	return &fakeDocker{replies: map[string]reply{
		"--version": {out: "Docker version 29.7.2, build 6a43e3d"},
	}}
}

func (f *fakeDocker) on(prefix, out string, err error) *fakeDocker {
	f.replies[prefix] = reply{out: out, err: err}
	return f
}

func (f *fakeDocker) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	joined := strings.Join(args, " ")
	// Longest matching prefix wins, so "context ls" beats "context".
	best, bestLen := reply{err: errors.New("fakeDocker: no reply for " + joined)}, -1
	for prefix, r := range f.replies {
		if strings.HasPrefix(joined, prefix) && len(prefix) > bestLen {
			best, bestLen = r, len(prefix)
		}
	}
	return []byte(best.out), best.err
}

func (f *fakeDocker) called(prefix string) bool {
	for _, c := range f.calls {
		if strings.HasPrefix(strings.Join(c, " "), prefix) {
			return true
		}
	}
	return false
}

func (f *fakeDocker) callWith(prefix string) []string {
	for _, c := range f.calls {
		if strings.HasPrefix(strings.Join(c, " "), prefix) {
			return c
		}
	}
	return nil
}

const hawserHost = "npipe:////./pipe/hawser_engine"

func TestEnsureCreatesMissingContext(t *testing.T) {
	f := newFakeDocker().
		on("context ls", "default\ndesktop-linux", nil).
		on("context create", "hawser", nil)

	m := &dockerctx.Manager{Runner: f}
	if err := m.Ensure(context.Background(), hawserHost); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	args := f.callWith("context create")
	if args == nil {
		t.Fatal("context was not created")
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "host="+hawserHost) {
		t.Errorf("create args %q do not set the endpoint", joined)
	}
	if f.called("context update") {
		t.Error("update was called for a context that did not exist")
	}
}

func TestEnsureIsIdempotent(t *testing.T) {
	// Reinstalling, or running proxy repeatedly, must not churn the context.
	f := newFakeDocker().
		on("context ls", "default\nhawser", nil).
		on("context inspect", hawserHost, nil)

	m := &dockerctx.Manager{Runner: f}
	if err := m.Ensure(context.Background(), hawserHost); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if f.called("context create") {
		t.Error("create was called for an existing context")
	}
	if f.called("context update") {
		t.Error("update was called when the endpoint already matched")
	}
}

func TestEnsureUpdatesMovedEndpoint(t *testing.T) {
	// The endpoint moves for real: Hawser serves the default pipe when it is
	// free and its own when Docker Desktop holds it, so installing Desktop
	// later changes which pipe is correct.
	f := newFakeDocker().
		on("context ls", "hawser", nil).
		on("context inspect", "npipe:////./pipe/docker_engine", nil).
		on("context update", "hawser", nil)

	m := &dockerctx.Manager{Runner: f}
	if err := m.Ensure(context.Background(), hawserHost); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	args := f.callWith("context update")
	if args == nil {
		t.Fatal("context was not updated")
	}
	if !strings.Contains(strings.Join(args, " "), "host="+hawserHost) {
		t.Errorf("update did not set the new endpoint: %v", args)
	}
	if f.called("context create") {
		t.Error("create was called instead of update, which would lose the selection")
	}
}

func TestEnsureRequiresHost(t *testing.T) {
	m := &dockerctx.Manager{Runner: newFakeDocker()}
	if err := m.Ensure(context.Background(), ""); err == nil {
		t.Error("Ensure succeeded with no docker host")
	}
}

func TestMissingDockerCLIIsTypedAndNonFatal(t *testing.T) {
	// Install must be able to distinguish "no docker CLI" (carry on, the engine
	// works over DOCKER_HOST) from a real failure.
	f := &fakeDocker{replies: map[string]reply{
		"--version": {err: errors.New(`exec: "docker": executable file not found in %PATH%`)},
	}}
	m := &dockerctx.Manager{Runner: f}

	err := m.Ensure(context.Background(), hawserHost)
	if err == nil {
		t.Fatal("Ensure succeeded with no docker CLI")
	}
	var noCLI *dockerctx.ErrNoDockerCLI
	if !errors.As(err, &noCLI) {
		t.Fatalf("error = %T (%v), want *ErrNoDockerCLI", err, err)
	}
	// The message must point at the working alternative.
	if !strings.Contains(err.Error(), "DOCKER_HOST") {
		t.Errorf("message should mention DOCKER_HOST: %v", err)
	}
	if f.called("context create") {
		t.Error("tried to create a context without a working CLI")
	}
}

func TestExistsDetectsPresenceAndAbsence(t *testing.T) {
	present := &dockerctx.Manager{Runner: newFakeDocker().
		on("context ls", "default\nhawser\ndesktop-linux", nil)}
	if ok, err := present.Exists(context.Background()); err != nil || !ok {
		t.Errorf("Exists = %v, %v; want true", ok, err)
	}

	absent := &dockerctx.Manager{Runner: newFakeDocker().
		on("context ls", "default\ndesktop-linux", nil)}
	if ok, err := absent.Exists(context.Background()); err != nil || ok {
		t.Errorf("Exists = %v, %v; want false", ok, err)
	}
}

func TestExistsIgnoresSubstringMatches(t *testing.T) {
	// A context named "hawser-test" must not be mistaken for "hawser".
	m := &dockerctx.Manager{Runner: newFakeDocker().
		on("context ls", "default\nhawser-test\nmy-hawser", nil)}
	if ok, err := m.Exists(context.Background()); err != nil || ok {
		t.Errorf("Exists = %v, %v; want false for substring matches only", ok, err)
	}
}

func TestRemoveSwitchesAwayFirst(t *testing.T) {
	// docker refuses to remove the context in use, and leaving the user on a
	// context that no longer exists would break every later docker command.
	f := newFakeDocker().
		on("context ls", "default\nhawser", nil).
		on("context show", "hawser", nil).
		on("context use", "default", nil).
		on("context rm", "hawser", nil)

	m := &dockerctx.Manager{Runner: f}
	if err := m.Remove(context.Background(), "desktop-linux"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	useArgs := f.callWith("context use")
	if useArgs == nil {
		t.Fatal("did not switch away before removing")
	}
	if got := useArgs[len(useArgs)-1]; got != "desktop-linux" {
		t.Errorf("restored to %q, want desktop-linux", got)
	}
	if !f.called("context rm") {
		t.Error("context was not removed")
	}
}

func TestRemoveDefaultsRestoreTarget(t *testing.T) {
	f := newFakeDocker().
		on("context ls", "hawser", nil).
		on("context show", "hawser", nil).
		on("context use", "default", nil).
		on("context rm", "hawser", nil)

	m := &dockerctx.Manager{Runner: f}
	if err := m.Remove(context.Background(), ""); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	useArgs := f.callWith("context use")
	if useArgs == nil || useArgs[len(useArgs)-1] != "default" {
		t.Errorf("restore target = %v, want default", useArgs)
	}
}

func TestRemoveSkipsSwitchWhenNotCurrent(t *testing.T) {
	f := newFakeDocker().
		on("context ls", "default\nhawser", nil).
		on("context show", "desktop-linux", nil).
		on("context rm", "hawser", nil)

	m := &dockerctx.Manager{Runner: f}
	if err := m.Remove(context.Background(), ""); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if f.called("context use") {
		t.Error("switched contexts even though hawser was not current")
	}
}

func TestRemoveOnAbsentContextIsNoOp(t *testing.T) {
	// Uninstall on a machine where the context was already removed by hand.
	f := newFakeDocker().on("context ls", "default", nil)
	m := &dockerctx.Manager{Runner: f}
	if err := m.Remove(context.Background(), ""); err != nil {
		t.Errorf("Remove = %v, want nil", err)
	}
	if f.called("context rm") {
		t.Error("tried to remove a context that does not exist")
	}
}

func TestErrorsCarryDockerMessage(t *testing.T) {
	// The CLI's own text is the diagnosis; the exit code says nothing.
	f := newFakeDocker().
		on("context ls", "default", nil).
		on("context create", `context "hawser" already exists`, errors.New("exit status 1"))

	m := &dockerctx.Manager{Runner: f}
	err := m.Ensure(context.Background(), hawserHost)
	if err == nil {
		t.Fatal("Ensure succeeded")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error %q does not include docker's message", err)
	}
}
