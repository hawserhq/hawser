# Corporate networks

The most common "works at home, breaks at work" failure: behind a TLS-inspecting
proxy, `docker pull` fails with an x509 / certificate error because the engine
does not trust the corporate root CA. Hawser fixes that, opt-in.

```
hawser config set network.import-host-cas on     # trust the host's roots
hawser config set network.proxy http://proxy.corp:8080
hawser config set network.no-proxy internal.corp,10.0.0.0/8
hawser restart                                    # applies the above
```

`hawser doctor` flags the classic mistake — a proxy set without CA import — before
you hit the x509 error.

## What each does

- **`network.import-host-cas`** — reads your Windows host's trusted root CA store
  (both LocalMachine and CurrentUser `Root`; **no elevation** — it's a readable
  store) and installs those roots into the engine's trust store on every start.
  This is what lets the engine pull through a proxy that re-signs TLS with a
  corporate root. Nothing silent: it is off by default, opt-in, and logged.
- **`network.proxy`** / **`network.no-proxy`** — set `HTTP_PROXY` / `HTTPS_PROXY`
  / `NO_PROXY` for dockerd, so its registry pulls go through your proxy.
  `localhost` and `127.0.0.1` are always bypassed.
- **Registry mirrors / insecure registries** are the engine `daemon.json` keys
  from [`hawser config set engine.*`](../README.md): `engine.registry-mirrors`,
  `engine.insecure-registries`.

## Notes

- These are re-read at **every engine start**, so `hawser restart` applies a
  change and a rootfs re-import (reinstall) keeps one. They configure dockerd
  itself, which is why they need the engine to come back rather than taking
  effect mid-flight.

  > They used to be captured once when the supervisor started, which meant
  > `hawser restart` re-applied whatever was set at logon and silently ignored
  > anything you had changed since. Fixed in
  > [#202](https://github.com/hawserhq/hawser/issues/202).
- The proxy affects the **engine's** pulls. Containers and builds that need a
  proxy still take it the usual way (build args / a container's own env).
- Trusting the host CA store is a real trust decision — it means the engine
  accepts the same roots your machine does. That is exactly what a corporate
  TLS-inspecting proxy needs, and why it is opt-in.

## See also

- [vpn.md](vpn.md) — the other half of "works at home, breaks at work": a VPN
  that clamps the MTU or hijacks DNS. `hawser doctor` recognizes the common
  clients and prints the fix.
