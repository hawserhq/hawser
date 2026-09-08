# Declarative install (`hawser.yaml`)

Describe a whole install in one YAML file, check it into your provisioning repo,
and install a fleet of runners from it instead of a pile of flags:

```
hawser install --config hawser.yaml
```

Re-running against an existing install **converges** it (idempotent) — it skips
provisioning and re-applies the settings — so the same file drives both the
first install and every later change. To capture an existing machine as a
starting point:

```
hawser config export > hawser.yaml
```

> Docker Desktop paywalls Settings Management behind Docker Business. This is
> free, and it is just a file.

## Schema

Every field maps to an existing flag or config key; nothing new is invented, and
unknown fields fail loudly rather than being silently ignored.

```yaml
# Install-time (used only on a fresh install):
distro: hawser-engine          # --distro
data-dir: D:\hawser            # --data-dir
engine-version: 29.7.2         # --engine-version

# Settings (applied on install and on every converge):
idle-timeout: 30m              # or "off"
autostart: true               # start the supervisor at logon

engine:                        # engine daemon.json keys (see `hawser config`)
  registry-mirrors: https://mirror.corp.example.com
  insecure-registries: registry.internal:5000
  log-opts: max-size=10m,max-file=3

hooks:                         # lifecycle scripts (see docs/hooks.md)
  post-start: C:\ProgramData\hawser\login.cmd

integrations:                  # distros to wsl-integrate
  - Ubuntu
```

All fields are optional: an absent field is left at its default or untouched, so
a partial file is valid.

## CI / fleet example

A GitHub Actions self-hosted runner image, provisioned in one step:

```powershell
# In the runner's setup script:
hawser install --config C:\provisioning\hawser.yaml --headless --no-autostart
hawser start
docker run --rm hello-world
```

- `--headless` never prompts.
- `--no-autostart` suits a runner started by a service manager rather than at
  logon; drop it (or set `autostart: true`) for interactive machines.
- Flags win over the file, so one shared `hawser.yaml` can be specialized per
  host without editing it.

## Pinning the engine (`hawser.lock`)

`hawser.yaml` describes *what to configure*; `hawser.lock` pins *exactly which
engine* — version, rootfs URL, and SHA-256 — so every machine runs a verified,
identical engine:

```
hawser lock > hawser.lock            # capture this build's pinned engine
hawser install --locked hawser.lock  # reproduce it, refusing on any checksum mismatch
```

Check both into your provisioning repo. `--locked` sets the rootfs the same way
the embedded manifest does, so the install stays checksum-verified end to end.

