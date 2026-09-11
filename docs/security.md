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

Who may produce a binary that claims to be Hawser, and how, is written down
separately in the [code signing policy](code-signing.md) — including the fact
that the Windows binaries are **not signed yet**.

Recorded download URLs are normalised to the project's current home rather
than relying on a GitHub redirect: a redirect stops the moment the old path is
occupied again, and anyone can occupy an abandoned repository name. The
checksum pin means a substituted rootfs fails verification rather than being
imported, so this is provenance, not integrity — but depending on a redirect
for either is a choice worth not making.

The engine (dockerd, containerd, runc, buildkit) is **built from source** at
pinned upstream tags whose commit SHAs are verified during the build, and the
Alpine base is pinned by digest (#88) — a moved tag or re-pushed image fails
the build rather than shipping. The rootfs is published with a SHA-256 that
`hawser install` verifies before importing; there is no code path that imports
an unverified rootfs.

### Verifying a download

> **v0.3.0 predates this.** Cosign signing and SLSA provenance were added two
> days after v0.3.0 was tagged, so that release ships the two zips and
> `SHA256SUMS` and nothing else — `gh attestation verify` returns 404 against
> it, and there is no `SHA256SUMS.cosign.bundle` to check. **Step 3 below is
> the verification that works today.** Steps 1 and 2 apply from the next
> release onward ([#225](https://github.com/hawserhq/hawser/issues/225)).
>
> Saying so matters: someone who runs step 1 on v0.3.0 and sees a 404 cannot
> otherwise tell "this release predates the feature" from "this artifact is
> not authentic", and the second reading is the alarming one.

From v0.4.0, every release artifact carries **SLSA build provenance** and the
checksum file is **signed with cosign keyless** (Sigstore, GitHub OIDC — no
long-lived key exists to be stolen). Two independent checks, answering
different questions:

```
# 1. Did GitHub Actions build this, from this repository, at a known commit?
gh attestation verify hawser_0.4.0_windows_amd64.zip --owner hawserhq

# 2. Is the checksum list itself authentic? (offline against the Sigstore log)
cosign verify-blob \
  --bundle SHA256SUMS.cosign.bundle \
  --certificate-identity-regexp '^https://github.com/hawserhq/hawser/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  SHA256SUMS

# 3. Then the usual: does your download match the signed list?
sha256sum -c SHA256SUMS --ignore-missing
```

### Licences of what the rootfs ships

The engine components are Apache-2.0 and the Alpine userland is a mix
(busybox and apk-tools are GPL-2.0). Every component's licence text is inside
the rootfs at `/usr/share/licenses/<component>/`, copied at build time from
the exact commit recorded in `/etc/hawser/commits`.

Alpine's own images carry no licence files, so `/usr/share/licenses/README`
names the Alpine release and points at `aports` and the package mirror, which
is also the written offer for the GPL components' source.

The SPDX SBOM beside the tarball records a licence identifier per component.
It is an inventory, not the licence text -- both ship, because a machine-
readable identifier is not what Apache-2.0 section 4(a) asks for.

The rootfs tarball, its `.sha256` and its SBOM are attested and signed the same
way, so a tampered rootfs fails verification **even if the attacker also edits
the sha256 in `internal/release/manifest.json`** — the signature is independent
of the pin.

### Making `hawser install` check it for you

The SHA-256 pin is always enforced. The signature check is opt-in:

```
hawser config set install.verify-signature on
```

From then on, every install and `hawser engine upgrade` verifies the rootfs
signature before importing, and **refuses** if it cannot — verification that
silently does nothing is the failure this exists to prevent. Three refusals,
each with its own message because each needs a different action:

- **no `cosign` on PATH** — verification shells out to cosign rather than
  vendoring Sigstore into a binary budgeted under 15 MB, so it tells you to
  install it (or to turn the check off and rely on the pin).
- **the release carries no signature** — anything cut before signing existed,
  and any rootfs you built yourself.
- **the signed checksum does not match the tarball** — the interesting one.

`--no-verify-signature` skips the check for a single install and logs that it
did. An unlogged bypass of a security check would be worse than not having one.

Air-gapped installs (`--offline`) have no transparency log to reach, which is
the other reason the check is opt-in rather than default-on.

Verified against a signed release: a tampered rootfs whose checksum was edited
to match — so the pin *passed* — was still refused, because the signed checksum
named the original bytes.

Release binaries are **not yet Authenticode code-signed** — that needs a
purchased certificate and is tracked by
[#77](https://github.com/hawserhq/hawser/issues/77) — so SmartScreen will
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
- **The signature check is off by default.** The SHA-256 pin is always
  enforced; signature verification is opt-in
  (`hawser config set install.verify-signature on`) because it needs `cosign`
  on PATH and cannot work on an air-gapped install (above).
- **The sibling-distro vsock boundary** is authenticated only by a handshake,
  not a secret, today; see #81.

Found a security issue? Please open an issue, or for anything sensitive,
contact the maintainer privately rather than filing publicly.
