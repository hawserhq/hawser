package winpath_test

import (
	"errors"
	"testing"

	"github.com/zcsizmadia/hawser/internal/winpath"
)

func TestToWSL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"simple drive path", `C:\src`, "/mnt/c/src"},
		{"nested", `C:\src\app\main.go`, "/mnt/c/src/app/main.go"},
		{"forward slashes", "C:/src/app", "/mnt/c/src/app"},
		{"mixed separators", `C:\src/app\deep`, "/mnt/c/src/app/deep"},
		{"lowercase drive", `c:\src`, "/mnt/c/src"},
		{"non-C drive uppercased", `D:\data`, "/mnt/d/data"},
		{"drive root backslash", `C:\`, "/mnt/c"},
		{"drive root forward", "C:/", "/mnt/c"},
		{"spaces in path", `C:\Program Files\app`, "/mnt/c/Program Files/app"},
		{"trailing separator", `C:\src\`, "/mnt/c/src/"},
		// Idempotence and pass-through: users who already speak WSL, and
		// container-side absolute paths, must survive untouched.
		{"already wsl path", "/mnt/c/src", "/mnt/c/src"},
		{"posix absolute", "/app", "/app"},
		{"posix root", "/", "/"},
		{"relative", "./src", "./src"},
		{"dot", ".", "."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := winpath.ToWSL(tt.in)
			if err != nil {
				t.Fatalf("ToWSL(%q) error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ToWSL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestToWSLIdempotent(t *testing.T) {
	// Translating twice must not double-prefix; the proxy may see a path that
	// has already been through translation.
	for _, in := range []string{`C:\src`, "C:/src", "/mnt/c/src", "/app"} {
		once, err := winpath.ToWSL(in)
		if err != nil {
			t.Fatalf("ToWSL(%q): %v", in, err)
		}
		twice, err := winpath.ToWSL(once)
		if err != nil {
			t.Fatalf("ToWSL(ToWSL(%q)): %v", in, err)
		}
		if once != twice {
			t.Errorf("not idempotent for %q: %q then %q", in, once, twice)
		}
	}
}

func TestToWSLRejectsUNC(t *testing.T) {
	for _, in := range []string{`\\server\share`, `\\server\share\dir`, "//server/share"} {
		_, err := winpath.ToWSL(in)
		if err == nil {
			t.Errorf("ToWSL(%q) succeeded, want ErrUNC", in)
			continue
		}
		var uncErr *winpath.ErrUNC
		if !errors.As(err, &uncErr) {
			t.Errorf("ToWSL(%q) error = %T (%v), want *ErrUNC", in, err, err)
		}
	}
}

func TestToWSLMapsPipesToEngineSocket(t *testing.T) {
	for _, in := range []string{`\\.\pipe\docker_engine`, "//./pipe/hawser_engine", `\\.\PIPE\anything`} {
		got, err := winpath.ToWSL(in)
		if err != nil {
			t.Errorf("ToWSL(%q) error: %v", in, err)
			continue
		}
		if got != winpath.EngineSocket {
			t.Errorf("ToWSL(%q) = %q, want %q", in, got, winpath.EngineSocket)
		}
	}
	// A share on a host that happens to be called "." is still a share.
	for _, in := range []string{`\\.\share\x`, `\\pipe\x`, `\\.\pipe`} {
		if _, err := winpath.ToWSL(in); err == nil {
			t.Errorf("ToWSL(%q) succeeded, want ErrUNC", in)
		}
	}
}

func TestToWSLEmpty(t *testing.T) {
	if _, err := winpath.ToWSL(""); err == nil {
		t.Error("ToWSL(\"\") succeeded, want error")
	}
}

func TestTranslateBind(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		// The exact forms Spike A measured against a real daemon.
		{"windows source backslash", `C:\src:/app`, "/mnt/c/src:/app"},
		{"windows source forward", "C:/src:/app", "/mnt/c/src:/app"},
		{"already wsl source", "/mnt/c/src:/app", "/mnt/c/src:/app"},
		// Options must ride along untouched.
		{"read-only option", `C:\src:/app:ro`, "/mnt/c/src:/app:ro"},
		{"multiple options", `C:\src:/app:ro,z`, "/mnt/c/src:/app:ro,z"},
		{"consistency option", `C:\src:/app:cached`, "/mnt/c/src:/app:cached"},
		// Named volumes are identifiers, not paths — translating them would
		// silently convert a volume into a bind mount.
		{"named volume", "myvolume:/data", "myvolume:/data"},
		{"named volume with dash", "my-vol_2.1:/data", "my-vol_2.1:/data"},
		{"named volume read-only", "myvolume:/data:ro", "myvolume:/data:ro"},
		// No source at all: anonymous volume.
		{"anonymous volume", "/data", "/data"},
		// Paths with spaces, the classic Windows case.
		{"spaces", `C:\Program Files\src:/app`, "/mnt/c/Program Files/src:/app"},
		// Drive root as source.
		{"drive root", `C:\:/app`, "/mnt/c:/app"},
		// Linux-style host path passed through.
		{"posix host path", "/home/user/src:/app", "/home/user/src:/app"},
		// The engine pipe, as Testcontainers' Ryuk and `-v //./pipe/docker_engine:
		// /var/run/docker.sock` name it: it means the engine's own socket.
		{"engine pipe forward", "//./pipe/hawser_engine:/var/run/docker.sock", "/var/run/docker.sock:/var/run/docker.sock"},
		{"engine pipe backslash", `\\.\pipe\docker_engine:/var/run/docker.sock:ro`, "/var/run/docker.sock:/var/run/docker.sock:ro"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := winpath.TranslateBind(tt.in)
			if err != nil {
				t.Fatalf("TranslateBind(%q) error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("TranslateBind(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestTranslateBindRejectsUNC(t *testing.T) {
	if _, err := winpath.TranslateBind(`\\server\share:/app`); err == nil {
		t.Error("TranslateBind of UNC source succeeded, want error")
	}
}

func TestTranslateBinds(t *testing.T) {
	in := []string{`C:\src:/app`, "myvolume:/data", "/mnt/d/x:/y:ro"}
	want := []string{"/mnt/c/src:/app", "myvolume:/data", "/mnt/d/x:/y:ro"}

	got, err := winpath.TranslateBinds(in)
	if err != nil {
		t.Fatalf("TranslateBinds: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d binds, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("bind %d = %q, want %q", i, got[i], want[i])
		}
	}

	// The input slice must not be mutated — the proxy may need the original
	// for error messages after a partial failure.
	if in[0] != `C:\src:/app` {
		t.Errorf("input mutated: %q", in[0])
	}
}

func TestTranslateBindsNil(t *testing.T) {
	// A create request with no binds is the common case; nil must stay nil
	// rather than becoming an empty array in the re-encoded JSON.
	got, err := winpath.TranslateBinds(nil)
	if err != nil {
		t.Fatalf("TranslateBinds(nil): %v", err)
	}
	if got != nil {
		t.Errorf("TranslateBinds(nil) = %v, want nil", got)
	}
}

func TestTranslateBindsFailsLoudly(t *testing.T) {
	// One bad spec must fail the batch, not silently drop a mount.
	_, err := winpath.TranslateBinds([]string{`C:\ok:/app`, `\\srv\share:/bad`})
	if err == nil {
		t.Fatal("TranslateBinds succeeded with a UNC bind, want error")
	}
}
