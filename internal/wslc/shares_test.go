package wslc

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
)

// shareRunner fakes the CLI: it records commands, answers the "does the share
// still exist" probe, and pretends `run -d` succeeds.
type shareRunner struct {
	mu       sync.Mutex
	calls    [][]string
	shareOK  bool // what the `test -d` probe reports
	runFails bool
}

func (r *shareRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	r.mu.Lock()
	r.calls = append(r.calls, args)
	shareOK, runFails := r.shareOK, r.runFails
	r.mu.Unlock()

	joined := strings.Join(args, " ")
	switch {
	case strings.Contains(joined, "test -d"):
		if shareOK {
			return []byte("yes"), nil
		}
		return []byte("no"), nil
	case strings.Contains(joined, "run -d"):
		if runFails {
			return nil, fmt.Errorf("no such session")
		}
		r.mu.Lock()
		r.shareOK = true // the holder now exists, so the share does too
		r.mu.Unlock()
		return []byte("abc123"), nil
	}
	return []byte(""), nil
}

func (r *shareRunner) countMatching(sub string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.calls {
		if strings.Contains(strings.Join(c, " "), sub) {
			n++
		}
	}
	return n
}

// inspectDialer answers the holder inspect with a fixed share path.
func inspectDialer(share string) func(context.Context) (io.ReadWriteCloser, error) {
	body := fmt.Sprintf(`{"Mounts":[{"Type":"bind","Source":%q,"Destination":"/share"}]}`, share)
	return func(context.Context) (io.ReadWriteCloser, error) {
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			buf := make([]byte, 2048)
			if _, err := server.Read(buf); err != nil {
				return
			}
			fmt.Fprintf(server, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
		}()
		return client, nil
	}
}

func shareTable(r *shareRunner, share string) *ShareTable {
	return &ShareTable{
		Local:      &Local{Exe: "wslc.exe", Runner: r},
		Session:    "s",
		EngineDial: inspectDialer(share),
	}
}

const testShare = "/mnt/{c0633ed5-7f75-4cd7-a5df-f559eba7fc79}"

func TestTranslateDrivePathToShare(t *testing.T) {
	s := shareTable(&shareRunner{}, testShare)
	got, err := s.Translate(context.Background(), `C:\src\app`)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	want := testShare + "/src/app"
	if got != want {
		t.Errorf("Translate = %q, want %q", got, want)
	}
}

func TestTranslateDriveRoot(t *testing.T) {
	s := shareTable(&shareRunner{}, testShare)
	got, err := s.Translate(context.Background(), `C:\`)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if got != testShare {
		t.Errorf("Translate = %q, want the share itself %q", got, testShare)
	}
}

// A pipe means this engine's socket on every backend (#164) and must not go
// anywhere near the share table — Ryuk depends on it.
func TestTranslatePipeStillMeansTheEngineSocket(t *testing.T) {
	r := &shareRunner{}
	s := shareTable(r, testShare)
	got, err := s.Translate(context.Background(), `\\.\pipe\docker_engine`)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if got != EngineSocket {
		t.Errorf("Translate = %q, want %q", got, EngineSocket)
	}
	if n := r.countMatching("run -d"); n != 0 {
		t.Errorf("a pipe source started %d share holders; it should start none", n)
	}
}

// Guest paths are what make docker-in-docker and /tmp binds work here, so they
// must pass through without touching the share table.
func TestTranslateGuestPathsPassThrough(t *testing.T) {
	r := &shareRunner{}
	s := shareTable(r, testShare)
	for _, p := range []string{"/tmp", "/var/run/docker.sock", "/c/src", "//c/src"} {
		got, err := s.Translate(context.Background(), p)
		if err != nil {
			t.Errorf("Translate(%q): %v", p, err)
			continue
		}
		if got != p {
			t.Errorf("Translate(%q) = %q, want it unchanged", p, got)
		}
	}
	if n := r.countMatching("run -d"); n != 0 {
		t.Errorf("guest paths started %d share holders; they should start none", n)
	}
}

// One holder per drive, not one per bind. A compose project with a dozen binds
// under C: must not start a dozen containers.
func TestOneHolderPerDrive(t *testing.T) {
	r := &shareRunner{}
	s := shareTable(r, testShare)
	for _, p := range []string{`C:\a`, `C:\b`, `C:\c\d`, `C:\a`} {
		if _, err := s.Translate(context.Background(), p); err != nil {
			t.Fatalf("Translate(%q): %v", p, err)
		}
	}
	if n := r.countMatching("run -d"); n != 1 {
		t.Errorf("started %d holders for four binds on one drive, want 1", n)
	}
}

// The GUID is per mount, so a session VM that restarted has a different share
// or none. A cached path that no longer exists must be re-established rather
// than handed to dockerd, which would mount an empty directory.
func TestStaleShareIsReestablished(t *testing.T) {
	r := &shareRunner{}
	s := shareTable(r, testShare)

	if _, err := s.Translate(context.Background(), `C:\a`); err != nil {
		t.Fatalf("first Translate: %v", err)
	}
	// The VM restarted: the mount point is gone.
	r.mu.Lock()
	r.shareOK = false
	r.mu.Unlock()

	if _, err := s.Translate(context.Background(), `C:\a`); err != nil {
		t.Fatalf("second Translate: %v", err)
	}
	if n := r.countMatching("run -d"); n < 2 {
		t.Errorf("started %d holders; a vanished share must be re-established", n)
	}
}

// A drive that cannot be shared has to fail with something a user can act on,
// naming the drive rather than a GUID they have never seen.
func TestShareFailureIsReported(t *testing.T) {
	s := shareTable(&shareRunner{runFails: true}, testShare)
	_, err := s.Translate(context.Background(), `D:\data`)
	if err == nil {
		t.Fatal("want an error when the holder cannot be started")
	}
	if !strings.Contains(err.Error(), "D:") {
		t.Errorf("error %q does not name the drive", err)
	}
}

// Concurrent first binds to the SAME drive must produce one holder, not a race
// where the second force-deletes the first one's live share.
//
// ensureHolder deletes any existing holder before creating its own, and it ran
// unlocked. A compose project binding C: from several services hit this: the
// loser's container was created with a /mnt/{GUID} that had just been
// destroyed, so its bind mount was silently empty.
func TestConcurrentBindsToOneDriveCreateOneHolder(t *testing.T) {
	r := &shareRunner{}
	s := shareTable(r, testShare)

	var wg sync.WaitGroup
	got := make([]string, 8)
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p, err := s.Translate(context.Background(), `C:\src`)
			if err != nil {
				t.Errorf("Translate: %v", err)
				return
			}
			got[i] = p
		}(i)
	}
	wg.Wait()

	if n := r.countMatching("run -d"); n != 1 {
		t.Errorf("started %d holders for concurrent binds to one drive, want 1", n)
	}
	// And everyone got the same share, not a mix of live and destroyed ones.
	for i, p := range got {
		if p != got[0] {
			t.Errorf("goroutine %d got %q, goroutine 0 got %q", i, p, got[0])
		}
	}
}
