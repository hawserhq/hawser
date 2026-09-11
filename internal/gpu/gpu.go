// Package gpu enables GPU access for containers in the engine distro (#83,
// #185): `docker run --device nvidia.com/gpu=all` reaches the GPU.
//
// WSL2 already does the hard part — the Windows driver projects its Linux
// userspace into every WSL2 distro at /usr/lib/wsl/lib and exposes the GPU
// kernel interface at /dev/dxg — so nothing is installed into the distro's
// userland. What was missing is telling the engine to inject those into
// containers. We do that with a Container Device Interface (CDI) spec, which
// dockerd supports natively (on by default since Docker 28.3.0).
//
// The spec is deliberately HOOKLESS. The usual NVIDIA WSL spec runs
// nvidia-cdi-hook (a glibc-oriented binary that also breaks the ld cache inside
// musl containers, and would have to be built and shipped). Instead we rbind the
// whole /usr/lib/wsl tree and set LD_LIBRARY_PATH=/usr/lib/wsl/lib — which BOTH
// glibc and musl containers honor — so no hook, no libnvidia-container, and no
// binary to build for Alpine. That is what makes this work on a musl engine.
//
// # Vendors
//
// The mechanism is not NVIDIA-specific: /dev/dxg is Microsoft's DXCore
// interface, not CUDA, and AMD's ROCm-on-WSL reaches the GPU the same way
// (ROCDXG over /dev/dxg, with libdxcore.so projected into the same
// /usr/lib/wsl/lib). So the AMD spec is this one with a different kind and
// without the nvidia-smi convenience bind.
//
// AMD support is EXPERIMENTAL and has never been run against real hardware —
// it is derived from AMD's documentation, not from observation. It is opt-in
// by name (`enable-gpu --vendor amd`) precisely so that nothing claims to work
// until somebody with a Radeon reports back.
package gpu

import "fmt"

// WSLLibDir is where WSL projects the Windows driver's Linux libraries.
const WSLLibDir = "/usr/lib/wsl/lib"

// DxgDevice is the GPU paravirtualization kernel interface WSL exposes. It is
// DXCore, so it is the same node for every vendor.
const DxgDevice = "/dev/dxg"

// Vendor is the GPU vendor a CDI spec targets.
type Vendor string

const (
	// NVIDIA is the default and the only vendor validated on hardware.
	NVIDIA Vendor = "nvidia"
	// AMD is experimental: written from AMD's ROCm-on-WSL documentation and
	// not yet run against a Radeon (#185).
	AMD Vendor = "amd"
)

// DefaultVendor is what an install assumes when nothing says otherwise, which
// keeps every pre-#185 `gpu on` install working unchanged.
const DefaultVendor = NVIDIA

// Vendors lists the supported vendors, for help text and validation messages.
func Vendors() []Vendor { return []Vendor{NVIDIA, AMD} }

// ParseVendor validates a vendor name from a flag or the config file.
func ParseVendor(s string) (Vendor, error) {
	switch Vendor(s) {
	case NVIDIA:
		return NVIDIA, nil
	case AMD:
		return AMD, nil
	case "":
		return DefaultVendor, nil
	}
	return "", fmt.Errorf("unknown GPU vendor %q (known: nvidia, amd)", s)
}

// Experimental reports whether this vendor is unvalidated, so callers can say
// so rather than implying it is supported.
func (v Vendor) Experimental() bool { return v == AMD }

// Kind is the CDI device kind a container selects with `--device <kind>=all`.
func (v Vendor) Kind() string {
	if v == AMD {
		return "amd.com/gpu"
	}
	return "nvidia.com/gpu"
}

// SpecPath is where this vendor's CDI spec lives inside the distro. /etc/cdi is
// one of dockerd's default cdi-spec-dirs and, unlike /var/run/cdi, it survives a
// distro restart. Each vendor gets its own file so switching removes cleanly.
func (v Vendor) SpecPath() string {
	if v == AMD {
		return "/etc/cdi/amd.yaml"
	}
	return "/etc/cdi/nvidia.yaml"
}

