# Engine snapshots

Save and restore the whole engine state — every image, container and volume — as
a named, checksummed archive. Docker Desktop has no engine-state snapshotting;
this is the "set up a dev environment, snapshot it, trash it during a risky test,
restore it in seconds" answer.

```
hawser snapshot save before-upgrade   # capture the current state
hawser snapshot list
hawser snapshot restore before-upgrade --yes
hawser snapshot delete before-upgrade
```

Only Hawser's own distro is ever touched.

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
  `hawser snapshot list` shows sizes. Delete ones you no longer need.
- Pairs with reproducible installs: a `hawser.lock` pins *which engine*, a
  snapshot captures *what is in it*.
