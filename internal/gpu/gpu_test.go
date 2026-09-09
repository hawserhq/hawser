package gpu

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestCDISpecIsValidYAMLWithRequiredFields guards the embedded spec: dockerd
// rejects a malformed CDI file, so a typo here would break GPU passthrough
// silently. This parses it and checks the fields dockerd requires.
func TestCDISpecIsValidYAMLWithRequiredFields(t *testing.T) {
	var spec struct {
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
	if err := yaml.Unmarshal(CDISpec(), &spec); err != nil {
		t.Fatalf("CDI spec is not valid YAML: %v", err)
	}

	if spec.CDIVersion == "" {
		t.Error("cdiVersion is required")
	}
	if spec.Kind != "nvidia.com/gpu" {
		t.Errorf("kind = %q, want nvidia.com/gpu", spec.Kind)
	}
	if len(spec.Devices) != 1 || spec.Devices[0].Name != "all" {
		t.Fatalf("expected a single device named 'all', got %+v", spec.Devices)
	}
	if len(spec.Devices[0].ContainerEdits.DeviceNodes) == 0 ||
		spec.Devices[0].ContainerEdits.DeviceNodes[0].Path != DxgDevice {
		t.Errorf("device must inject %s", DxgDevice)
	}

	// The hookless design is the whole point on musl: no hooks, and the loader
	// path set via env instead.
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

	// The WSL driver tree must be mounted.
	mountsWSL := false
	for _, m := range spec.ContainerEdits.Mounts {
		if m.HostPath == "/usr/lib/wsl" {
			mountsWSL = true
		}
	}
	if !mountsWSL {
		t.Error("spec must rbind /usr/lib/wsl into the container")
	}
}
