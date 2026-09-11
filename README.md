<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/hawser-mark-ondark.svg">
    <img alt="Hawser" src="assets/hawser-mark.svg" width="120" height="120">
  </picture>
</p>

<h1 align="center">Hawser</h1>

<p align="center"><em><strong>hawser</strong> (n.) — the heavy line that moors a ship to the dock. It holds fast.</em></p>

<p align="center">
  <a href="https://github.com/zcsizmadia/hawser/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/zcsizmadia/hawser/actions/workflows/ci.yml/badge.svg?branch=main"></a>
  <a href="https://github.com/zcsizmadia/hawser/releases"><img alt="Latest release" src="https://img.shields.io/github/v/release/zcsizmadia/hawser?include_prereleases&sort=semver&label=release&color=0a7d84"></a>
  <a href="LICENSE"><img alt="License: Apache-2.0" src="https://img.shields.io/github/license/zcsizmadia/hawser?color=2F3B45"></a>
  <img alt="Go" src="https://img.shields.io/github/go-mod/go-version/zcsizmadia/hawser?color=00ADD8">
  <img alt="Platform: Windows 11 + WSL2" src="https://img.shields.io/badge/platform-Windows%2011%20%2B%20WSL2-2F3B45">
</p>

A minimal, invisible way to run the upstream open source **Docker Engine on Windows** via WSL2.
No license fees, no Electron, no Kubernetes — install once, `docker ps` works forever, on
laptops and CI runners alike.

**Status: v0.3 pre-release.** Installable and working as a daily driver: install once and
the engine starts at every logon, heals itself, and answers `docker` at the same speed as
Docker Desktop. v0.3 adds `hawser doctor`, validated engine settings, lifecycle hooks, and
declarative installs, corporate-network/VPN support, and a bundled docker CLI so you can
drop Docker Desktop entirely. Read
[PLAN.md](PLAN.md) for the strategy and [ROADMAP.md](ROADMAP.md) for the schedule; the
[issue tracker](https://github.com/zcsizmadia/hawser/issues) is the live state.

## Install

Requirements: Windows 11 with WSL2, and a `docker` CLI. Docker Desktop's works (Hawser
coexists with it), or install Hawser's own bundled CLI with `hawser cli install` and drop
Docker Desktop entirely — see [docs/docker-cli.md](docs/docker-cli.md).

