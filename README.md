<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/skrog-mark-ondark.svg">
    <img alt="Skrog" src="assets/skrog-mark.svg" width="120" height="120">
  </picture>
</p>

<h1 align="center">Skrog</h1>

<p align="center"><em><strong>skrog</strong> (n., Norwegian) — the hull: the body of the ship that carries the cargo and keeps the sea out.</em></p>

<p align="center">
  <a href="https://github.com/wslkit/skrog/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/wslkit/skrog/actions/workflows/ci.yml/badge.svg?branch=main"></a>
  <a href="https://github.com/wslkit/skrog/releases"><img alt="Latest release" src="https://img.shields.io/github/v/release/wslkit/skrog?include_prereleases&sort=semver&label=release&color=0a7d84"></a>
  <a href="LICENSE"><img alt="License: Apache-2.0" src="https://img.shields.io/github/license/wslkit/skrog?color=2F3B45"></a>
  <img alt="Go" src="https://img.shields.io/github/go-mod/go-version/wslkit/skrog?color=00ADD8">
  <img alt="Platform: Windows 11 + WSL2" src="https://img.shields.io/badge/platform-Windows%2011%20%2B%20WSL2-2F3B45">
</p>

A minimal, invisible way to run the upstream open source **Docker Engine on Windows** via WSL2.
No license fees, no Electron, no Kubernetes — install once, `docker ps` works forever, on
laptops and CI runners alike.

**Status: v0.3 pre-release.** Installable and working as a daily driver: install once and
the engine starts at every logon, heals itself, and answers `docker` at the same speed as
Docker Desktop. v0.3 adds `skrog doctor`, validated engine settings, lifecycle hooks, and
declarative installs, corporate-network/VPN support, and a bundled docker CLI so you can
drop Docker Desktop entirely. Read
[PLAN.md](PLAN.md) for the strategy and [ROADMAP.md](ROADMAP.md) for the schedule; the
[issue tracker](https://github.com/wslkit/skrog/issues) is the live state.

## Install

Requirements: Windows 11 with WSL2, and a `docker` CLI. Docker Desktop's works (Skrog
coexists with it), or install Skrog's own bundled CLI with `skrog cli install` and drop
Docker Desktop entirely — see [docs/docker-cli.md](docs/docker-cli.md).

```powershell
irm https://wslkit.github.io/skrog/install.ps1 | iex
```

That downloads the newest release, **verifies it against the release's `SHA256SUMS`**,
unpacks it to `%LOCALAPPDATA%\Programs\skrog` and adds it to your PATH. It stops there
on purpose — it does not provision anything. Then:

1. `skrog install` — downloads the checksum-verified engine rootfs, imports it as the
   `skrog-engine` WSL2 distro, starts the engine, wires a `skrog` docker context, and
   registers the supervisor to start at logon (`--no-autostart` opts out)
2. `skrog start` — brings up the always-on bridge now (from your next logon it starts
   itself)
3. `docker --context skrog run --rm hello-world`

