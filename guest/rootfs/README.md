# Skrog rootfs

The Linux side of Skrog: an Alpine base plus the engine, built into a tarball that
`wsl --import` turns into the `skrog-engine` distro.

## Provenance

Engine binaries are **compiled from upstream source** (moby, containerd, runc, buildkit at
pinned git tags) rather than repackaged from `download.docker.com`. Two reasons: the SBOM
can point at a commit instead of someone else's zip, and redistributing Docker Inc.'s
compiled bundles raises a trademark question that building from Apache-2.0 source doesn't.
See PLAN §04 and the trademark risk row in §09.

## Build

Needs Linux + Docker — CI uses `ubuntu-latest`; locally a WSL2 Ubuntu distro works.

```bash
./build.sh [out-dir]        # default ./out — takes ~30 min, mostly compiling moby
./smoke-test.sh out         # verify the artifact before trusting it
```

Outputs, all three of which become release assets:

- `skrog-rootfs-<version>.tar.gz` — the distro image
- `skrog-rootfs-<version>.tar.gz.sha256` — what `skrog install` verifies before importing
- `skrog-rootfs-<version>.spdx.json` — SBOM, so a security team can diff what's inside

The tarball is built deterministically (sorted entries, fixed mtimes and ownership,
timestamp-free gzip), so the same pins reproduce the same checksum.

## CI caching

Compiling the engine is the expensive step; assembling the rootfs takes seconds.
So CI caches only `engine-bin/` (the binaries plus `commits.txt`), keyed on a hash
of `versions.env` + `build-engine.sh` — the two files that actually determine what
those binaries are. Editing `daemon.json` or `wsl.conf` therefore rebuilds the
tarball in seconds from cached binaries, while bumping a pin recompiles.

The same mechanism works locally: point `BIN_DIR` at a directory you keep around.

```bash
BIN_DIR=~/.cache/skrog-engine ./guest/rootfs/build.sh out   # compiles once, reuses after
```

Two deliberate safety choices: there are **no `restore-keys`** (a partial cache match
would hand us binaries built from different pins — worse than a miss), and the build
re-verifies that a reused `dockerd` reports the pinned version before trusting it.
A GitHub cache is writable by any branch, so that check is what stands between a
poisoned entry and a shipped artifact. `CACHE_EPOCH` in `versions.env` forces a
recompile when something outside the keyed files changes.

## Versions

Every pin lives in `versions.env` — nothing is ever fetched as "latest". The component
combination there is the one the acceptance suite validates; bumping any single pin means
re-running acceptance, because `skrog engine upgrade` refuses combinations outside the
tested matrix (PLAN §04).

## What's baked in

- `/usr/local/bin/` — dockerd, containerd, containerd-shim-runc-v2, ctr, runc, buildkitd, buildctl
- `/etc/docker/daemon.json` — log rotation on from the first run
- `/etc/wsl.conf` — systemd off (Skrog's Windows service supervises dockerd instead),
  metadata on automounts, Windows PATH not appended into the distro
- `/etc/skrog/engine-version` — what `skrog version` reports
