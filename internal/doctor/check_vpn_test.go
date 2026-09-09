package doctor

import (
	"strings"
	"testing"

	"github.com/zcsizmadia/hawser/internal/vpnfingerprint"
)

func TestCheckVPNNoneDetected(t *testing.T) {
	got := checkVPN().Run(Facts{})
	if got.Status != Skip {
		t.Fatalf("status = %v, want Skip when no VPN", got.Status)
	}
}

func TestCheckVPNWarnsWithGuidance(t *testing.T) {
	f := Facts{VPNs: []vpnfingerprint.Match{{
		Fingerprint: vpnfingerprint.Fingerprint{
			Name:      "Palo Alto GlobalProtect",
			MTU:       1400,
			DNS:       []string{"1.1.1.1"},
			WSLConfig: map[string]string{"networkingMode": "mirrored"},
			Note:      "clamps MTU",
		},
		Adapter: vpnfingerprint.Adapter{Name: "Ethernet 4", Description: "PANGP", Up: true},
	}}}
	got := checkVPN().Run(f)
	if got.Status != Warn {
		t.Fatalf("status = %v, want Warn", got.Status)
	}
	if !strings.Contains(got.Summary, "GlobalProtect") {
		t.Errorf("summary should name the VPN: %q", got.Summary)
	}
	joined := strings.Join(got.Detail, "\n")
	if !strings.Contains(joined, "1400") {
		t.Errorf("detail should carry the recommended MTU:\n%s", joined)
	}
	if !strings.Contains(got.Remedy, "networkingMode=mirrored") {
		t.Errorf("remedy should show the .wslconfig key:\n%s", got.Remedy)
	}
}

func TestParseAdaptersArray(t *testing.T) {
	js := []byte(`[{"Name":"Ethernet 4","InterfaceDescription":"PANGP Virtual Ethernet Adapter","Status":"Up"},
	                {"Name":"Wi-Fi","InterfaceDescription":"Intel AX201","Status":"Disconnected"}]`)
	got := parseAdapters(js)
	if len(got) != 2 {
		t.Fatalf("want 2 adapters, got %d", len(got))
	}
	if !got[0].Up || got[1].Up {
		t.Errorf("Up flags wrong: %+v", got)
	}
	if got[0].Description != "PANGP Virtual Ethernet Adapter" {
		t.Errorf("description not parsed: %q", got[0].Description)
	}
}

func TestParseAdaptersSingleObject(t *testing.T) {
	// ConvertTo-Json emits a bare object when there is exactly one adapter.
	js := []byte(`{"Name":"Ethernet","InterfaceDescription":"Realtek","Status":"Up"}`)
	got := parseAdapters(js)
	if len(got) != 1 || got[0].Name != "Ethernet" || !got[0].Up {
		t.Fatalf("single-object parse wrong: %+v", got)
	}
}

func TestParseAdaptersGarbage(t *testing.T) {
	if got := parseAdapters([]byte("not json")); got != nil {
		t.Errorf("garbage should parse to nil, got %+v", got)
	}
	if got := parseAdapters(nil); got != nil {
		t.Errorf("nil should parse to nil, got %+v", got)
	}
}

// End to end through Detect: adapters JSON with a VPN yields a detected match.
func TestParseThenDetect(t *testing.T) {
	js := []byte(`[{"Name":"vpn0","InterfaceDescription":"Cisco AnyConnect Virtual Miniport Adapter","Status":"Up"}]`)
	got := vpnfingerprint.Detect(parseAdapters(js))
	if len(got) != 1 || !strings.Contains(got[0].Name, "AnyConnect") {
		t.Fatalf("want AnyConnect detected, got %+v", got)
	}
}
