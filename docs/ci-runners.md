# CI runners

Skrog turns a Windows machine into a Linux-container CI runner: the engine is
upstream dockerd in WSL2, so a job that runs on `docker` on a Linux runner runs
here too, with no per-runner Docker Desktop license and no auto-update that
changes the engine under a pipeline.

There are two halves to this, and they are independent:

- **Installing Skrog on a runner** — [setup-skrog](https://github.com/wslkit/setup-skrog)
  for GitHub Actions, the same script in `before_script` for GitLab, or a baked
  image (see `contrib/`).
- **Running jobs against the engine** — everything below. Anything that follows
  `docker` follows Skrog: `DOCKER_HOST`, or the `skrog` docker context.

## Point a runner at the engine

The install wires a docker context named `skrog`, and `setup-skrog` exports
`DOCKER_CONTEXT=skrog` for the rest of the job. For tools that do not read
docker contexts, set the host explicitly:

```
DOCKER_HOST=npipe:////./pipe/skrog_engine
```

`skrog status --json` prints the pipe actually in use — Skrog takes
`\\.\pipe\docker_engine` when it is free, and its own
`\\.\pipe\skrog_engine` when Docker Desktop already holds it.

## GitHub Actions (self-hosted Windows runner)

```yaml
jobs:
  build:
    runs-on: [self-hosted, windows, skrog]
    steps:
      - uses: actions/checkout@v4
      - uses: wslkit/setup-skrog@v2
        with:
          version: 0.4.0        # pin it; "latest" resolves the newest release
      - run: docker run --rm alpine:3.20 echo hello
```

The runner needs a **logged-on interactive session** — WSL2 cannot start from a
Windows service. See [auto-logon-runner.md](auto-logon-runner.md); `skrog
runner check` gives one verdict on whether a host is set up correctly
(auto-logon, autostart, power settings, engine health).

Two things GitHub-hosted runners get wrong for this workload and a Skrog runner
gets right: the engine is pinned by *you* (a committed `skrog.lock`, so laptop
and runner install the same dockerd, containerd, runc and BuildKit to the
commit), and Linux containers run natively rather than needing a Linux runner
in the fleet.

## GitLab CI

### Shell executor (simplest)

The Windows runner runs jobs in a shell; Skrog is just the engine `docker`
talks to:

```yaml
default:
  before_script:
    - Invoke-WebRequest https://raw.githubusercontent.com/wslkit/setup-skrog/v2/scripts/install-skrog.ps1 -OutFile install-skrog.ps1
    - pwsh -File install-skrog.ps1 -Version 0.4.0
    - $env:DOCKER_CONTEXT = 'skrog'

build:
  script:
    - docker run --rm alpine:3.20 echo hello
```

On a baked image (`contrib/packer`) drop the `before_script` entirely.

### Docker executor (jobs run *inside* containers)

Point the executor at the engine's pipe in the runner's `config.toml`:

```toml
[[runners]]
  name = "windows-skrog"
  executor = "docker"
  [runners.docker]
    host = "npipe:////./pipe/skrog_engine"
    image = "alpine:3.20"
    privileged = false
    volumes = ["/cache"]
```

Use the pipe Skrog reports (`skrog status --json`), and note that the runner
service still needs the interactive session that keeps WSL2 alive — the
executor talks to the engine, but the engine is per-user.

`gitlab-runner exec` was removed in 17.0, so a pipeline cannot be run locally
with the real runner any more; use `gitlab-ci-local` (below) for that.

## Testcontainers

Testcontainers needs no special configuration beyond `DOCKER_HOST`. Two things
about it are worth stating, because both were bugs in Skrog before they were
features:

- **Mapped ports** are reached from Windows (`container.MappedPort` +
  `container.Host`), which needs `engine.userland-proxy=false` — Skrog's
  default. See [vpn.md](vpn.md#published-ports-under-mirrored-networking).
- **Ryuk**, the reaper container, mounts the engine's socket
  (`-v //./pipe/...:/var/run/docker.sock`). Skrog maps a Windows named pipe in
  a bind mount to the engine's own `/var/run/docker.sock`, so this works as it
  does on Docker Desktop. No `TESTCONTAINERS_RYUK_DISABLED` needed.

```
DOCKER_HOST=npipe:////./pipe/skrog_engine go test ./...
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
skrog prune --all --keep-recent 24h     # reclaim, with a report
skrog reset --to clean-slate            # restore a snapshot between jobs
skrog prewarm images.txt                # pull the pinned list ahead of need
```

`skrog doctor` warns before the data disk runs out (`disk-warn-below` is
configurable), which is the failure that otherwise shows up as an opaque
mid-build error. See [housekeeping.md](housekeeping.md) and
[snapshots.md](snapshots.md).

## What is not supported

- **Windows containers.** Skrog runs Linux containers via WSL2; a job that
  needs Windows containers needs a Windows-container engine.
- **Rootless / multi-user on one host.** The engine is per-user, in that user's
  interactive session. One runner account per host.
- **Running as a Windows service without a session.** WSL2 will not start; this
  is why auto-logon exists.
