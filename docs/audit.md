# Audit log

Because Hawser proxies the docker API at the pipe, it can record the
container-affecting calls that actually crossed it — image pulls, container
create / start / stop / remove, exec, and builds — something Docker Desktop
exposes nowhere. Useful for "what did that compose file pull and mount?", and
for security review on a shared or CI machine.

Off by default. Turn it on and restart the bridge:

```
hawser config set audit on
hawser restart
hawser audit tail
hawser audit tail --since 30m
hawser audit tail -n 50
```

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

- The setting is read when the bridge starts, so `hawser restart` is needed
  after toggling it.
- Records are best-effort: a write failure never blocks or fails the docker
  command being proxied.
- It pairs with the coming policy engine ([#120](https://github.com/zcsizmadia/hawser/issues/120)):
  every allow/deny decision there becomes one audit record here.