Binaries are not signed yet, so SmartScreen will warn on first run. Prefer to do it by
hand? Download the zip for your architecture from the
[latest release](https://github.com/wslkit/skrog/releases), check it against
`SHA256SUMS`, and unpack it anywhere on your PATH — the script does nothing else.
[Read it first](scripts/install.ps1) if you would rather not pipe a URL into your shell;
it is the same file that URL serves.

On a CI runner, use [setup-skrog](https://github.com/wslkit/setup-skrog) instead.

`skrog.exe uninstall` removes everything Skrog created — the distro and all images and
volumes in it, the autostart entry, any distro integrations — and restores your previous
docker context. Nothing else on the system is touched.

## What it does today (v0.3)

- Upstream Docker Engine (Linux containers) in a dedicated WSL2 distro — the real API, byte
  for byte: compose, buildx, Testcontainers, `run -it`, bind mounts with Windows paths
- **Always-on supervisor**: starts at logon, survives engine crashes, `wsl --shutdown`, and
  sleep/resume; `skrog start/stop/restart/status --json`. Settings apply live — the
  supervisor follows the file, so nothing here needs a restart; `skrog restart
  --supervisor` replaces the supervisor process itself on the rare occasion that helps
- **Docker Desktop speed**: a vsock transport to the engine (~80 ms `docker version`,
  measured at parity with Desktop), with an automatic fallback path
- **Idle RAM answer**: `skrog config set idle-timeout 30m` stops a quiet engine and
  cold-starts it (~1 s engine start) on your next `docker` command
- **`skrog doctor`**: diagnoses the WSL / PATH / credential-helper / supervisor quirk zoo,
  with `--json`, `--report` (paste straight into an issue), and `--fix` for the safe subset;
  recognizes corporate VPNs (GlobalProtect, AnyConnect, Zscaler…) and prints the MTU/DNS fix
  ([docs/vpn.md](docs/vpn.md))
- **Validated engine settings**: `skrog config set engine.<key>` edits the engine's
  `daemon.json` (registry mirrors, logging, DNS…), checked with `dockerd --validate` before
  it applies and rolled back if the engine will not come back
- **Lifecycle hooks**: run your own script on post-start / pre-stop / on-idle-stop / on-wake
  ([docs/hooks.md](docs/hooks.md))
- **Declarative installs**: `skrog install --config skrog.yaml` (idempotent) and
  `skrog config export` — infrastructure-as-code for a fleet
  ([docs/declarative-install.md](docs/declarative-install.md))
- **NVIDIA GPU**: `skrog enable-gpu`, then `docker run --device nvidia.com/gpu=all …` runs
  CUDA workloads (Ollama, vLLM, PyTorch) — a hookless CDI spec that works on the musl engine,
  no toolkit installed ([docs/gpu.md](docs/gpu.md))
- **Bundled docker CLI**: `skrog cli install` installs the upstream docker CLI + compose +
  buildx + credential helper — checksum-pinned, nothing fetched as "latest" — so you can
  uninstall Docker Desktop entirely ([docs/docker-cli.md](docs/docker-cli.md))
- **Remote engine over mutual TLS**: `skrog serve --tcp` exposes the engine to a
  teammate or CI runner, reachable only by holders of a client cert this machine's CA
  signed — off by default; on the client, `skrog remote add/use` makes it docker's default
  in one command ([docs/remote-engine.md](docs/remote-engine.md))
- **`skrog status --stats`**: container/image/volume counts and reclaimable space, the
  VHDX footprint, VM memory and CPUs (configured versus actual), engine and supervisor
  uptime with idle-stop history, and bridge counters including **which transport is live**
  — the one number that explains a slow `docker` with a healthy engine
- **Right-size the VM with consent**: `skrog config set wsl.memory 4GB` then
  `skrog wsl-config apply` shows the diff to the GLOBAL ~/.wslconfig and writes only on
  a yes (`--yes` for runners, idempotent) — [docs/vm-sizing.md](docs/vm-sizing.md)
- **Engine upgrades, reversibly**: `skrog engine upgrade` swaps the engine binaries out of a
  checksum-verified rootfs and leaves /var/lib/docker alone, so images and volumes survive;
  a new engine that does not come back is rolled back automatically
  ([docs/engine-upgrade.md](docs/engine-upgrade.md))
- **Disk hygiene**: `skrog prune` reclaims stopped containers, unused images and build cache
  through whatever docker targets; **`skrog compact`** then shrinks the engine's VHDX itself
  (`fstrim` + `CompactVirtualDisk`, no administrator rights, so it works on Windows Home);
  `skrog doctor` warns below a configurable free-space floor
  ([docs/housekeeping.md](docs/housekeeping.md))
- **`skrog wsl-integrate <distro>`**: use the engine from inside your own WSL distros
- **`skrog migrate --from-desktop`**: copy images and volumes out of Docker Desktop,
  non-destructively and resumably (`--dry-run` first)
- Optional status-light tray (`skrogtray.exe`) — six menu items, forever
- Headless CI installs (`--headless`, exit codes, `--json` on every state-reporting command —
  the contract in [docs/cli-json.md](docs/cli-json.md)), `skrog healthcheck --wait` as a
  runner readiness probe, `skrog logs --json` for log shippers, `skrog prewarm images.txt`
  to pre-pull a pinned image list, version pinning as a contract (nothing fetches "latest"),
  no telemetry
- A logged-on session is required — a WSL2 platform constraint that binds every WSL-based
  engine; for CI runners see [docs/auto-logon-runner.md](docs/auto-logon-runner.md), and
  `skrog runner check` verifies the setup in one verdict
- The supervisor is watched: if it ever dies, `skrogw.exe` restarts it in about a second
  (backing off, with a crash budget) instead of leaving every `docker` command broken until
  the next `skrog start`
- **CI runners**: GitHub Actions via
  [setup-skrog](https://github.com/wslkit/setup-skrog), GitLab shell or docker
  executor, Testcontainers (Ryuk included) — [docs/ci-runners.md](docs/ci-runners.md)
- **Debug pipelines locally, against the engine your runner uses**: `act`,
  `gitlab-ci-local`, Dagger, and a BuildKit cache a laptop and a runner share; commit
  `skrog.lock` and both install the same engine to the commit —
  [docs/local-ci.md](docs/local-ci.md)
- **Kubernetes when you want it, never bundled**: `kind` and `k3d` clusters run on the
  engine, with `kubectl` and NodePort/Ingress reachable from Windows —
  [docs/kubernetes.md](docs/kubernetes.md)

## What's ahead

Signed installers (winget/scoop/choco) — tracked in the
[issue tracker](https://github.com/wslkit/skrog/issues). Data-dir relocation
shipped: see [`skrog relocate`](docs/housekeeping.md).

## What it will never be

Windows containers, Kubernetes, or a management GUI. Because Skrog serves the standard
Docker API, existing frontends (Portainer, lazydocker, VS Code) already work against it —
and a cluster is just containers, so `kind` and `k3d` run on it today
([docs/kubernetes.md](docs/kubernetes.md)) without Skrog owning a control plane.

## Repository layout

Standard Go project layout — the Go toolchain, not a framework, decides this shape:

```
cmd/skrog/     the CLI — the product; every capability lives here
cmd/skrogw/    windowless logon launcher and supervisor watchdog (no console flash)
cmd/skrogtray/ optional status-light tray; shells out to the CLI, holds no logic
internal/       implementation packages, compiler-enforced private to this module
  wsl/          every wsl.exe call, behind an interface so tests run anywhere
  ...           provision, pipeproxy, supervise, config, migrate, integrate,
                doctor, engineconfig, skrogfile, tray
guest/          Linux side: rootfs build scripts, vsock agent
docs/           operator docs, e.g. the unattended/auto-logon runner playbook
site/           the docs site: layouts and nav only -- content comes from docs/
test/e2e/       cross-package suite; the only part needing real WSL2
spike/          throwaway experiments, deleted once their issue closes
```

Running unattended (CI runners, build agents) needs a logged-on session, because
WSL2 cannot start from a Windows service — see
[docs/auto-logon-runner.md](docs/auto-logon-runner.md). Who can reach the engine
and where the trust boundaries lie (the pipe ACL, `wsl-integrate`'s shared
socket, the supply chain) is documented in [docs/security.md](docs/security.md).

Two Go conventions worth stating, since they surprise people arriving from other ecosystems:
**tests live beside the code they test** (`wsl.go` and `wsl_test.go` in the same folder — the
`_test.go` suffix is how the toolchain finds them, and package-private tests need it), and
there is no `src/` — the module root *is* the source root, and `internal/` is a compiler
rule (nothing outside this module can import it), not a naming preference. `test/e2e/` exists
only for suites that belong to no single package.

## Documentation

Also published as a site: **<https://wslkit.github.io/skrog/>**, generated
from these same files. Grouped below by the question you arrived with; every
page is reachable from here.

**Start**
[Bundled docker CLI](docs/docker-cli.md) ·
[Dev Containers](docs/devcontainers.md)

**Keep it healthy**
[Housekeeping: prune, compact, relocate](docs/housekeeping.md) ·
[Snapshots](docs/snapshots.md) ·
[Staying current](docs/upgrading.md) ·
[Engine upgrade and rollback](docs/engine-upgrade.md) ·
[VM sizing](docs/vm-sizing.md)

**At work**
[Corporate networks](docs/corporate-network.md) ·
[VPNs](docs/vpn.md) ·
[Air-gapped installs](docs/air-gap.md) ·
[Security and trust boundaries](docs/security.md) ·
[Code signing policy](docs/code-signing.md) ·
[Audit log](docs/audit.md) ·
[Admission control](docs/policy.md)

**Fleet and CI**
[CI runners](docs/ci-runners.md) ·
[Unattended / auto-logon runners](docs/auto-logon-runner.md) ·
[Declarative install](docs/declarative-install.md) ·
[Profiles](docs/profiles.md) ·
[Remote engine over mTLS](docs/remote-engine.md) ·
[Local CI](docs/local-ci.md)

**Advanced**
[GPU (NVIDIA, and experimental AMD)](docs/gpu.md) ·
[Kubernetes](docs/kubernetes.md) ·
[Lifecycle hooks](docs/hooks.md) ·
[JSON output contract](docs/cli-json.md) ·
[Command reference](docs/reference.md)

**Contributing**
[Releasing](RELEASING.md) ·
[Bumping the engine and docker CLI](docs/bumping-upstream.md) ·
[Design notes](docs/design/) ·
[Per-workspace engines](docs/design/per-workspace-engine.md)

Editing the docs edits the site: `pwsh -File scripts/build-docs.ps1 -Serve`
previews it locally, and merging to `main` publishes it. Nothing in `site/` is
hand-written prose except the front page.

## License

[Apache-2.0](LICENSE). Docker and the Docker logo are trademarks of Docker, Inc.
Skrog is not affiliated with or endorsed by Docker, Inc.
