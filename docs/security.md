# Hawser security model & trust boundaries

Hawser runs the real Docker Engine as root inside a WSL2 distro and bridges it
to `docker.exe`. Access to that engine is access to root inside the distro,
which — through WSL's automounted drives — can read and write your Windows
files. This page states, plainly, who can reach the engine and where the
boundaries are, so you can decide whether Hawser fits your threat model. Where
a boundary is looser than you need, it says so.

## Who can reach the engine

**The Windows named pipe** (`\\.\pipe\docker_engine`, or `\\.\pipe\hawser_engine`
when Docker Desktop owns the default). Its ACL grants access to **SYSTEM,
local administrators, and the user who installed Hawser** — and no one else.
A second, unrelated account logged onto the same machine (via RDP or fast user
switching) cannot reach your engine.

> Earlier v0.2.0 builds granted access to *all interactive users*; that was
> tightened to the owning user in v0.3 (issue #79). A future opt-in
> "Hawser Users" group (#8) will let you admit specific additional accounts
> deliberately, the way Docker Desktop's `docker-users` group works.

**Other WSL2 distros of the same user**, if you run `hawser wsl-integrate`.
That command shares the engine socket into a distro at
`/mnt/wsl/<distro>/docker.sock` with mode `0666`, so any user in that distro
can use docker without sudo. Two consequences worth understanding:

- `/mnt/wsl` is a tmpfs shared across *all of your own* WSL distros within one
  utility VM. It is not reachable by other Windows users or by the host
  network — the boundary is your own WSL environment.
- **A distro you integrate becomes root-equivalent to the engine**, and thus to
  your Windows files via drvfs. If you deliberately harden a distro as a
  sandbox (interop and automount disabled), integrating it **voids that
  sandbox**. Only integrate distros you trust with your engine. `hawser
  wsl-integrate --remove <distro>` reverses it, and `hawser uninstall` removes
  every integration Hawser created.

**The vsock transport.** On a normal install the bridge reaches the engine
over an AF_HYPERV vsock connection to an in-distro agent, not socat. The agent
accepts connections only from the host partition (CID 2), and every connection
must pass a handshake before any byte reaches dockerd. Hardening of this path
against a hostile *sibling distro impersonating the agent* is tracked in #81.

## What Hawser deliberately does not do

- **It is not a Windows service and holds no elevated persistent privilege.**
  Install and the supervisor run as your normal user. A logged-on session is
  required (WSL2 cannot start from session 0); for unattended machines see
  [auto-logon-runner.md](auto-logon-runner.md).
- **It never takes Docker Desktop's pipe.** If Desktop is serving the default
  pipe, Hawser serves its own and the two coexist.
- **It manages only the distro it created**, never `wsl --shutdown` and never
  other distros.

## Supply chain

The engine (dockerd, containerd, runc, buildkit) is **built from source** at
pinned upstream tags whose commit SHAs are verified during the build, and the
Alpine base is pinned by digest (#88) — a moved tag or re-pushed image fails
the build rather than shipping. The rootfs is published with a SHA-256 that
`hawser install` verifies before importing; there is no code path that imports
an unverified rootfs.

### Verifying a download

Every release artifact carries **SLSA build provenance** and the checksum file
is **signed with cosign keyless** (Sigstore, GitHub OIDC — no long-lived key
exists to be stolen). Two independent checks, answering different questions:

```
# 1. Did GitHub Actions build this, from this repository, at a known commit?
gh attestation verify hawser_0.4.0_windows_amd64.zip --owner zcsizmadia

# 2. Is the checksum list itself authentic? (offline against the Sigstore log)
cosign verify-blob \
  --bundle SHA256SUMS.cosign.bundle \
  --certificate-identity-regexp '^https://github.com/zcsizmadia/hawser/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  SHA256SUMS

# 3. Then the usual: does your download match the signed list?
sha256sum -c SHA256SUMS --ignore-missing
```

The rootfs tarball, its `.sha256` and its SBOM are attested and signed the same
way, so a tampered rootfs fails verification **even if the attacker also edits
the sha256 in `internal/release/manifest.json`** — the signature is independent
of the pin.

`hawser install` does not yet check these itself (it enforces the SHA-256 pin);
wiring that in, with an explicit and logged escape hatch, is the remaining half
of [#147](https://github.com/zcsizmadia/hawser/issues/147).

Release binaries are **not yet Authenticode code-signed** — that needs a
purchased certificate and is tracked by
[#77](https://github.com/zcsizmadia/hawser/issues/77) — so SmartScreen will
still warn. Provenance and Authenticode are different things and neither
substitutes for the other.

## Known limitations (stated, not hidden)

- **Fallback transport idle bound.** If the vsock agent is unreachable (an
  older rootfs, or the agent mid-restart), the bridge falls back to a
  per-connection socat relay. On that path a *quiet but live* long stream
  (`docker events`, `docker logs -f` on an idle container) is cut after about
  five minutes of no traffic in either direction. The vsock path has no such
  timer; a normal install uses it.
- **Release binaries are not Authenticode-signed** (above), so SmartScreen
  warns. They *are* attested and their checksums signed, which is a different
  guarantee: it proves origin, not that Windows will trust the executable.
- **`hawser install` does not verify the signature itself** yet — it enforces
  the SHA-256 pin, and the signature is a check you run (#147).
- **The sibling-distro vsock boundary** is authenticated only by a handshake,
  not a secret, today; see #81.

Found a security issue? Please open an issue, or for anything sensitive,
contact the maintainer privately rather than filing publicly.
