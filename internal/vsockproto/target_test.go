package vsockproto

import (
	"bytes"
	"strings"
	"testing"
)

func TestTargetRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteTarget(&buf, "172.17.0.3:80"); err != nil {
		t.Fatalf("WriteTarget: %v", err)
	}
	if got := buf.String(); got != "CONNECT 172.17.0.3:80\n" {
		t.Errorf("wire form = %q", got)
	}
	got, err := ReadTarget(&buf)
	if err != nil {
		t.Fatalf("ReadTarget: %v", err)
	}
	if got != "172.17.0.3:80" {
		t.Errorf("target = %q", got)
	}
}

func TestReadTargetRejectsAForeignLine(t *testing.T) {
	// A client that skipped straight to data, or reached the forward port by
	// mistake, must be refused rather than have its first bytes parsed as a
	// destination.
	for _, line := range []string{
		"GET /version HTTP/1.1\n",
		"SKROG/1\n",
		"\n",
	} {
		if _, err := ReadTarget(strings.NewReader(line)); err == nil {
			t.Errorf("ReadTarget(%q) succeeded, want an error", line)
		}
	}
}

func TestValidTargetAcceptsWhatDockerPublishes(t *testing.T) {
	for _, ok := range []string{
		"172.17.0.3:80",
		"127.0.0.1:8080",
		"10.0.0.1:65535",
		"[::1]:80",
		"host.internal:5432",
	} {
		if err := ValidTarget(ok); err != nil {
			t.Errorf("ValidTarget(%q) = %v, want nil", ok, err)
		}
	}
}

func TestValidTargetRejectsTheDangerousShapes(t *testing.T) {
	cases := map[string]string{
		"empty":            "",
		"no port":          "172.17.0.3",
		"port zero":        "172.17.0.3:0",
		"port too large":   "172.17.0.3:70000",
		"non-numeric port": "172.17.0.3:http",
		"no host":          ":80",
		// A newline would let one target line inject a second request.
		"embedded newline": "172.17.0.3:80\nCONNECT evil:22",
		"embedded NUL":     "172.17.0.3:80\x00",
	}
	for name, target := range cases {
		if err := ValidTarget(target); err == nil {
			t.Errorf("%s: ValidTarget(%q) = nil, want an error", name, target)
		}
	}
	if err := ValidTarget(strings.Repeat("a", maxTarget+1) + ":80"); err == nil {
		t.Error("an over-long target was accepted")
	}
}

// WriteTarget validates too, so a bug on the host cannot put a malformed line
// on the wire and have the agent reject it at the far end.
func TestWriteTargetRefusesAnInvalidTarget(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteTarget(&buf, "no-port-here"); err == nil {
		t.Fatal("want an error")
	}
	if buf.Len() != 0 {
		t.Errorf("wrote %q despite the error", buf.String())
	}
}
