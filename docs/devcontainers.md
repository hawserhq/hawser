# Dev Containers & VS Code

The Dev Containers CLI and the VS Code Dev Containers extension work against the
Skrog engine with **no shim** — Skrog serves the standard Docker API, and its
Windows-path rewriting handles the bind mounts these tools generate.

Validated end to end against a real Skrog engine (Dev Containers CLI 0.89.0):
`devcontainer up` builds and starts the container, the `C:\…\project →
/workspaces/…` bind mount is translated transparently, and `devcontainer exec`
runs commands inside it.

## Point them at Skrog

Both use the docker CLI, so anything that selects the Skrog engine works:

- **Docker context** (simplest): `docker context use skrog` makes it the
  default for every tool, the CLI and Dev Containers included.
- **`DOCKER_HOST`**: set it to the Skrog pipe for one shell —
  `npipe:////./pipe/docker_engine` (or `…/skrog_engine` if Docker Desktop holds
  the default pipe; `skrog status` and the install output name the one in use).

### Dev Containers CLI

```
docker context use skrog
devcontainer up   --workspace-folder .
devcontainer exec --workspace-folder . bash
```

### VS Code

VS Code's Dev Containers extension uses whatever docker context / `DOCKER_HOST`
your environment selects. Select the `skrog` context (or set `DOCKER_HOST`) and
"Reopen in Container" builds against the Skrog engine like any other. To be
explicit, set `"docker.environment": { "DOCKER_HOST": "npipe:////./pipe/docker_engine" }`
in your VS Code settings.

## Notes

- Bind mounts with Windows paths (`.` → the workspace) are rewritten by the
  bridge, so nothing special is needed for source-tree mounts.
- The engine is the upstream Docker Engine, so Dev Container **features**,
  Docker Compose-based dev containers, and `docker-in-docker` behave exactly as
  they do on any Linux engine.
