package engineconfig

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// fakeDistro simulates just enough of the distro shell for the manager: a tiny
// file map, cp/mv, base64 writes, cat, and a programmable dockerd --validate.
type fakeDistro struct {
	files      map[string]string
	validateOK bool
	ops        []string // classified operation log, for ordering assertions
}

func newFakeDistro() *fakeDistro {
	return &fakeDistro{files: map[string]string{}, validateOK: true}
}

func (f *fakeDistro) Exec(_ context.Context, _, _ string, args ...string) (string, error) {
	cmd := args[len(args)-1] // sh -c "<cmd>"

	switch {
	case strings.HasPrefix(cmd, "cat "):
		f.ops = append(f.ops, "read")
		path := strings.Fields(cmd)[1]
		return f.files[path], nil

	case strings.Contains(cmd, "dockerd --validate"):
		f.ops = append(f.ops, "validate")
		if !f.validateOK {
			return "invalid config: unknown field \"bogus\"", errors.New("exit 1")
		}
		return "configuration OK", nil

	case strings.Contains(cmd, "base64 -d > "):
		path := cmd[strings.Index(cmd, "base64 -d > ")+len("base64 -d > "):]
		path = strings.TrimSpace(path)
		// b64 is the token after "printf %s ".
		rest := cmd[strings.Index(cmd, "printf %s ")+len("printf %s "):]
		b64 := strings.Fields(rest)[0]
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return "", err
		}
		f.files[path] = string(raw)
		if strings.HasSuffix(path, ".candidate") {
			f.ops = append(f.ops, "write-candidate")
		} else {
			f.ops = append(f.ops, "write-live")
		}
		return "", nil

	case strings.Contains(cmd, "cp ") && strings.Contains(cmd, ".bak"):
		f.ops = append(f.ops, "backup")
		src := DefaultDaemonPath
		if v, ok := f.files[src]; ok {
			f.files[src+".bak"] = v
		}
		return "", nil

	case strings.HasPrefix(cmd, "[ -f ") && strings.Contains(cmd, "mv "):
		f.ops = append(f.ops, "rollback-restore")
		if v, ok := f.files[DefaultDaemonPath+".bak"]; ok {
			f.files[DefaultDaemonPath] = v
			delete(f.files, DefaultDaemonPath+".bak")
		} else {
			delete(f.files, DefaultDaemonPath)
		}
		return "", nil

	default:
		return "", nil
	}
}

func TestSetOnRunningEngineValidatesThenWritesThenRestarts(t *testing.T) {
	fd := newFakeDistro()
	fd.files[DefaultDaemonPath] = `{"log-driver":"json-file"}`
	restarts := 0
	m := &Manager{
		WSL:           fd,
		Distro:        "d",
		EngineRunning: func(context.Context) bool { return true },
		Restart:       func(context.Context) error { restarts++; return nil },
	}

	res, err := m.Set(context.Background(), "registry-mirrors", "https://mirror.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Restarted || res.Applied != "https://mirror.example.com" {
		t.Fatalf("result = %+v", res)
	}
	if restarts != 1 {
		t.Fatalf("restarts = %d, want 1", restarts)
	}

	// Ordering: validate must precede the live write.
	order := strings.Join(fd.ops, ",")
	vi, wi := strings.Index(order, "validate"), strings.Index(order, "write-live")
	if vi < 0 || wi < 0 || vi > wi {
		t.Fatalf("validate must come before write-live; ops = %s", order)
	}
	// The live file now carries both the old and new keys.
	live := fd.files[DefaultDaemonPath]
	if !strings.Contains(live, "registry-mirrors") || !strings.Contains(live, "log-driver") {
		t.Fatalf("live daemon.json = %s", live)
	}
}

