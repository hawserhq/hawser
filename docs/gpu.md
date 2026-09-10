# NVIDIA GPU access

Run CUDA workloads — Ollama, vLLM, PyTorch, `nvidia-smi` — in containers on the
Hawser engine:

```
hawser enable-gpu
docker run --rm --device nvidia.com/gpu=all nvidia/cuda:12.4.1-base-ubuntu22.04 nvidia-smi
```

WSL2 already does the hard part: the Windows NVIDIA driver projects the CUDA
libraries into every WSL2 distro at `/usr/lib/wsl/lib` and exposes the GPU at
`/dev/dxg`. `hawser enable-gpu` installs a **Container Device Interface (CDI)**
spec so the engine injects those into containers. dockerd supports CDI natively
(on by default since Docker 28.3.0), so nothing else is configured.

## Requirements

- An **NVIDIA** GPU with a recent, WSL-capable Windows driver (`nvidia-smi` works
  in a WSL distro). AMD and Intel GPUs expose compute to WSL differently and are
  not wired up here.
- WSL up to date (`wsl --update`) and the Hawser engine installed.

`hawser enable-gpu` checks that the distro actually sees the GPU (`/dev/dxg` and
the WSL CUDA library) before enabling, and tells you which of driver/WSL/GPU is
missing if not.

## The invocation

Use the CDI device form:

```
docker run --rm --device nvidia.com/gpu=all <image> <cmd>
```

Compose:

```yaml
services:
  app:
    image: nvidia/cuda:12.4.1-base-ubuntu22.04
    deploy:
      resources:
        reservations:
          devices:
            - driver: cdi
              device_ids: ["nvidia.com/gpu=all"]
```

### `--gpus all` too — on rootfs 29.7.2-4 and later

`docker run --gpus all` (and VS Code Dev Containers' `"hostRequirements":
{"gpu": true}`, which passes `--gpus all`) also reaches the GPU, with one
condition: the engine must find an `nvidia-cdi-hook` binary **when dockerd
starts** — that is what makes moby register its NVIDIA GPU driver and route
`--gpus` to the CDI spec instead of the legacy runtime hook. Rootfs 29.7.2-4
and later ship it; `hawser enable-gpu` tells you which spelling your rootfs
supports. On an older rootfs `--gpus all` reports "AMD CDI spec not found" —
use `--device nvidia.com/gpu=all`, which works everywhere.

Two honest notes about that binary. It is the one glibc program in the
otherwise-musl rootfs (NVIDIA's `go-nvml` does not build on musl), built as a
static binary from the pinned toolkit tag; and it is **never executed** for GPU
injection — the spec is hookless — it only has to exist. `--gpus device=0` also
works: the spec names the single WSL GPU both `all` and `0`.

## Why this works on the musl engine

The Hawser engine is Alpine (musl libc). The usual NVIDIA container stack targets
glibc — `libnvidia-container` does not build for musl, and the standard WSL CDI
spec runs an `ldconfig` hook that also breaks inside musl. Hawser's spec is
**hookless**: it rbind-mounts `/usr/lib/wsl` into the container and sets
`LD_LIBRARY_PATH=/usr/lib/wsl/lib` — which both glibc and musl containers honor —
so the driver libraries are found with no hook, no `libnvidia-container`, and no
binary to build for Alpine.

Prefer **glibc** CUDA base images (`nvidia/cuda`, `ubuntu`); they are the
best-tested path. musl (Alpine) containers can find the libraries too via
`LD_LIBRARY_PATH`, but CUDA userspace on musl is its own adventure.

## Persistence and turning it off

The setting persists: the CDI spec is re-applied on every engine start, so a
reinstall keeps GPU access. dockerd reads CDI specs dynamically, so
`hawser enable-gpu` takes effect on the next `docker run` — no restart.

```
hawser enable-gpu --off      # remove the spec
hawser doctor                # reports GPU visible / enabled / spec installed
```
