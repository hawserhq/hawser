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
declarative installs; a bundled docker CLI and corporate-network support are next. Read
[PLAN.md](PLAN.md) for the strategy and [ROADMAP.md](ROADMAP.md) for the schedule; the
[issue tracker](https://github.com/zcsizmadia/hawser/issues) is the live state.

## Install

Requirements: Windows 11 with WSL2, and any `docker` CLI on PATH (Docker Desktop's works —
Hawser coexists with it rather than replacing it; a bundled CLI is planned for a later release).

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
  with `--json`, `--report` (paste straight into an issue), and `--fix` for the safe subset
- **Validated engine settings**: `hawser config set engine.<key>` edits the engine's
  `daemon.json` (registry mirrors, logging, DNS…), checked with `dockerd --validate` before
  it applies and rolled back if the engine will not come back
- **Lifecycle hooks**: run your own script on post-start / pre-stop / on-idle-stop / on-wake
  ([docs/hooks.md](docs/hooks.md))
- **Declarative installs**: `hawser install --config hawser.yaml` (idempotent) and
  `hawser config export` — infrastructure-as-code for a fleet
  ([docs/declarative-install.md](docs/declarative-install.md))
- **`hawser wsl-integrate <distro>`**: use the engine from inside your own WSL distros
- **`hawser migrate --from-desktop`**: copy images and volumes out of Docker Desktop,
  non-destructively and resumably (`--dry-run` first)
- Optional status-light tray (`hawsertray.exe`) — six menu items, forever
- Headless CI installs (`--headless`, exit codes, `--json`), version pinning as a contract
  (nothing fetches "latest"), no telemetry
- A logged-on session is required — a WSL2 platform constraint that binds every WSL-based
  engine; for CI runners see [docs/auto-logon-runner.md](docs/auto-logon-runner.md)

## What's ahead

A bundled docker CLI + compose + buildx (uninstall Docker Desktop entirely), corporate
proxy/CA trust + registry mirrors, VHDX compaction and data-dir relocation, and pinned engine
upgrades with rollback — tracked in the
[issue tracker](https://github.com/zcsizmadia/hawser/issues).

## What it will never be

Windows containers, Kubernetes, or a management GUI. Because Hawser serves the standard
Docker API, existing frontends (Portainer, lazydocker, VS Code) already work against it.

## Repository layout

Standard Go project layout — the Go toolchain, not a framework, decides this shape:

```
cmd/hawser/     the CLI — the product; every capability lives here
cmd/hawserw/    windowless logon launcher (starts the supervisor, no console flash)
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

## License

[Apache-2.0](LICENSE). Docker and the Docker logo are trademarks of Docker, Inc.
Hawser is not affiliated with or endorsed by Docker, Inc.
