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

## `hawser remote … --json`

- `remote --json` (list) →

  ```json
  {
    "current": "desktop",
    "remotes": [
      { "name": "desktop", "host": "tcp://my-desktop.corp:2376", "added": "…",
        "certNotAfter": "2028-09-09T16:00:00Z", "dir": "C:\\...\\remotes\\desktop", "current": true }
    ]
  }
  ```

  `current` is `"local"` when docker is on the `hawser` context, a remote's name
  when on `hawser-<name>`, and `""` when docker is on some other context
  entirely. `remotes` is always an array.
- `remote test <name> --json` → `{ "name", "serverVersion", "ms" }`. Exits `1`
  when the remote does not answer, `3` when no such remote.

## `hawser audit trace --json -- <cmd> [args]`

Runs the command, then reports what it did to the engine from the audit records
appended while it ran:

```json
{
  "command": ["act", "-j", "build"],
  "exitCode": 0,
  "ms": 41200,
  "events": 9,
  "actions": { "image-pull": 2, "container-create": 3, "container-start": 3, "exec-start": 1 },
  "images": ["catthehacker/ubuntu:act-latest", "node:20"],
  "containers": ["act-build-1a2b", "db"],
  "note": "audit log rotated during the run; the summary covers the current file"
}
```

- `exitCode` is the traced command's own; **the process exits with it too**, so
  `audit trace -- make test` fails exactly when `make test` does.
- `images` / `containers` are the distinct ones touched — always arrays.
- `note` is omitted unless something qualifies the summary (rotation mid-run, or
  no records written at all).
- Attribution is by log position (appended after the command started), so
  concurrent docker use during the run is included.
- Requires `audit` to be on; otherwise exits `1` with the recipe.
- `--raw` instead prints the matching records as JSON lines.

## `hawser healthcheck --json`

A readiness probe for runner warm-ups and orchestrators:

```json
{ "installed": true, "supervisor": "running", "engine": "idle", "ready": true,
  "reason": "engine idle; wakes on the next docker command" }
```

- `ready` is the verdict the exit code carries: **`0` ready, `1` not ready,
  `3` not installed**. `reason` is never omitted.
- Ready means a docker command would succeed now: the supervisor is serving the
  pipe **and** the engine is `running` or `idle` (idle wakes on demand).
- `--wait <duration>` keeps probing until ready or the deadline; nothing is
  started by the probe itself — pair it with `hawser start`.

## `hawser logs --json`

One object per line, the **same envelope for every source** so a log shipper
needs one pipeline:

```json
{"source":"dockerd","line":"time=\"2026-09-09T16:44:18Z\" level=info msg=\"Daemon has completed initialization\""}
{"source":"supervisor","line":"time=... level=INFO msg=\"engine socket is up\" distro=hawser-engine"}
{"source":"audit","line":"{\"time\":\"...\",\"action\":\"image-pull\",...}"}
```

- `--source supervisor|dockerd|audit` (default supervisor); `-n <lines>` (default
  200, `0` = all); `--follow` streams new lines and survives the 5 MB rotation.
- `line` is the raw record; parse it further if you want dockerd's logfmt fields
  or the audit event's JSON.
- Exits `3` for `--source dockerd` with no engine installed.

## `hawser prewarm --json <images.txt>`

Pulls a pinned image list ahead of need (runner warm-up, golden-image bake,
post-start hook), through whatever docker currently targets:

```json
{
  "file": "images.txt",
  "concurrency": 3,
  "pulled": 2,
  "failed": 1,
  "ms": 8420,
  "images": [
    { "ref": "alpine:3.20", "ok": true, "ms": 1210 },
    { "ref": "node:20@sha256:…", "ok": true, "ms": 8390 },
    { "ref": "ghcr.io/x/missing:1", "ok": false, "ms": 640, "error": "manifest unknown" }
  ]
}
```

- `images` is in list order and always an array; `error` is omitted on success.
- Exit `0` only when `failed` is `0`; a failed pull never stops the others.
- The list file: one reference per line, `#` comments and blank lines ignored,
  duplicates dropped. Digest pins encouraged.

## `hawser runner check --json`

One verdict on whether an unattended host will bring the engine back after a
reboot (see [auto-logon-runner.md](auto-logon-runner.md)):

```json
{
  "ready": false,
  "findings": [
    { "name": "autologon", "status": "ok", "summary": "auto-logon is configured" },
    { "name": "autologon-account", "status": "ok", "summary": "auto-logon uses this account" },
    { "name": "autologon-password", "status": "warn",
      "summary": "the auto-logon password is stored in clear text in the registry (Winlogon\\DefaultPassword)",
      "remedy": "use Sysinternals Autologon, which stores it as an LSA secret, then delete the DefaultPassword registry value (docs/auto-logon-runner.md §3)." },
    { "name": "autostart", "status": "fail", "summary": "no logon autostart; the session will start but the supervisor will not",
      "remedy": "run `hawser autostart enable` as the auto-logon account (needs hawserw.exe beside hawser.exe)." },
    { "name": "supervisor", "status": "ok", "summary": "supervisor is running" },
    { "name": "engine", "status": "ok", "summary": "engine is running" }
  ]
}
```

