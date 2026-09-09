// Package gpu enables NVIDIA GPU access for containers in the engine distro
// (#83): `docker run --device nvidia.com/gpu=all` reaches the GPU.
//
// WSL2 already does the hard part — the Windows NVIDIA driver projects the CUDA
// libraries into every WSL2 distro at /usr/lib/wsl/lib and exposes the GPU
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
package gpu

// CDISpecPath is where the engine's CDI spec lives inside the distro. /etc/cdi
// is one of dockerd's default cdi-spec-dirs and, unlike /var/run/cdi, it
// survives a distro restart.
const CDISpecPath = "/etc/cdi/nvidia.yaml"

// WSLLibDir is where WSL projects the Windows NVIDIA driver's Linux libraries.
const WSLLibDir = "/usr/lib/wsl/lib"

// DxgDevice is the GPU paravirtualization kernel interface WSL exposes.
const DxgDevice = "/dev/dxg"

// ProbeLib is a library that is present iff the WSL GPU projection is active for
// an NVIDIA GPU — the cheap gate for "can this distro see the GPU at all".
const ProbeLib = WSLLibDir + "/libcuda.so.1"

// cdiSpec is the hookless WSL CDI spec. cdiVersion 0.5.0 is the first that
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
const cdiSpec = `cdiVersion: "0.5.0"
kind: "nvidia.com/gpu"
devices:
  - name: all
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

// CDISpec returns the CDI spec to write into the distro at CDISpecPath.
func CDISpec() []byte { return []byte(cdiSpec) }
