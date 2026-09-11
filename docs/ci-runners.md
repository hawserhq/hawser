# CI runners

Hawser turns a Windows machine into a Linux-container CI runner: the engine is
upstream dockerd in WSL2, so a job that runs on `docker` on a Linux runner runs
here too, with no per-runner Docker Desktop license and no auto-update that
changes the engine under a pipeline.

There are two halves to this, and they are independent:

- **Installing Hawser on a runner** — [setup-hawser](https://github.com/hawserhq/setup-hawser)
  for GitHub Actions, the same script in `before_script` for GitLab, or a baked
  image (see `contrib/`).
- **Running jobs against the engine** — everything below. Anything that follows
  `docker` follows Hawser: `DOCKER_HOST`, or the `hawser` docker context.

## Point a runner at the engine

The install wires a docker context named `hawser`, and `setup-hawser` exports
`DOCKER_CONTEXT=hawser` for the rest of the job. For tools that do not read
docker contexts, set the host explicitly:

```
DOCKER_HOST=npipe:////./pipe/hawser_engine
```

`hawser status --json` prints the pipe actually in use — Hawser takes
`\\.\pipe\docker_engine` when it is free, and its own
`\\.\pipe\hawser_engine` when Docker Desktop already holds it.

## GitHub Actions (self-hosted Windows runner)

```yaml
jobs:
  build:
    runs-on: [self-hosted, windows, hawser]
    steps:
      - uses: actions/checkout@v4
      - uses: hawserhq/setup-hawser@v1
        with:
          version: 0.3.0        # pin it; "latest" resolves the newest release
      - run: docker run --rm alpine:3.20 echo hello
```

The runner needs a **logged-on interactive session** — WSL2 cannot start from a
Windows service. See [auto-logon-runner.md](auto-logon-runner.md); `hawser
runner check` gives one verdict on whether a host is set up correctly
(auto-logon, autostart, power settings, engine health).

Two things GitHub-hosted runners get wrong for this workload and a Hawser runner
gets right: the engine is pinned by *you* (a committed `hawser.lock`, so laptop
and runner install the same dockerd, containerd, runc and BuildKit to the
commit), and Linux containers run natively rather than needing a Linux runner
in the fleet.

## GitLab CI

### Shell executor (simplest)

The Windows runner runs jobs in a shell; Hawser is just the engine `docker`
talks to:

```yaml
default:
  before_script:
    - Invoke-WebRequest https://raw.githubusercontent.com/hawserhq/setup-hawser/v1/scripts/install-hawser.ps1 -OutFile install-hawser.ps1
    - pwsh -File install-hawser.ps1 -Version 0.3.0
    - $env:DOCKER_CONTEXT = 'hawser'

build:
  script:
    - docker run --rm alpine:3.20 echo hello
```

On a baked image (`contrib/packer`) drop the `before_script` entirely.

### Docker executor (jobs run *inside* containers)

Point the executor at the engine's pipe in the runner's `config.toml`:

```toml
[[runners]]
  name = "windows-hawser"
  executor = "docker"
  [runners.docker]
    host = "npipe:////./pipe/hawser_engine"
    image = "alpine:3.20"
    privileged = false
    volumes = ["/cache"]
```

Use the pipe Hawser reports (`hawser status --json`), and note that the runner
service still needs the interactive session that keeps WSL2 alive — the
executor talks to the engine, but the engine is per-user.

`gitlab-runner exec` was removed in 17.0, so a pipeline cannot be run locally
with the real runner any more; use `gitlab-ci-local` (below) for that.

## Testcontainers

Testcontainers needs no special configuration beyond `DOCKER_HOST`. Two things
about it are worth stating, because both were bugs in Hawser before they were
features:

- **Mapped ports** are reached from Windows (`container.MappedPort` +
  `container.Host`), which needs `engine.userland-proxy=false` — Hawser's
  default. See [vpn.md](vpn.md#published-ports-under-mirrored-networking).
- **Ryuk**, the reaper container, mounts the engine's socket
  (`-v //./pipe/...:/var/run/docker.sock`). Hawser maps a Windows named pipe in
  a bind mount to the engine's own `/var/run/docker.sock`, so this works as it
  does on Docker Desktop. No `TESTCONTAINERS_RYUK_DISABLED` needed.

```
DOCKER_HOST=npipe:////./pipe/hawser_engine go test ./...
```

The acceptance suite runs a real Testcontainers module (Go, Ryuk enabled)
against the engine: `test/e2e/testcontainers`.

## Running pipelines locally

The point of a local engine is that a pipeline can be *debugged* locally, not
just executed in CI: `act` for GitHub Actions, `gitlab-ci-local` for GitLab,
Dagger, and a BuildKit cache a laptop and a runner can share. All of it — with
measured numbers and each tool's own limitations — is in
**[local-ci.md](local-ci.md)**.

## Housekeeping on a long-lived runner

A runner that never reboots accumulates images, build cache and volumes:

```
hawser prune --all --keep-recent 24h     # reclaim, with a report
hawser reset --to clean-slate            # restore a snapshot between jobs
hawser prewarm images.txt                # pull the pinned list ahead of need
```

`hawser doctor` warns before the data disk runs out (`disk-warn-below` is
configurable), which is the failure that otherwise shows up as an opaque
mid-build error. See [housekeeping.md](housekeeping.md) and
[snapshots.md](snapshots.md).

## What is not supported

- **Windows containers.** Hawser runs Linux containers via WSL2; a job that
  needs Windows containers needs a Windows-container engine.
- **Rootless / multi-user on one host.** The engine is per-user, in that user's
  interactive session. One runner account per host.
- **Running as a Windows service without a session.** WSL2 will not start; this
  is why auto-logon exists.