- `ready` is the exit code's verdict: **`0` ready (warnings allowed), `1` not
  ready, `3` not installed**. `findings` is always an array; `status` is `ok` |
  `warn` | `fail`; `remedy` is omitted when `ok`.
- Read-only and unelevated. The auto-logon account is **compared, never
  printed**, and the password value is probed for existence only.

## `hawser reset --to <snapshot> --json`

The runner's clean slate — `snapshot restore` with the interactive guards
implied (no `--yes`, no running-container check):

```json
{ "snapshot": "golden", "engineVersion": "29.7.2", "ms": 6840 }
```

- `ms` is the whole cycle — verify the archive, unregister, import, engine back
  — the number a clean-slate budget is measured against.
- `engineVersion` is the snapshot's recorded engine, omitted when unknown.
- Exit `3` when the snapshot does not exist or nothing is installed; `1` when
  the restore failed (the engine is brought back best-effort either way).

## `hawser prune --json`

Reclaims disk on whatever docker currently targets:

```json
{
  "reclaimedBytes": 1234000000,
  "failed": 0,
  "steps": [
    { "name": "containers",  "reclaimedBytes": 100000000 },
    { "name": "images",      "reclaimedBytes": 1134000000 },
    { "name": "build-cache", "reclaimedBytes": 0, "error": "..." }
  ]
}
```

- `steps` is in plan order (containers → images → volumes → build-cache) and
  always an array; `error` is omitted on success. A failed step never stops the
  others.
- Exit `0` only when `failed` is `0`.
- `--all` removes every unused image (default: dangling only); `--until 168h`
  keeps anything newer; `--build-cache` and `--volumes` widen the sweep
  (volumes hold data, so off by default).

## `hawser compact --json`

Shrinks the engine's virtual disk (fstrim + CompactVirtualDisk):

```json
{
  "distro": "hawser-engine",
  "path": "C:\\Users\\me\\AppData\\Local\\Hawser\\distro\\ext4.vhdx",
  "trimmed": true,
  "offeredBytes": 1078939029504,
  "beforeBytes": 15032385536,
  "afterBytes": 9663676416,
  "reclaimedBytes": 5368709120,
  "waitedSeconds": 66.4,
  "restarted": true,
  "dryRun": false
}
```

- `reclaimedBytes` is the difference in the file's size **on disk** and the only
  honest measure of what happened.
- `offeredBytes` is what `fstrim` printed: the free extent of the whole virtual
  disk, **not** space reclaimed. Named "offered" so nothing mistakes it for a
  result; omitted when `--no-trim` was used.
- `held` is present when other distros are keeping WSL from releasing the disk,
  and pairs with exit code **11**. Under `--dry-run` it appears without an
  error: the dry run reports that a real run would refuse, and names who.
- `waitedSeconds` is how long WSL took to let go (about a minute after the last
  distro stops).
- Exit codes: `0` ok, `1` error, `2` usage, `3` not installed, `11` the disk is
  held.

## `hawser engine list --json`

What this build can install, what is installed, and where a rollback goes:

```json
{
  "installed": "29.7.2-4",
  "previous": "29.7.2-3",
  "available": [
    { "ref": "29.7.2-4", "version": "29.7.2", "default": true, "published": true }
  ]
}
```

- `ref` is the **revisioned** label and the unit an upgrade moves between;
  `version` is only the dockerd version, and two revisions can share one.
- `published` is `false` for a manifest entry with no checksum yet — a
  placeholder that cannot be installed.
- `installed` and `previous` are omitted when unknown; `available` is always an
  array.

## `hawser engine upgrade --json` / `hawser engine rollback --json`

```json
{
  "from": "29.7.2-3",
  "to": "29.7.2-4",
  "replaced": ["dockerd", "containerd", "runc", "..."],
  "engineVersion": "29.7.2",
  "rolledBack": false,
  "dryRun": false
}
```

- `engineVersion` is what the new dockerd reports about **itself** — evidence
  the swap took, not an assumption that it did.
- `rolledBack: true` with a **non-zero exit** is the interesting case: the
  upgrade failed and the previous engine was restored, so the engine is up.
- `replaced` and `engineVersion` are absent on a dry run.
## `hawser wsl-config show|apply --json`

The WSL2 VM''s sizing, from the global `~/.wslconfig` (#148):

```json
{
  "path": "C:\\Users\\me\\.wslconfig",
  "exists": true,
  "effective": { "memory": "4GB", "processors": "2", "autoMemoryReclaim": "gradual" },
  "desired":   { "memory": "4GB", "processors": "2", "autoMemoryReclaim": "gradual" },
  "pending": [ { "key": "memory", "old": "8GB", "new": "4GB", "added": false } ],
  "applied": false
}
```

- `effective` is what the file says now; `desired` is what Hawser's own
  settings ask for; `pending` is the difference — so a converge script can tell
  "already right" from "would change something" without parsing prose.
- `pending` is omitted when there is nothing to do, which is the signal that a
  repeated `apply --yes` is a no-op.
- `applied` is `true` only when this invocation wrote the file.
- `apply --json` requires `--yes`: there is no way to ask a question in JSON, so
  it exits `2` rather than appearing to hang.
## The rule for new commands

Anything that gains state reporting must gain `--json` in the same change and
be added here; its shape goes in `cmd/hawser/jsonshapes.go` with a test.
