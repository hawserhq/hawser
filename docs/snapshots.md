# Engine snapshots

Save and restore the whole engine state — every image, container and volume — as
a named, checksummed archive. Docker Desktop has no engine-state snapshotting;
this is the "set up a dev environment, snapshot it, trash it during a risky test,
restore it in seconds" answer.

```
skrog snapshot save before-upgrade   # capture the current state
skrog snapshot list
skrog snapshot restore before-upgrade --yes
skrog snapshot delete before-upgrade
```

Only Skrog's own distro is ever touched.

## Runners: a golden snapshot as the clean slate

CI runners want two contradictory things: a **clean engine per job** and **no
image pulls**. A golden snapshot gives both. Bake it once, reset to it before
each job:

```
skrog install --headless
docker pull node:20 && docker pull mcr.microsoft.com/devcontainers/base:ubuntu   # or: skrog prewarm images.txt
skrog snapshot save golden

# in the runner's before_script / job setup:
skrog reset --to golden
```

`skrog reset --to` is `snapshot restore` with the interactive guards implied —
no `--yes`, no running-container check — because a job script has already
decided. It reports how long the cycle took (`--json` → `ms`), which is the
number to budget against: today that is the archive checksum pass plus
`wsl --unregister` + `wsl --import` of the tar, so a few-GB golden lands in
tens of seconds, not the single digits the ideal wants. The fast path to get
there is exporting the golden as a VHDX (`wsl --export --vhd`) and using
`wsl --import-in-place`, which turns the restore into a file copy — tracked as
the follow-up on
[#142](https://github.com/wslkit/skrog/issues/142) with real timings from
the acceptance suite, which resets to a fresh snapshot on every run.

## How it works

- **save** is a `wsl --export` of the engine distro to `snapshots/<name>.tar` in
  the state dir, plus a JSON sidecar recording the created time, engine version,
  size, and SHA-256. The export briefly stops the engine for a consistent image;
  the supervisor brings it back.
- **restore** verifies the archive against its recorded checksum, then replaces
  the distro (`wsl --unregister` + `--import`) and starts the engine on the
  restored state. Because it verifies first, a corrupt or tampered archive is
  refused rather than importing over a good engine with nothing.

## Safety

- `save` and `restore` refuse while containers are running (pass `--force` to
  override), so you do not snapshot or replace an engine mid-build.
- `restore` is **destructive** — every image, container and volume not in the
  snapshot is lost — so it requires `--yes`.
- Restore cooperates with the supervisor: it pauses it, replaces the distro, and
  resumes it on the new state, rather than racing the health loop.

## Notes

- Snapshots are full engine images, so they are large (hundreds of MB to GB) —
  `skrog snapshot list` shows sizes. Delete ones you no longer need.
- Pairs with reproducible installs: a `skrog.lock` pins *which engine*, a
  snapshot captures *what is in it*.
