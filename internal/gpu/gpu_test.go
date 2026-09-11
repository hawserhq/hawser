package gpu

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type cdiFile struct {
	CDIVersion string `yaml:"cdiVersion"`
	Kind       string `yaml:"kind"`
	Devices    []struct {
		Name           string `yaml:"name"`
		ContainerEdits struct {
			DeviceNodes []struct {
				Path string `yaml:"path"`
			} `yaml:"deviceNodes"`
		} `yaml:"containerEdits"`
	} `yaml:"devices"`
	ContainerEdits struct {
		Env    []string `yaml:"env"`
		Mounts []struct {
			HostPath      string   `yaml:"hostPath"`
			ContainerPath string   `yaml:"containerPath"`
			Options       []string `yaml:"options"`
		} `yaml:"mounts"`
		Hooks []any `yaml:"hooks"`
	} `yaml:"containerEdits"`
}

// TestEveryVendorSpecIsValid guards the embedded specs: dockerd rejects a
// malformed CDI file, so a typo here would break GPU passthrough silently.
//
// It runs over every vendor deliberately. The AMD spec cannot be validated on
// hardware by anyone working on this repo (#185), so this structural check is
// the only guarantee it has — it should therefore be at least as strict as the
// one the NVIDIA spec gets, not a lighter version of it.
func TestEveryVendorSpecIsValid(t *testing.T) {
	for _, v := range Vendors() {
		t.Run(string(v), func(t *testing.T) {
			var spec cdiFile
			if err := yaml.Unmarshal(v.Spec(), &spec); err != nil {
				t.Fatalf("CDI spec is not valid YAML: %v", err)
			}

			if spec.CDIVersion == "" {
				t.Error("cdiVersion is required")
			}
			if spec.Kind != v.Kind() {
				t.Errorf("kind = %q, want %q", spec.Kind, v.Kind())
			}

			// "all" is the documented device; "0" aliases it so count-style
			// requests resolve (#139). Every device must inject /dev/dxg —
			// which is DXCore, hence the same node for both vendors.
			names := map[string]bool{}
			for _, d := range spec.Devices {
				names[d.Name] = true
				if len(d.ContainerEdits.DeviceNodes) == 0 || d.ContainerEdits.DeviceNodes[0].Path != DxgDevice {
					t.Errorf("device %q must inject %s", d.Name, DxgDevice)
				}
			}
			for _, want := range []string{"all", "0"} {
				if !names[want] {
					t.Errorf("spec is missing device %q (have %v)", want, names)
				}
			}

			// The hookless design is the whole point on musl: no hooks, and
			// the loader path set via env instead.
			if len(spec.ContainerEdits.Hooks) != 0 {
				t.Errorf("spec must be hookless (musl safety); found hooks: %+v", spec.ContainerEdits.Hooks)
			}
			foundLD := false
			for _, e := range spec.ContainerEdits.Env {
				if strings.HasPrefix(e, "LD_LIBRARY_PATH=") && strings.Contains(e, WSLLibDir) {
					foundLD = true
				}
			}
			if !foundLD {
				t.Errorf("spec must set LD_LIBRARY_PATH to %s so containers find the driver libs", WSLLibDir)
			}

			// The WSL driver tree must be mounted: it carries libcuda.so.1 for
			// NVIDIA and libdxcore.so for AMD.
			mountsWSL := false
			for _, m := range spec.ContainerEdits.Mounts {
				if m.HostPath == "/usr/lib/wsl" {
					mountsWSL = true
				}
			}
			if !mountsWSL {
				t.Error("spec must rbind /usr/lib/wsl into the container")
			}
		})
	}
}

func TestNvidiaSpecBindsNvidiaSmi(t *testing.T) {
	// The convenience that makes `docker run ... nvidia-smi` work. AMD has no
	// projected equivalent, which is asserted separately below.
	var spec cdiFile
	if err := yaml.Unmarshal(NVIDIA.Spec(), &spec); err != nil {
		t.Fatal(err)
	}
	for _, m := range spec.ContainerEdits.Mounts {
		if m.ContainerPath == "/usr/bin/nvidia-smi" {
			return
		}
	}
	t.Error("the nvidia spec should bind nvidia-smi into the container")
}

func TestAMDSpecBindsNothingNvidia(t *testing.T) {
	// A copy-paste of the NVIDIA spec would bind /usr/lib/wsl/lib/nvidia-smi,
	// which does not exist on an AMD machine — the mount would fail and take
	// every container with it.
	if s := string(AMD.Spec()); strings.Contains(s, "nvidia") {
		t.Errorf("the amd spec must not reference nvidia:\n%s", s)
	}
}

func TestVendorIdentities(t *testing.T) {
	cases := []struct {
		v                     Vendor
		kind, specPath, probe string
		experimental          bool
	}{
		{NVIDIA, "nvidia.com/gpu", "/etc/cdi/nvidia.yaml", WSLLibDir + "/libcuda.so.1", false},
		{AMD, "amd.com/gpu", "/etc/cdi/amd.yaml", WSLLibDir + "/libdxcore.so", true},
	}
	for _, c := range cases {
		t.Run(string(c.v), func(t *testing.T) {
			if got := c.v.Kind(); got != c.kind {
				t.Errorf("Kind() = %q, want %q", got, c.kind)
			}
			// Separate files matter: switching vendors has to be able to
			// remove the one it is not using.
			if got := c.v.SpecPath(); got != c.specPath {
				t.Errorf("SpecPath() = %q, want %q", got, c.specPath)
			}
			if got := c.v.ProbeLib(); got != c.probe {
				t.Errorf("ProbeLib() = %q, want %q", got, c.probe)
			}
			if got := c.v.Experimental(); got != c.experimental {
				t.Errorf("Experimental() = %v, want %v", got, c.experimental)
			}
		})
	}
}

func TestParseVendor(t *testing.T) {
	for in, want := range map[string]Vendor{
		"nvidia": NVIDIA,
		"amd":    AMD,
		// Empty is how an install that predates #185 reads: it must stay
		// NVIDIA, or `gpu on` would silently change meaning on upgrade.
		"": DefaultVendor,
	} {
		got, err := ParseVendor(in)
		if err != nil {
			t.Errorf("ParseVendor(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("ParseVendor(%q) = %q, want %q", in, got, want)
		}
	}

	for _, bad := range []string{"intel", "NVIDIA ", "rocm", "cuda"} {
		if _, err := ParseVendor(bad); err == nil {
			t.Errorf("ParseVendor(%q) should be refused", bad)
		}
	}
}