func TestSetRejectsInvalidConfigWithoutTouchingLiveFile(t *testing.T) {
	fd := newFakeDistro()
	fd.validateOK = false
	fd.files[DefaultDaemonPath] = `{"log-driver":"json-file"}`
	restarts := 0
	m := &Manager{
		WSL:           fd,
		Distro:        "d",
		EngineRunning: func(context.Context) bool { return true },
		Restart:       func(context.Context) error { restarts++; return nil },
	}

	if _, err := m.Set(context.Background(), "log-driver", "bogusdriver"); err == nil {
		t.Fatal("expected validation error")
	}
	if restarts != 0 {
		t.Errorf("engine must not restart on invalid config; restarts = %d", restarts)
	}
	if fd.files[DefaultDaemonPath] != `{"log-driver":"json-file"}` {
		t.Errorf("live file changed on invalid config: %s", fd.files[DefaultDaemonPath])
	}
}

func TestSetRollsBackWhenEngineDoesNotComeBack(t *testing.T) {
	fd := newFakeDistro()
	fd.files[DefaultDaemonPath] = `{"log-driver":"json-file"}`
	m := &Manager{
		WSL:           fd,
		Distro:        "d",
		EngineRunning: func(context.Context) bool { return true },
		Restart:       func(context.Context) error { return errors.New("dockerd did not create its socket") },
	}

	if _, err := m.Set(context.Background(), "dns", "1.1.1.1"); err == nil {
		t.Fatal("expected a restart failure error")
	}
	// After rollback the live file is the original again.
	if !strings.Contains(fd.files[DefaultDaemonPath], "json-file") ||
		strings.Contains(fd.files[DefaultDaemonPath], "1.1.1.1") {
		t.Errorf("rollback did not restore the original: %s", fd.files[DefaultDaemonPath])
	}
	if !strings.Contains(strings.Join(fd.ops, ","), "rollback-restore") {
		t.Errorf("expected a rollback-restore op; ops = %v", fd.ops)
	}
}

func TestSetOnStoppedEngineIsPending(t *testing.T) {
	fd := newFakeDistro()
	restarts := 0
	m := &Manager{
		WSL:           fd,
		Distro:        "d",
		EngineRunning: func(context.Context) bool { return false },
		Restart:       func(context.Context) error { restarts++; return nil },
	}
	res, err := m.Set(context.Background(), "dns", "1.1.1.1")
	if err != nil {
		t.Fatal(err)
	}
	if restarts != 0 {
		t.Errorf("stopped engine must not be restarted; restarts = %d", restarts)
	}
	if !res.PendingRestart {
		t.Errorf("expected PendingRestart; got %+v", res)
	}
}

func TestGetAndListAndClear(t *testing.T) {
	fd := newFakeDistro()
	fd.files[DefaultDaemonPath] = `{"registry-mirrors":["https://m"],"log-opts":{"max-size":"10m"}}`
	m := &Manager{WSL: fd, Distro: "d"}

	got, err := m.Get(context.Background(), "registry-mirrors")
	if err != nil || got != "https://m" {
		t.Fatalf("Get = %q, %v", got, err)
	}

	list, err := m.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if list["engine.registry-mirrors"] != "https://m" || list["engine.dns"] != "" {
		t.Fatalf("List = %+v", list)
	}

	// Clear by empty value: the key disappears from daemon.json.
	if _, err := m.Set(context.Background(), "registry-mirrors", ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fd.files[DefaultDaemonPath], "registry-mirrors") {
		t.Errorf("registry-mirrors should be cleared: %s", fd.files[DefaultDaemonPath])
	}
	if !strings.Contains(fd.files[DefaultDaemonPath], "log-opts") {
		t.Errorf("clearing one key dropped another: %s", fd.files[DefaultDaemonPath])
	}
}

func TestSetUnknownKey(t *testing.T) {
	m := &Manager{WSL: newFakeDistro(), Distro: "d"}
	if _, err := m.Set(context.Background(), "totally-bogus", "x"); err == nil {
		t.Fatal("expected unknown-key error")
	}
}
