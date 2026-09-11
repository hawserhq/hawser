# Bundled docker CLI

Skrog runs the engine; you still need a `docker` command to talk to it. Docker
Desktop's `docker.exe` works fine (Skrog coexists with it), but if you want to
**remove Docker Desktop entirely**, Skrog can install the upstream command-line
tools itself:

```
skrog cli install
```

That fetches, checksum-verifies, and installs the exact versions pinned in this
build:

| Tool | What it is | Where it lands |
| --- | --- | --- |
| `docker` | the Docker CLI | `<state>\bin\docker.exe` (on PATH) |
| `docker compose` | Compose v2 plugin | `~/.docker/cli-plugins\docker-compose.exe` |
| `docker buildx` | Buildx plugin | `~/.docker/cli-plugins\docker-buildx.exe` |
| `docker-credential-wincred` | Windows credential helper | `<state>\bin` (on PATH) |

Nothing is fetched as "latest": the versions and per-download SHA-256 are compiled
into the Skrog binary, exactly like the engine (see [PLAN.md](../PLAN.md) §04).
The bytes come from each upstream project's own release assets — the docker CLI
from `download.docker.com`, compose/buildx/wincred from their GitHub releases —
so no Docker Desktop and no third-party mirror is involved.

## After installing

`skrog cli install` adds the `bin` directory to your **user** PATH (no elevation)
and prepends it, so a **new** terminal resolves `docker` to the bundled CLI. Verify:

```
docker version
docker compose version
docker buildx version
```

If Docker Desktop is still installed, its `docker` may win in shells opened before
the PATH change. `skrog doctor` flags that:

```
[warn] bundled docker CLI: another docker shadows the bundled CLI on PATH
  bundled: ...\Skrog\bin\docker.exe
  active:  C:\Program Files\Docker\Docker\resources\bin\docker.exe
```

Open a new terminal (or stop/uninstall Docker Desktop) and it resolves to Skrog's.

Pass `--no-path` to install the tools without touching PATH (you add the directory
yourself).

## Licensing

The bundled tools are open source and redistributable: the docker CLI, Compose,
and Buildx are Apache-2.0; the credential helper is MIT. Each project's LICENSE is
installed alongside the binaries in `<state>\bin\licenses`. Skrog is not
affiliated with or endorsed by Docker, Inc.; "Docker" is a trademark of Docker,
Inc.

## Uninstalling

```
skrog cli uninstall
```

removes the bundled tools and takes the `bin` directory back off your PATH. (Plain
`skrog uninstall` removes the engine; the CLI bundle is managed separately so you
can keep the CLI while reinstalling the engine.)

## Architecture note

Windows **amd64** gets all four tools. On Windows **arm64**, compose, buildx, and
the credential helper are available, but upstream does not yet publish a Windows
arm64 `docker.exe`; `skrog cli install` reports it as unavailable and installs
the rest. Use Docker Desktop's `docker` (which is arm64-native) until an upstream
arm64 CLI ships.
