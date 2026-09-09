package vpnfingerprint

import "testing"

func TestDetectGlobalProtectByDescription(t *testing.T) {
	// The connection name is user-renamed; the description is what identifies it.
	got := Detect([]Adapter{
		{Name: "Ethernet 4", Description: "PANGP Virtual Ethernet Adapter Secure", Up: true},
		{Name: "Wi-Fi", Description: "Intel Wireless-AC 9560", Up: true},
	})
	if len(got) != 1 {
		t.Fatalf("want 1 match, got %d: %+v", len(got), got)
	}
	if got[0].Name != "Palo Alto GlobalProtect" {
		t.Errorf("matched %q, want GlobalProtect", got[0].Name)
	}
	if got[0].Adapter.Name != "Ethernet 4" {
		t.Errorf("reported adapter %q, want the matching one", got[0].Adapter.Name)
	}
	if got[0].MTU != 1400 {
		t.Errorf("MTU = %d, want 1400", got[0].MTU)
	}
	if got[0].WSLConfig["networkingMode"] != "mirrored" {
		t.Errorf("expected mirrored networking recommendation")
	}
}

func TestDetectIgnoresDownAdapters(t *testing.T) {
	// A VPN adapter that exists but is not Up is not the current path.
	got := Detect([]Adapter{
		{Name: "Ethernet 4", Description: "Cisco AnyConnect Virtual Miniport Adapter", Up: false},
	})
	if len(got) != 0 {
		t.Fatalf("a down VPN adapter must not be detected: %+v", got)
	}
}

func TestDetectMatchesOnNameWhenDescriptionBlank(t *testing.T) {
	got := Detect([]Adapter{
		{Name: "ZScaler Network Adapter", Description: "", Up: true},
	})
	if len(got) != 1 || got[0].Name != "Zscaler" {
		t.Fatalf("want Zscaler via name, got %+v", got)
	}
}

func TestDetectNoVPN(t *testing.T) {
	got := Detect([]Adapter{
		{Name: "Ethernet", Description: "Realtek PCIe GbE Family Controller", Up: true},
		{Name: "vEthernet (WSL)", Description: "Hyper-V Virtual Ethernet Adapter", Up: true},
	})
	if len(got) != 0 {
		t.Fatalf("no VPN present, got %+v", got)
	}
}

func TestDetectMultipleVPNs(t *testing.T) {
	got := Detect([]Adapter{
		{Name: "GP", Description: "PANGP Virtual Ethernet Adapter", Up: true},
		{Name: "ZS", Description: "Zscaler Network Adapter", Up: true},
	})
	if len(got) != 2 {
		t.Fatalf("want both VPNs, got %d: %+v", len(got), got)
	}
}

func TestDetectDeduplicatesPerFingerprint(t *testing.T) {
	// Two adapters both matching GlobalProtect yield one match, not two.
	got := Detect([]Adapter{
		{Name: "a", Description: "PANGP Virtual Ethernet Adapter", Up: true},
		{Name: "b", Description: "GlobalProtect helper", Up: true},
	})
	if len(got) != 1 {
		t.Fatalf("want a single GlobalProtect match, got %d: %+v", len(got), got)
	}
}

func TestDBWellFormed(t *testing.T) {
	for _, fp := range DB() {
		if fp.Name == "" {
			t.Error("fingerprint with empty name")
		}
		if len(fp.Match) == 0 {
			t.Errorf("%s has no match substrings", fp.Name)
		}
		for _, m := range fp.Match {
			if m == "" {
				t.Errorf("%s has an empty match substring", fp.Name)
			}
		}
		if fp.Note == "" {
			t.Errorf("%s has no explanatory note", fp.Name)
		}
	}
}
