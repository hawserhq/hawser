# Air-gapped install

Isolated-network machines cannot use Docker Desktop at all — it phones home to
log in and check for updates. Hawser installs from a single file, with **zero
network calls**, and this page states that guarantee and how to check it.

## Two steps

On a **connected** machine, pack the engine into a bundle:

```
hawser bundle -o hawser-bundle.zip
```

This downloads the rootfs, verifies its SHA-256, and writes a `.zip` containing
the rootfs and a `hawser.lock` (engine version, rootfs checksum, component
versions). Copy the file to the isolated machine by whatever means you already
trust (USB, internal share).

On the **isolated** machine, install from it:

```
hawser install --offline hawser-bundle.zip
hawser start
docker run --rm hello-world
```

## The guarantee

`hawser install --offline` reads the rootfs from the bundle and imports it. It
does not resolve the release manifest, contact GitHub, or open any socket:

- The rootfs source is a local file extracted from the bundle. The only importer
  in the codebase is the same checksum-verified path a networked install uses,
  so the bundle's rootfs is verified against the lock's SHA-256 before import —
  a corrupt or swapped bundle is refused, not installed.
- Nothing is fetched as "latest," on this step or any other. Version pinning is
  a contract, and the bundle *is* the pin.

Any network access during `install --offline` is a bug — please report it.

## How to audit it

The promise is checkable, which is the point:

- **Run it disconnected.** Pull the network on the target machine (or block the
  `hawser.exe` process at the firewall) and run `hawser install --offline`. It
  completes; nothing times out waiting on a download.
- **Watch the process.** Run it under a network monitor (Process Monitor,
  `Resource Monitor`, or a packet capture). There are no connections.
- **Read the bundle.** A `.zip` is inspectable with any tool: `hawser.lock` is
  plain JSON, and the rootfs entry's SHA-256 is what the install verifies.

## Reproducibility

The bundle embeds a `hawser.lock`, so an air-gapped install is also a
reproducible one — every isolated machine installs the identical, verified
engine. See [declarative-install.md](declarative-install.md) for `hawser.lock`
and the fleet story.
