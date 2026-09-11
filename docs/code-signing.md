# Code signing policy

This page exists because the SignPath Foundation requires projects it sponsors
to publish one, and because anyone installing a binary that claims to be Hawser
deserves to know who can make one.

> **Status: accepted, not yet in effect.** The roles and the CI-only signing
> below are settled; Hawser's binaries are **not signed today**, because the
> application to the SignPath Foundation is still open — tracked in
> [#77](https://github.com/hawserhq/hawser/issues/77). Until a certificate is
> issued, verify releases against `SHA256SUMS` and expect a SmartScreen
> warning; see [verifying a release](#verifying-a-release-today) below.

## Attribution

Free code signing provided by [SignPath.io](https://about.signpath.io),
certificate by the [SignPath Foundation](https://signpath.org).

## Project roles

SignPath requires three roles, separated so that no single unreviewed action
produces a signed binary.

| role | who | what they can do |
|---|---|---|
| **Authors** | [@zcsizmadia](https://github.com/zcsizmadia) | commit to the repository |
| **Reviewers** | [@zcsizmadia](https://github.com/zcsizmadia) | review and merge external contributions |
| **Approvers** | [@zcsizmadia](https://github.com/zcsizmadia) | approve an individual signing request |

Hawser has one maintainer, so all three are currently the same person. Stating
that plainly is the point: the separation is a real control only when the roles
are held by different people, and claiming otherwise would misrepresent what
the signature attests to. As the project gains maintainers, the Reviewer and
Approver roles are the first to be handed to someone else, and this table is
updated in the same change.

What the arrangement does still guarantee, with one maintainer or ten: **no
signature is produced without a deliberate act**. Signing is not automatic on a
tag — every release waits for an approval that a person has to give.

Every contribution from outside the Author list is reviewed before it is
merged. Every release is signed only after a manual approval — signing is never
automatic on a tag.

Accounts holding any of these roles use multi-factor authentication for both
GitHub and SignPath.

## What is signed

The Windows executables in a release: `hawser.exe`, `hawserw.exe` and
`hawsertray.exe`.

The engine rootfs is **not** covered by this certificate. It is a Linux tarball
and is protected differently, and more strongly: pinned by SHA-256 in the
manifest compiled into the binary, verified before import, and separately
signed with [Sigstore/cosign](security.md). `hawser config set
install.verify-signature on` enforces that signature at install time.

## How a signed build is produced

Signing happens in CI, never on a developer machine — a private key that has
touched a laptop is a key whose custody cannot be described. Concretely:

1. A tag triggers `.github/workflows/release.yml`
2. The binaries are built from that tag's source with `-trimpath`, on a GitHub-hosted runner
3. [SLSA build provenance](https://slsa.dev) is attested for every artifact
4. The signing request is submitted to SignPath and waits for **manual approval**
5. Signed artifacts are published, with `SHA256SUMS` alongside

The consequence worth stating: a signed Hawser binary is reproducible only
through that pipeline, not from a local `go build`. The source is identical;
the signature is not something a local build can produce.

That is not a change SignPath introduces. The release already signs
`SHA256SUMS` with **cosign keyless**, whose identity is a GitHub Actions OIDC
token — an identity that exists nowhere but inside a workflow run. CI has
therefore always been the only thing able to produce an official Hawser
release. SignPath adds an Authenticode signature to a pipeline that was
already the sole source of signed artifacts.

## Data handling

Hawser collects nothing. There is no telemetry, no analytics, no crash
reporting, no phone-home, and no identifier of any kind is transmitted at any
point — including by the signing process, which operates on artifacts in CI and
never on a user's machine.

The single outbound request the product makes is `hawser upgrade`'s query to
the GitHub releases API, which happens only when a user runs that command and
sends nothing but the request itself. `--offline` skips it. See
[staying current](upgrading.md) and the [security model](security.md).

SignPath receives the build artifacts and the repository metadata needed to
verify them. It receives nothing about anyone who installs or runs Hawser,
because nothing about them exists to send.

## What the programme constrains

Two conditions are worth writing down, because they bind the project and not
just the pipeline.

The certificate is **issued by the Foundation to the project**, not owned by
us. And the programme requires an OSI-approved licence with **no commercial
dual-licensing, for any component**. A future paid tier or dual-licence would
end eligibility and mean buying a commercial certificate instead. Neither is a
one-way door — but both are doors.

The Foundation can also pause the subscription or revoke the certificate,
immediately or retroactively, over a Code of Conduct violation.

## Verifying a release today

Until signing is in effect:

```powershell
# Download the zip and SHA256SUMS from the release page, then:
(Get-FileHash .\hawser-windows-amd64.zip -Algorithm SHA256).Hash.ToLower()
# compare against the matching line in SHA256SUMS
```

Every release also carries SLSA provenance, which can be verified with the
[GitHub CLI](https://cli.github.com):

```powershell
gh attestation verify .\hawser-windows-amd64.zip --repo hawserhq/hawser
```

That attestation is the stronger claim of the two: it says *this artifact was
built by this workflow from this commit*, which a signature alone does not.

## Reporting a problem

A binary that claims to be Hawser and does not verify, or a signature you
cannot account for, is worth reporting immediately —
[open an issue](https://github.com/hawserhq/hawser/issues/new) or, if it looks
like a compromise rather than a mistake, mark it as such and do not include a
working reproduction in public.