1. Download the zip for your architecture from the
   [latest release](https://github.com/zcsizmadia/hawser/releases) and verify it against
   `SHA256SUMS` (binaries are not yet signed; SmartScreen will warn)
2. `hawser.exe install` — downloads the checksum-verified engine rootfs, imports it as the
   `hawser-engine` WSL2 distro, starts the engine, wires a `hawser` docker context, and
   registers the supervisor to start at logon (`--no-autostart` opts out)
3. `hawser.exe start` — brings up the always-on bridge now (from your next logon it starts
   itself)
4. `docker --context hawser run --rm hello-world`

`hawser.exe uninstall` removes everything Hawser created — the distro and all images and
volumes in it, the autostart entry, any distro integrations — and restores your previous
docker context. Nothing else on the system is touched.

## What it does today (v0.3)

- Upstream Docker Engine (Linux containers) in a dedicated WSL2 distro — the real API, byte
  for byte: compose, buildx, Testcontainers, `run -it`, bind mounts with Windows paths
- **Always-on supervisor**: starts at logon, survives engine crashes, `wsl --shutdown`, and
  sleep/resume; `hawser start/stop/restart/status --json`
- **Docker Desktop speed**: a vsock transport to the engine (~80 ms `docker version`,
  measured at parity with Desktop), with an automatic fallback path
- **Idle RAM answer**: `hawser config set idle-timeout 30m` stops a quiet engine and
  cold-starts it (~1 s engine start) on your next `docker` command
- **`hawser doctor`**: diagnoses the WSL / PATH / credential-helper / supervisor quirk zoo,
  with `--json`, `--report` (paste straight into an issue), and `--fix` for the safe subset;
  recognizes corporate VPNs (GlobalProtect, AnyConnect, Zscaler…) and prints the MTU/DNS fix
  ([docs/vpn.md](docs/vpn.md))
- **Validated engine settings**: `hawser config set engine.<key>` edits the engine's
  `daemon.json` (registry mirrors, logging, DNS…), checked with `dockerd --validate` before
  it applies and rolled back if the engine will not come back
- **Lifecycle hooks**: run your own script on post-start / pre-stop / on-idle-stop / on-wake
  ([docs/hooks.md](docs/hooks.md))
- **Declarative installs**: `hawser install --config hawser.yaml` (idempotent) and
  `hawser config export` — infrastructure-as-code for a fleet
  ([docs/declarative-install.md](docs/declarative-install.md))
- **NVIDIA GPU**: `hawser enable-gpu`, then `docker run --device nvidia.com/gpu=all …` runs
  CUDA workloads (Ollama, vLLM, PyTorch) — a hookless CDI spec that works on the musl engine,
  no toolkit installed ([docs/gpu.md](docs/gpu.md))
- **Bundled docker CLI**: `hawser cli install` installs the upstream docker CLI + compose +
  buildx + credential helper — checksum-pinned, nothing fetched as "latest" — so you can
  uninstall Docker Desktop entirely ([docs/docker-cli.md](docs/docker-cli.md))
- **Remote engine over mutual TLS**: `hawser serve --tcp` exposes the engine to a
  teammate or CI runner, reachable only by holders of a client cert this machine's CA
  signed — off by default; on the client, `hawser remote add/use` makes it docker's default
  in one command ([docs/remote-engine.md](docs/remote-engine.md))
- **`hawser status --stats`**: container/image/volume counts and reclaimable space, the
  VHDX footprint, VM memory and CPUs (configured versus actual), engine and supervisor
  uptime with idle-stop history, and bridge counters including **which transport is live**
  — the one number that explains a slow `docker` with a healthy engine
- **Right-size the VM with consent**: `hawser config set wsl.memory 4GB` then
  `hawser wsl-config apply` shows the diff to the GLOBAL ~/.wslconfig and writes only on
  a yes (`--yes` for runners, idempotent) — [docs/vm-sizing.md](docs/vm-sizing.md)
- **Engine upgrades, reversibly**: `hawser engine upgrade` swaps the engine binaries out of a
  checksum-verified rootfs and leaves /var/lib/docker alone, so images and volumes survive;
  a new engine that does not come back is rolled back automatically
  ([docs/engine-upgrade.md](docs/engine-upgrade.md))
- **Disk hygiene**: `hawser prune` reclaims stopped containers, unused images and build cache
  through whatever docker targets; **`hawser compact`** then shrinks the engine's VHDX itself
  (`fstrim` + `CompactVirtualDisk`, no administrator rights, so it works on Windows Home);
  `hawser doctor` warns below a configurable free-space floor
  ([docs/housekeeping.md](docs/housekeeping.md))
- **`hawser wsl-integrate <distro>`**: use the engine from inside your own WSL distros
- **`hawser migrate --from-desktop`**: copy images and volumes out of Docker Desktop,
  non-destructively and resumably (`--dry-run` first)
- Optional status-light tray (`hawsertray.exe`) — six menu items, forever
- Headless CI installs (`--headless`, exit codes, `--json` on every state-reporting command —
  the contract in [docs/cli-json.md](docs/cli-json.md)), `hawser healthcheck --wait` as a
  runner readiness probe, `hawser logs --json` for log shippers, `hawser prewarm images.txt`
  to pre-pull a pinned image list, version pinning as a contract (nothing fetches "latest"),
  no telemetry
- A logged-on session is required — a WSL2 platform constraint that binds every WSL-based
  engine; for CI runners see [docs/auto-logon-runner.md](docs/auto-logon-runner.md), and
  `hawser runner check` verifies the setup in one verdict
- The supervisor is watched: if it ever dies, `hawserw.exe` restarts it in about a second
  (backing off, with a crash budget) instead of leaving every `docker` command broken until
  the next `hawser start`
- **CI runners**: GitHub Actions via
  [setup-hawser](https://github.com/zcsizmadia/setup-hawser), GitLab shell or docker
  executor, Testcontainers (Ryuk included) — [docs/ci-runners.md](docs/ci-runners.md)
- **Debug pipelines locally, against the engine your runner uses**: `act`,
  `gitlab-ci-local`, Dagger, and a BuildKit cache a laptop and a runner share; commit
  `hawser.lock` and both install the same engine to the commit —
  [docs/local-ci.md](docs/local-ci.md)
- **Kubernetes when you want it, never bundled**: `kind` and `k3d` clusters run on the
  engine, with `kubectl` and NodePort/Ingress reachable from Windows —
  [docs/kubernetes.md](docs/kubernetes.md)

## What's ahead

Data-dir relocation and signed installers (winget/scoop/choco) — tracked in the
[issue tracker](https://github.com/zcsizmadia/hawser/issues).

## What it will never be

Windows containers, Kubernetes, or a management GUI. Because Hawser serves the standard
Docker API, existing frontends (Portainer, lazydocker, VS Code) already work against it —
and a cluster is just containers, so `kind` and `k3d` run on it today
([docs/kubernetes.md](docs/kubernetes.md)) without Hawser owning a control plane.

## Repository layout

Standard Go project layout — the Go toolchain, not a framework, decides this shape:

```
cmd/hawser/     the CLI — the product; every capability lives here
cmd/hawserw/    windowless logon launcher and supervisor watchdog (no console flash)
cmd/hawsertray/ optional status-light tray; shells out to the CLI, holds no logic
internal/       implementation packages, compiler-enforced private to this module
  wsl/          every wsl.exe call, behind an interface so tests run anywhere
  ...           provision, pipeproxy, supervise, config, migrate, integrate,
                doctor, engineconfig, hawserfile, tray
guest/          Linux side: rootfs build scripts, vsock agent
docs/           operator docs, e.g. the unattended/auto-logon runner playbook
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

Grouped by the question you arrived with. A proper docs site is [#197]; until
then this is the index, and every page is reachable from here.

**Start**
[Bundled docker CLI](docs/docker-cli.md) ·
[Dev Containers](docs/devcontainers.md)

**Keep it healthy**
[Housekeeping: prune, compact, relocate](docs/housekeeping.md) ·
[Snapshots](docs/snapshots.md) ·
[Engine upgrade and rollback](docs/engine-upgrade.md) ·
[VM sizing](docs/vm-sizing.md)

**At work**
[Corporate networks](docs/corporate-network.md) ·
[VPNs](docs/vpn.md) ·
[Air-gapped installs](docs/air-gap.md) ·
[Security and trust boundaries](docs/security.md) ·
[Audit log](docs/audit.md)

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
[JSON output contract](docs/cli-json.md)

**Contributing**
[Releasing](RELEASING.md) ·
[Bumping the engine and docker CLI](docs/bumping-upstream.md) ·
[Design notes](docs/design/)

[#197]: https://github.com/zcsizmadia/hawser/issues/197

## License

[Apache-2.0](LICENSE). Docker and the Docker logo are trademarks of Docker, Inc.
Hawser is not affiliated with or endorsed by Docker, Inc.
