# Lifecycle hooks

Run your own script when the engine's state changes. A hook is just a path to an
executable; Skrog runs it on the event, time-bounded and best-effort — a
failing or slow hook is logged but never blocks the engine's lifecycle.

## Events

| config key | fires |
|---|---|
| `hook.post-start` | after the engine starts (recovery or first start) |
| `hook.pre-stop` | before the engine stops on `skrog stop` |
| `hook.on-idle-stop` | after the idle timeout stops the engine |
| `hook.on-wake` | after the engine cold-starts on demand |

Set one with `skrog config`, clear it with an empty value:

```
skrog config set hook.post-start C:\Users\me\skrog\login.cmd
skrog config set hook.post-start ""      # clear
```

The path must exist when you set it (a typo fails loudly rather than silently
doing nothing later). `.ps1` runs under PowerShell, `.cmd`/`.bat` under cmd, and
anything else is executed directly.

## Environment

Every hook gets:

- `SKROG_EVENT` — the event name (`post-start`, `pre-stop`, …), so one script
  can serve several events.
- `SKROG_STATE_DIR` — Skrog's state directory.

## Notes

- Hooks are **best-effort**: they run off the reconciler with a 2-minute
  timeout, and their success or failure is written to the supervisor log
  (`skrog` state dir), not surfaced to `docker`. They observe the lifecycle;
  they do not gate it.
- Because they run inside the always-on supervisor, they fire whether the engine
  moved because of you (`skrog stop`), the idle timeout, or a crash recovery.

## Recipes

**Log in to a private registry after the engine starts** (`hook.post-start`):

```cmd
@echo off
docker login registry.example.com -u "%REG_USER%" -p "%REG_TOKEN%"
```

**Bring up Portainer after start** (`hook.post-start`):

```cmd
@echo off
docker start portainer 2>nul || docker run -d --name portainer -p 9000:9000 ^
  -v /var/run/docker.sock:/var/run/docker.sock portainer/portainer-ce
```

**Warm a build cache after a cold wake** (`hook.on-wake`, PowerShell):

```powershell
docker pull node:22-alpine
docker pull golang:1.27-alpine
```
