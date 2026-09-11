package wsl

import (
	"strings"
	"testing"
	"unicode/utf16"
)

// utf16le encodes text the way wsl.exe emits its own messages.
func utf16le(s string, bom bool) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 0, len(u)*2+2)
	if bom {
		b = append(b, 0xFF, 0xFE)
	}
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	return b
}

func TestDecodeOutputUTF16(t *testing.T) {
	// The exact shape that produced "T h e   o p e r a t i o n" in raw output.
	want := "The operation completed successfully."
	for _, bom := range []bool{true, false} {
		got := decodeOutput(utf16le(want, bom))
		if got != want {
			t.Errorf("bom=%v: decodeOutput = %q, want %q", bom, got, want)
		}
	}
}

func TestDecodeOutputUTF16BigEndian(t *testing.T) {
	want := "Windows Subsystem for Linux"
	u := utf16.Encode([]rune(want))
	b := []byte{0xFE, 0xFF}
	for _, c := range u {
		b = append(b, byte(c>>8), byte(c))
	}
	if got := decodeOutput(b); got != want {
		t.Errorf("decodeOutput = %q, want %q", got, want)
	}
}

func TestDecodeOutputUTF8Passthrough(t *testing.T) {
	// Output of a command run *inside* a distro is plain UTF-8 and must not be
	// touched — including non-ASCII, which a NUL-stripping approach mangles.
	for _, s := range []string{
		"alive",
		"Docker version 29.7.2, build 6a43e3d",
		"héllo wörld",
		"日本語のテキスト",
		"",
	} {
		if got := decodeOutput([]byte(s)); got != s {
			t.Errorf("decodeOutput(%q) = %q, want unchanged", s, got)
		}
	}
}

func TestDecodeOutputUTF8WithBOM(t *testing.T) {
	b := append([]byte{0xEF, 0xBB, 0xBF}, []byte("hello")...)
	if got := decodeOutput(b); got != "hello" {
		t.Errorf("decodeOutput = %q, want %q", got, "hello")
	}
}

func TestDecodeOutputDoesNotMisreadBinary(t *testing.T) {
	// Binary content with embedded NULs must not be mistaken for UTF-16; a
	// container's stdout can legitimately contain arbitrary bytes.
	binary := []byte{0x89, 0x50, 0x4E, 0x47, 0x00, 0x0D, 0x0A, 0x1A, 0xFF, 0xD8, 0xFF, 0xE0}
	got := decodeOutput(binary)
	if got != string(binary) {
		t.Errorf("binary input altered: %q", got)
	}
}

func TestLooksUTF16LE(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		want bool
	}{
		{"utf16 ascii text", utf16le("Ubuntu\nStopped", false), true},
		{"plain utf8", []byte("Ubuntu\nStopped"), false},
		{"utf8 with one nul", []byte("abc\x00def ghijklmnop"), false},
		{"too short", []byte{0x41, 0x00}, false},
		{"odd length", []byte{0x41, 0x00, 0x42}, false},
		{"empty", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := looksUTF16LE(tt.in); got != tt.want {
				t.Errorf("looksUTF16LE = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCleanLines(t *testing.T) {
	in := "  first  \r\n\r\n   second\n\n  \nthird\r\n"
	got := cleanLines(in)
	want := []string{"first", "second", "third"}
	if len(got) != len(want) {
		t.Fatalf("got %d lines %q, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParseListVerbose(t *testing.T) {
	// Real shape of `wsl --list --verbose`, including the two-space indent and
	// the default marker.
	out := strings.Join([]string{
		"  NAME                   STATE           VERSION",
		"* Ubuntu                 Running         2",
		"  skrog-engine          Stopped         2",
		"  docker-desktop         Running         2",
		"  legacy-distro          Stopped         1",
	}, "\n")

	got := parseListVerbose(out)
	if len(got) != 4 {
		t.Fatalf("got %d distros %+v, want 4", len(got), got)
	}

	if got[0].Name != "Ubuntu" || !got[0].Default || !got[0].Running() || got[0].Version != 2 {
		t.Errorf("first distro = %+v", got[0])
	}
	if got[1].Name != "skrog-engine" || got[1].Default || got[1].Running() {
		t.Errorf("skrog-engine = %+v", got[1])
	}
	if got[3].Version != 1 {
		t.Errorf("legacy distro version = %d, want 1", got[3].Version)
	}
}

func TestParseListVerboseSkipsHeaderAndJunk(t *testing.T) {
	// A localized header must not become a phantom distro.
	out := "  NOM                    ÉTAT            VERSION\n* Ubuntu                 Arrêté          2"
	got := parseListVerbose(out)
	if len(got) != 1 || got[0].Name != "Ubuntu" {
		t.Fatalf("got %+v, want just Ubuntu", got)
	}
	// State is localized; Running() is false, which is the safe reading.
	if got[0].Running() {
		t.Error("localized state should not read as Running")
	}
}

func TestParseListVerboseEmpty(t *testing.T) {
	if got := parseListVerbose(""); len(got) != 0 {
		t.Errorf("got %+v, want none", got)
	}
}

func TestParseListVerboseNameWithSpaces(t *testing.T) {
	out := "  NAME      STATE     VERSION\n  My Distro 2004      Stopped   2"
	got := parseListVerbose(out)
	if len(got) != 1 {
		t.Fatalf("got %d, want 1", len(got))
	}
	if got[0].Name != "My Distro 2004" {
		t.Errorf("name = %q, want %q", got[0].Name, "My Distro 2004")
	}
}