// ProbeLib is a library present iff the WSL GPU projection is live — the cheap
// gate for "can this distro see a GPU at all".
//
// For NVIDIA this is also a vendor test: libcuda.so.1 is projected only by the
// NVIDIA driver. For AMD it is NOT. The library AMD's WSL path needs is
// libdxcore.so, which is Microsoft's DXCore shim and is projected on NVIDIA
// machines too, and AMD documents no vendor-specific marker in this directory.
// So the AMD probe answers "a WSL GPU projection exists", and the vendor itself
// is asserted by the user passing --vendor amd. Guessing would be worse: a
// silent false positive on an NVIDIA machine.
func (v Vendor) ProbeLib() string {
	if v == AMD {
		return WSLLibDir + "/libdxcore.so"
	}
	return WSLLibDir + "/libcuda.so.1"
}

// Spec returns the CDI spec to write into the distro at SpecPath.
func (v Vendor) Spec() []byte {
	if v == AMD {
		return []byte(amdSpec)
	}
	return []byte(nvidiaSpec)
}

// nvidiaSpec is the hookless WSL CDI spec. cdiVersion 0.5.0 is the first that
// carries container env edits, which is how LD_LIBRARY_PATH is set; Docker 28.3+
// supports well beyond it. The device is named "all"; a client selects it with
// `--device nvidia.com/gpu=all`.
//
// Rationale for each edit:
//   - deviceNode /dev/dxg — the GPU kernel interface, with its cgroup rule.
//   - rbind /usr/lib/wsl — libcuda.so.1, libnvidia-ml.so.1, libdxcore.so, the
//     driver store under drivers/, and nvidia-smi. Recursive because WSL mounts
//     lib/ as its own mount; a plain bind would miss it. Read-only.
//   - LD_LIBRARY_PATH=/usr/lib/wsl/lib — so the loader finds those libraries
//     without an ldconfig hook. musl and glibc both honor it.
//   - nvidia-smi onto /usr/bin so `docker run ... nvidia-smi` just works.
const nvidiaSpec = `cdiVersion: "0.5.0"
kind: "nvidia.com/gpu"
devices:
  - name: all
    containerEdits:
      deviceNodes:
        - path: /dev/dxg
  # "0" aliases the same (single) WSL GPU so ` + "`--gpus device=0`" + ` and count-style
  # requests resolve too; WSL exposes one /dev/dxg regardless of GPU count.
  - name: "0"
    containerEdits:
      deviceNodes:
        - path: /dev/dxg
containerEdits:
  env:
    - LD_LIBRARY_PATH=/usr/lib/wsl/lib
  mounts:
    - hostPath: /usr/lib/wsl
      containerPath: /usr/lib/wsl
      options: [ro, nosuid, nodev, rbind]
    - hostPath: /usr/lib/wsl/lib/nvidia-smi
      containerPath: /usr/bin/nvidia-smi
      options: [ro, nosuid, nodev, bind]
`

// amdSpec is the NVIDIA spec with the vendor-specific parts removed, which is
// the whole finding behind #185: AMD's ROCm-on-WSL uses the same /dev/dxg and
// the same /usr/lib/wsl/lib projection, so the same two edits are what AMD's
// own documentation tells users to pass to `docker run` by hand.
//
// Two differences from the NVIDIA spec:
//   - no nvidia-smi bind. There is no projected AMD equivalent; rocm-smi ships
//     inside the ROCm image.
//   - the ROCm userspace (librocdxg.so, /opt/rocm) comes from the IMAGE, not
//     from the host. AMD states ROCDXG needs no Radeon Software for Linux
//     packages in the distro, which is what keeps the musl engine irrelevant
//     here — nothing is installed in it either way.
//
// Use a ROCm image: the container must supply ROCm, and AMD supports Ubuntu
// 24.04/22.04, so a musl image will not work no matter what this spec mounts.
const amdSpec = `cdiVersion: "0.5.0"
kind: "amd.com/gpu"
devices:
  - name: all
    containerEdits:
      deviceNodes:
        - path: /dev/dxg
  # "0" aliases the same (single) WSL GPU, as on the NVIDIA side; WSL exposes
  # one /dev/dxg regardless of GPU count, and AMD does not support multi-GPU
  # under WSL at all.
  - name: "0"
    containerEdits:
      deviceNodes:
        - path: /dev/dxg
containerEdits:
  env:
    - LD_LIBRARY_PATH=/usr/lib/wsl/lib
  mounts:
    - hostPath: /usr/lib/wsl
      containerPath: /usr/lib/wsl
      options: [ro, nosuid, nodev, rbind]
`
