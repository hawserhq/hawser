# The `--json` contract

Every Hawser command that reports state can emit machine-readable JSON with
`--json`. This is **the contract** that tools build on — the
[VS Code extension](https://github.com/zcsizmadia/hawser-vscode), CI scripts,
fleet health checks — so it is governed by three rules:

1. **Additive only.** Fields are added, never renamed or removed. A consumer
   that ignores unknown fields keeps working across Hawser versions.
2. **Exit codes mean the same thing as the human output** — `0` ok, `1` error,
   `2` usage, `3` not installed / not found — and a non-zero exit still emits
   the JSON when there is something to say (e.g. `version --json` exits 3 with
   no engine, `cli status --json` exits 3 with tools missing).
3. **Arrays are arrays.** An empty list is `[]`, never `null`. Where `null` is
   used it is deliberate and documented (`config --json` → `engine`).

Shapes are pinned by `cmd/hawser/jsonshapes_test.go`.

## `hawser status --json`

```json
{
  "installed": true,
  "distro": "hawser-engine",
  "stateDir": "C:\\Users\\me\\AppData\\Local\\Hawser",
  "supervisor": "running",
  "engine": "running",
  "desired": "running",
  "profile": "work",
  "gpu": { "enabled": true, "probed": true, "visible": true, "specInstalled": true }
}
```

- `supervisor`: `running` | `stopped`.
- `engine`: `running` | `idle` | `stopped`. **`idle`** is the engine stopped by the
  idle timeout — healthy, it wakes on the next `docker` call. Scripts can tell
  it from broken.
- `desired`: `running` | `stopped` (what `hawser start`/`stop` last asked for).
- `profile`: omitted when no profile is active.
- `gpu`: `visible` and `specInstalled` are **only probed while the engine is
  running and `enabled` is true** — status never boots a stopped distro (#82).
  `probed: false` means they are not authoritative.

Exit `0` always (an uninstalled machine is `installed: false`, not an error).

## `hawser version --json`

`{ "app": "0.3.0", ... }` — the full component picture; see `hawser version`.
Exits `3` when no engine is installed (still emits JSON).

## `hawser doctor --json`

An array of check results: `{ "name", "title", "status", "summary", "detail",
"remedy", "fixed" }` with `status` one of `ok` | `skip` | `warn` | `fail`.
Exit code is the worst status (`0` ok/skip, `1` warn, `2`… see `doctor --help`).

## `hawser config --json`

```json
{
  "settings": { "idle-timeout": "off", "audit": "on", "network.proxy": "", "gpu": "off", "...": "..." },
  "engine":   { "engine.registry-mirrors": "https://mirror.corp", "...": "..." }
}
```

- `settings`: every known key with its stored value **or default** — a stable
  key set.
- `engine`: the engine's `daemon.json` keys. **`null` when no engine is
  installed; `{}` when installed with nothing set.** The two are different
  answers.

## `hawser cli status --json`

```json
{
  "arch": "amd64",
  "binDir": "C:\\...\\Hawser\\bin",
  "onPath": true,
  "activeDocker": "C:\\...\\Hawser\\bin\\docker.exe",
  "tools": [
    { "name": "docker",  "version": "29.8.0", "role": "cli",    "path": "...", "installed": true, "available": true },
    { "name": "compose", "version": "5.5.1",  "role": "plugin", "path": "...", "installed": true, "available": true }
  ]
}
```

- `available`: whether the manifest publishes the tool for this arch at all
  (the docker CLI has no Windows arm64 build — `available: false` there).
- `activeDocker`: the `docker` that resolves on PATH; omitted if none.
- Exits `3` when an available tool is not installed.

## `hawser snapshot … --json`

- `snapshot list --json` → array (always) of
  `{ "name", "created", "engineVersion", "distro", "sha256", "sizeBytes" }`.
- `snapshot save <name> --json` → one such object.
- `snapshot restore <name> --yes --json` → `{ "restored": "<name>" }`.
- `snapshot delete <name> --json` → `{ "deleted": "<name>" }`.

Flags come **before** the verb: `hawser snapshot --json list`.

## `hawser profile … --json`

- `profile --json` (list) →
  `{ "active": "work", "profiles": [ { "name": "work", "active": true }, … ] }`
  (`active` omitted when none; `profiles` always an array).
- `profile show <name> --json` → the profile as the same document its YAML
  holds: `distro`, `data-dir`, `engine-version`, `idle-timeout`, `autostart`,
  `engine`, `hooks`, `integrations` (unset fields omitted).

## `hawser audit tail`

The audit log **is already JSON lines** — one `audit.Event` per line:

```json
{"time":"2026-09-09T16:46:39.226Z","action":"image-pull","method":"POST","path":"/v1.44/images/create","image":"nvidia/cuda:12.4.1-base-ubuntu22.04","status":200,"ms":1840}
```

Fields: `time`, `action`, `method`, `path`, `image`, `name`, `container`,
`status`, `ms`, `error` (optional ones omitted when empty). Records are derived
from the request line only, never the body.

- Default output: JSON lines (one parse per line; friendly to `tail -f`).
- `audit tail --json`: the same records as **one JSON array**, for a single
  parse. `[]` when the log does not exist yet.

## `hawser install --json`

The resulting install manifest: `{ "distro", "dataDir", "rootfsUrl",
"rootfsSha256", "engineVersion", "installedAt", "wslVersion" }`.

## Not yet JSON

`hawser remote` (#138) will ship with `--json` from the start. Anything else
that gains state reporting must gain `--json` in the same change and be added
here.
