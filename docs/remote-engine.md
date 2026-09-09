# Remote engine over mutual TLS

By default Hawser's engine is reachable only from the host it runs on, through a
Windows named pipe — nothing listens on the network. `hawser serve` opens a
second door: a TCP listener protected by **mutual TLS**, so a teammate, a second
machine, or a CI runner can point a stock `docker` client at your engine.

Mutual means both sides prove themselves. The server presents a certificate the
client checks, and the client presents one the server checks. Only a client
holding a certificate signed by *this machine's* CA can connect — the engine is
never open to the network at large, even while the port is listening.

```
hawser serve cert --host my-desktop.corp        # once: mint the certificates
hawser serve --tcp 0.0.0.0:2376                 # run the server (foreground)
```

## Minting the certificates

`hawser serve cert` writes three things into `tls/` under the state dir:

- **`ca.pem` / `ca-key.pem`** — a private certificate authority. Generated once
  and **reused** on later runs, so certificates you have already handed out keep
  working. Rotating the CA (delete `ca*.pem` and re-run) invalidates every
  client.
- **`server.pem` / `server-key.pem`** — the engine's certificate, valid for
  `localhost`, `127.0.0.1`, this machine's hostname, and its local IPs. Add every
  other name or address a client will dial with `--host` (repeatable):

  ```
  hawser serve cert --host my-desktop.corp --host 10.1.2.3
  ```
- **`client.pem` / `client-key.pem`** — the certificate you give to whoever
  connects. Name it with `--client <name>` (the certificate's CN) to tell clients
  apart; run the command again with a new name to mint another.

Re-running `serve cert` regenerates the server and client certs against the
existing CA — safe and idempotent. Keep every `*-key.pem` private: anyone with a
signed client certificate can reach the engine.

## Running the server

```
hawser serve --tcp 0.0.0.0:2376
```

Runs in the foreground and relays to the engine through the same bind-path
rewriting the local pipe uses, so remote `docker run -v` behaves as it does
locally. Stop it with Ctrl-C.

The engine must be running (`hawser start`). Because a remote client has no way
to wake a stopped engine, pair remote serving with the idle timeout off (its
default):

```
hawser config set idle-timeout off
```

`2376` is the IANA port for the Docker TLS endpoint. Bind `0.0.0.0` to accept
from any interface, or a specific address (e.g. `192.168.1.5:2376`) to limit it.

## Connecting a client

Copy `ca.pem`, `client.pem`, and `client-key.pem` to the client machine, into a
directory of their own, then:

**PowerShell**

```powershell
$env:DOCKER_HOST      = "tcp://my-desktop.corp:2376"
$env:DOCKER_TLS_VERIFY = "1"
$env:DOCKER_CERT_PATH  = "C:\path\to\certs"   # holds ca.pem, cert.pem, key.pem
docker version
```

`DOCKER_CERT_PATH` expects the files named **`ca.pem`, `cert.pem`, `key.pem`**.
Rename `client.pem` → `cert.pem` and `client-key.pem` → `key.pem`, or pass them
explicitly:

```
docker --tlsverify --tlscacert ca.pem --tlscert client.pem --tlskey client-key.pem \
       -H tcp://my-desktop.corp:2376 version
```

## Security notes

- **Off by default.** Nothing listens on TCP until you run `hawser serve`, and it
  runs only while that command is in the foreground.
- **No elevation.** Certificates are generated with Go's `crypto/x509`; the
  listener binds an unprivileged high port. Opening the port through the Windows
  firewall to other machines is the one step that may prompt for elevation, and
  that is Windows' firewall, not Hawser.
- **Mutual TLS, not a password.** There are no plaintext secrets. Access is
  revoked by rotating the CA (invalidating all clients) — per-client revocation
  lists are not implemented; mint clients narrowly and rotate when someone leaves.
- **The engine is root-equivalent.** A client that can reach the engine can mount
  any host path the engine can see. Hand out client certificates as carefully as
  you would ssh access.
