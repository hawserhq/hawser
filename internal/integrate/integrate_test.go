package integrate

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/hawserhq/hawser/internal/wsl"
)

// fakeWSL records Execs and serves a fixed distro list.
type fakeWSL struct {
	wsl.WSL // panic on anything not overridden

	mu      sync.Mutex
	distros []wsl.Distro
	execs   [][]string
	execErr error
}

func (f *fakeWSL) List(context.Context) ([]wsl.Distro, error) {
	return f.distros, nil
}

func (f *fakeWSL) Exec(_ context.Context, distro, user string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execs = append(f.execs, append([]string{distro, user}, args...))
	return "", f.execErr
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func manager(t *testing.T, distros ...string) (*Manager, *fakeWSL) {
	t.Helper()
	f := &fakeWSL{}
	for _, d := range distros {
		f.distros = append(f.distros, wsl.Distro{Name: d, Version: 2})
	}
	return &Manager{WSL: f, StateDir: t.TempDir(), Logger: quiet()}, f
}

func TestIntegrateWritesProfileAndRecords(t *testing.T) {
	m, f := manager(t, "Ubuntu", "hawser-engine")

	if err := m.Integrate(context.Background(), "Ubuntu", "hawser-engine",
		"/mnt/wsl/hawser-engine/docker.sock"); err != nil {
		t.Fatalf("Integrate: %v", err)
	}

	if len(f.execs) != 1 {
		t.Fatalf("%d execs, want 1", len(f.execs))
	}
	joined := strings.Join(f.execs[0], " ")
	if f.execs[0][0] != "Ubuntu" || f.execs[0][1] != "root" {
		t.Errorf("exec targeted %s as %s", f.execs[0][0], f.execs[0][1])
	}
	if !strings.Contains(joined, ProfilePath) {
		t.Errorf("exec does not write %s: %s", ProfilePath, joined)
	}
	// The export travels as a positional parameter, not spliced into the
	// script — and names the socket.
	if !strings.Contains(joined, `DOCKER_HOST="unix:///mnt/wsl/hawser-engine/docker.sock"`) {
		t.Errorf("profile content missing the DOCKER_HOST export: %s", joined)
	}

	got, err := m.List()
	if err != nil || len(got) != 1 || got[0] != "Ubuntu" {
		t.Errorf("List = %v, %v; want [Ubuntu]", got, err)
	}
}

func TestIntegrateRefusesEngineDistro(t *testing.T) {
	m, f := manager(t, "hawser-engine")
	if err := m.Integrate(context.Background(), "hawser-engine", "hawser-engine", "/s"); err == nil {
		t.Fatal("integrated the engine distro into itself")
	}
	if len(f.execs) != 0 {
		t.Error("a refused integrate still ran commands")
	}
}

func TestIntegrateRefusesUnknownDistro(t *testing.T) {
	m, _ := manager(t, "Ubuntu")
	err := m.Integrate(context.Background(), "Debain", "hawser-engine", "/s")
	if err == nil {
		t.Fatal("integrated a distro that does not exist")
	}
	// The error should help with the typo.
	if !strings.Contains(err.Error(), "Ubuntu") {
		t.Errorf("error does not list existing distros: %v", err)
	}
}

func TestRemoveUnwiresAndUnrecords(t *testing.T) {
	m, f := manager(t, "Ubuntu", "hawser-engine")
	if err := m.Integrate(context.Background(), "Ubuntu", "hawser-engine", "/s"); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove(context.Background(), "Ubuntu"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	last := strings.Join(f.execs[len(f.execs)-1], " ")
	if !strings.Contains(last, "rm -f "+ProfilePath) {
		t.Errorf("remove did not delete the profile script: %s", last)
	}
	if got, _ := m.List(); len(got) != 0 {
		t.Errorf("List after Remove = %v, want empty", got)
	}
}

func TestRemoveVanishedDistroUnrecords(t *testing.T) {
	// The distro was integrated and later deleted by the user; uninstall must
	// still come clean instead of failing on it.
	m, f := manager(t, "Ubuntu", "hawser-engine")
	if err := m.Integrate(context.Background(), "Ubuntu", "hawser-engine", "/s"); err != nil {
		t.Fatal(err)
	}
	f.distros = f.distros[1:] // Ubuntu is gone

	if err := m.Remove(context.Background(), "Ubuntu"); err != nil {
		t.Fatalf("Remove of vanished distro: %v", err)
	}
	if got, _ := m.List(); len(got) != 0 {
		t.Errorf("List = %v, want empty", got)
	}
}

func TestRemoveAllBestEffort(t *testing.T) {
	m, f := manager(t, "Ubuntu", "Debian", "hawser-engine")
	m.Integrate(context.Background(), "Ubuntu", "hawser-engine", "/s")
	m.Integrate(context.Background(), "Debian", "hawser-engine", "/s")

	f.execErr = errors.New("distro broken")
	m.RemoveAll(context.Background()) // must not panic or stop at the first failure

	f.execErr = nil
	m.RemoveAll(context.Background())
	if got, _ := m.List(); len(got) != 0 {
		t.Errorf("List after RemoveAll = %v, want empty", got)
	}
}
