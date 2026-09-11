# Running CI pipelines locally

The push-and-wait loop is the worst part of CI work: edit YAML, push, wait five
minutes, read a log, repeat. Every tool below runs your pipeline on your own
machine against Skrog's engine, so the loop is seconds long — and because the
engine is *pinned*, what runs locally is the engine that runs on the runner.

Everything on this page was verified against a Skrog engine; where a tool has
a limitation, it is named as the tool's, not hidden.

For installing Skrog *on* a runner, see [ci-runners.md](ci-runners.md).

## The hook: laptop == runner, by checksum

```
skrog lock                 # writes skrog.lock: dockerd, containerd, runc, BuildKit
git add skrog.lock
```

`setup-skrog` installs exactly that engine on the runner; `skrog install
--locked skrog.lock` installs it on a laptop. Same versions **to the commit**.
Docker Desktop cannot pin an engine version, so "works locally, fails in CI"
from engine drift stops being a category of bug.

Point any tool at the engine the same way:

```
DOCKER_HOST=npipe:////./pipe/skrog_engine
```

(`skrog status --json` prints the pipe in use — Skrog takes
`\\.\pipe\docker_engine` when it is free, its own when Docker Desktop holds it.)

## GitHub Actions with act

[act](https://github.com/nektos/act) runs workflow jobs as containers.

```
DOCKER_HOST=npipe:////./pipe/skrog_engine act -j build
```

Verified on the Skrog engine: a `container:` job with a `services:` sidecar —
the job network is created, the service answers by its alias, and the workspace
is bind-mounted from Windows into the job container.

```yaml
jobs:
  container-job:
    runs-on: ubuntu-latest
    container: alpine:3.20
    services:
      redis:
        image: redis:7-alpine
    steps:
      - run: apk add --no-cache redis
      - run: redis-cli -h redis ping      # PONG
```

Pick the runner image explicitly if you do not want act's default:
`-P ubuntu-latest=catthehacker/ubuntu:act-22.04`.

**act's own limitation:** it approximates GitHub's runner. Some actions behave
differently, and `runs-on: windows-*` jobs are not Linux containers at all.
That is act, not Skrog.

## GitLab CI with gitlab-ci-local

`gitlab-runner exec` was **removed in GitLab Runner 17.0**, so
[gitlab-ci-local](https://github.com/firecow/gitlab-ci-local) is the practical
way to run a `.gitlab-ci.yml` without a GitLab instance.

```
DOCKER_HOST=npipe:////./pipe/skrog_engine gitlab-ci-local unit
```

Verified on the Skrog engine: a job with a `services:` entry (service
healthcheck passes, `redis-cli -h redis ping` → `PONG`) and a job that builds
**and runs** an image through the mounted engine socket.

```yaml
docker-build:
  image: docker:cli
  variables:
    DOCKER_HOST: unix:///var/run/docker.sock
  script:
    - docker build -t proof:test .
    - docker run --rm proof:test
```

…run with the socket mounted into the job container:

```
gitlab-ci-local --volume /var/run/docker.sock:/var/run/docker.sock docker-build
```

Socket-mounting is preferable to the `docker:dind` service: no privileged
container, and the job shares the engine's image cache instead of pulling
everything twice.

**gitlab-ci-local on Windows needs `rsync`** — it stages the working tree
through it. Two wrinkles worth knowing, both the tool's:

- Without rsync on `PATH`, every job fails during setup with
  `rsync: command not found`.
- MSYS2's rsync then fails on read-only files (`failed to set permissions on
  .git/objects/…: Permission denied`), because it cannot chmod them under
  Windows ACLs.

The reliable answer is to run it from Linux against the same engine — a WSL
distro (`skrog wsl-integrate` shares the engine socket into your own distros)
or a container:

```
docker run --rm -v //./pipe/skrog_engine:/var/run/docker.sock \
  -v "%CD%:/work" node:22-bookworm bash -lc \
  "apt-get -qq update && apt-get -qq install -y rsync git && npm i -g gitlab-ci-local && cd /work && gitlab-ci-local"
```

That is exactly how the runs above were verified. (gitlab-ci-local needs Node
22 or newer.)

## Dagger

[Dagger](https://dagger.io) provisions its own engine container and talks to
`DOCKER_HOST`, so it needs nothing special:

```
DOCKER_HOST=npipe:////./pipe/skrog_engine dagger core container \
  from --address=alpine:3.20 with-exec --args=echo,hello stdout
```

Verified: the `dagger-engine` container starts on the Skrog engine and a
pipeline runs to completion. `docker ps` shows it sitting there between runs —
it is a long-lived cache, and `skrog prune` will not touch a running
container.

## BuildKit cache parity

The engine ships a **pinned BuildKit**, which is what keeps a cache written on
one machine readable on another. Exporting and importing it turns a cold CI
build into a warm one:

```
docker buildx create --name bake --driver docker-container --use
docker buildx build --cache-to   type=local,dest=./buildcache,mode=max .
docker buildx build --cache-from type=local,src=./buildcache .
```

Measured here, with a deliberately slow layer: **26.6 s** cold; after
`docker buildx prune -af` wiped the builder's 124 MB of cache, a rebuild
importing that directory reported `CACHED` in **1.7 s**. `docker-bake.hcl`
takes the same `cache-to`/`cache-from`, so a CI job and a laptop can share one
declaration.

**A buildx wrinkle, not an engine one:** registry cache export to a *plain
HTTP* registry fails —

```
ERROR: failed to solve: error writing layer blob: failed to do request:
Head "https://localhost:5000/v2/...": read: connection reset by peer
```

— because BuildKit still speaks TLS despite `registry.insecure=true` on the
cache attributes and `http = true` in `buildkitd.toml` (verified present inside
the builder). Against an HTTPS registry — which is what a real shared cache is
— `type=registry` works normally; for a local experiment, use `type=local`.

## What the local engine gives you that a runner does not

- **Idle stop.** The engine parks itself when nothing is using it, so a machine
  you use for occasional local pipeline runs is not paying RAM for an idle VM
  (`skrog config set idle-timeout 30m`).
- **Corporate network.** Proxy and CA import (`network.import-host-cas`) mean a
  local pipeline pulls images at work, behind the same TLS-inspecting VPN that
  breaks naive setups — see [corporate-network.md](corporate-network.md) and
  [vpn.md](vpn.md).
- **GPU.** `skrog enable-gpu` makes `--gpus all` work, so an ML job in a local
  pipeline uses the GPU already in your laptop — [gpu.md](gpu.md).
- **Snapshots.** `skrog snapshot save clean` before a pipeline experiment and
  `skrog reset --to clean` after, instead of hand-cleaning containers —
  [snapshots.md](snapshots.md).
