# contrib

Provisioning material for putting Skrog on machines you do not click through
by hand (#143): a Packer template that bakes a runner image, and an Ansible
role that converges an existing Windows host.

Both are **unsupported examples** in the honest sense: they encode the right
sequence and the traps worth knowing, and they are not exercised in CI, because
CI has no image-building or Windows-fleet infrastructure to exercise them
against. Read them as documentation you can run, and expect to adapt the
builder or connection plumbing to your environment.

They both lean on the same install script as
[setup-skrog](https://github.com/wslkit/setup-skrog), pinned by tag, so an
image, a GitHub Action and an Ansible run install Skrog identically — verified
against the release's `SHA256SUMS`, never "curl | iex".

## packer/ — bake a runner image

```
cd contrib/packer
packer init .
packer build -var skrog_version=0.4.0 .
packer build -var skrog_version=0.4.0 -var bundle_path=./skrog-29.7.2.zip .   # air-gapped
```

The template installs WSL2 (with the reboot it needs), stages `skrog.exe`,
imports the engine — offline from a `skrog bundle` zip when you pass one,
otherwise the pinned rootfs — registers the logon autostart, gates the image on
`skrog doctor` reporting no failures, and stops the engine so the image is
captured quiescent. A freshly provisioned runner then boots ready: no download,
no first-run import.

The `source` block is Azure only because it has to be *something*; the
provisioners are the portable part. Swap in Hyper-V, vSphere or QEMU and keep
`scripts/provision-skrog.ps1` as-is. Whatever the builder, the VM needs
**nested virtualization** — WSL2 will not start without it.

## ansible/ — converge an existing host

```
ansible-galaxy collection install ansible.windows
ansible-playbook -i inventory.ini contrib/ansible/playbook-example.yml
```

The role checks WSL2 and **fails with the fix** rather than rebooting your fleet
on its own initiative, installs the pinned `skrog.exe` only when the version
differs, imports the engine if it is not there, applies Skrog and engine
settings through the validated `skrog config` surface (not raw `daemon.json`),
registers autostart, waits with `skrog healthcheck --wait`, optionally
pre-pulls a pinned image list, and finishes on `skrog doctor` — failures fail
the play, warnings do not.

## The one thing neither of them does: auto-logon

WSL2 cannot start from a Windows service, so an unattended runner needs an
interactive session, which means auto-logon, which means a credential stored on
the machine. Neither the template nor the role configures it: baking a password
into an image or handing one to a role is exactly the decision that should be
yours and visible.

Do it deliberately — [Sysinternals Autologon](https://learn.microsoft.com/sysinternals/downloads/autologon)
stores it LSA-encrypted rather than in plain registry text — following
[docs/auto-logon-runner.md](../docs/auto-logon-runner.md), and verify with:

```
skrog runner check
```

which reports on auto-logon, autostart, power/sleep settings and engine health
in one verdict. It compares the configured account without ever printing it, and
never reads the stored password.

## See also

- [docs/ci-runners.md](../docs/ci-runners.md) — running jobs against the engine
  (GitHub Actions, GitLab, Testcontainers, act, gitlab-ci-local)
- [docs/auto-logon-runner.md](../docs/auto-logon-runner.md) — the unattended-host playbook
- [docs/air-gap.md](../docs/air-gap.md) — `skrog bundle` and `install --offline`
- [docs/declarative-install.md](../docs/declarative-install.md) — `skrog install --config`, if you
  would rather converge from a file than from role variables
