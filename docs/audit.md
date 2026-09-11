# Audit log

Because Hawser proxies the docker API at the pipe, it can record the
container-affecting calls that actually crossed it — image pulls, container
create / start / stop / remove, exec, and builds — something Docker Desktop
exposes nowhere. Useful for "what did that compose file pull and mount?", and
for security review on a shared or CI machine.

Off by default. Turn it on; it takes effect on the next docker call, with
nothing to restart:

```
hawser config set audit on
hawser audit tail
hawser audit tail --since 30m
hawser audit tail -n 50
```

## Tracing a command

`hawser audit trace` answers "what did *that* just do to the engine?" — run
anything, get a summary of the records written while it ran:

```
hawser audit trace -- act -j build
hawser audit trace -- docker compose up -d
hawser audit --json trace -- gitlab-ci-local test        # machine-readable
hawser audit --raw trace -- ./deploy.ps1                 # the records themselves
```

```
--- hawser audit trace: act -j build (exit 0, 41.2s) ---
  container-create   3
  container-start    3
  exec-start         1
  image-pull         2
  images:     catthehacker/ubuntu:act-latest, node:20
  containers: act-build-1a2b, db
```

It is the trace for opaque CI YAML — see exactly which images a pipeline pulls
and which containers it starts, locally, before pushing — and for auditing a
script you did not write. The traced command's exit code is propagated, so
`hawser audit trace -- make test` fails exactly when `make test` does.

Two honest limits: attribution is **by position in the log** (everything
appended after the command started), so concurrent docker use during the run is
included; and audit must already be on (it refuses with the recipe otherwise,
rather than restarting the engine behind your back).

## What a record looks like

One JSON line per call, written to `audit.log` in the state directory
(rotated):

```json
{"time":"2026-09-09T12:00:00.000Z","action":"image-pull","method":"POST","path":"/v1.44/images/create","image":"nginx:latest","status":200,"ms":812}
{"time":"2026-09-09T12:00:03.100Z","action":"container-create","method":"POST","path":"/v1.44/containers/create","name":"web","status":201,"ms":18}
{"time":"2026-09-09T12:00:03.200Z","action":"container-start","method":"POST","path":"/v1.44/containers/web/start","container":"web","status":204,"ms":140}
```

Recorded actions: `image-pull`, `image-push`, `image-build`, `container-create`,
`container-start` / `-stop` / `-kill` / `-restart` / `-pause` / `-unpause`,
`container-remove`, `exec-create`, `exec-start`, `volume-create`,
`network-create`. Everything else (the `/_ping` and `/containers/json` polls
docker makes constantly) is dropped, so the log stays meaningful.

## Privacy

An event is derived from the request **line and query string only** — never the
request body. Credentials, build contexts, environment variables and mount
payloads are never written. That is why a `container-create` shows the name (a
query field) but not the image or mounts (body fields).

## Notes

- The setting is followed live: the supervisor opens the log when you turn it
  on and closes it when you turn it off, both on the next docker call.

  > An earlier version read the setting once when the supervisor started and
  > told you to run `hawser restart`. That did not work — `restart` bounces
  > the engine, not the supervisor that holds the setting — so turning the
  > audit log on produced no error and no log. Fixed in
  > [#202](https://github.com/hawserhq/hawser/issues/202); the worst way for
  > a security feature to fail is the way you cannot see.
- Records are best-effort: a write failure never blocks or fails the docker
  command being proxied.
- It pairs with the coming policy engine ([#120](https://github.com/hawserhq/hawser/issues/120)):
  every allow/deny decision there becomes one audit record here.
