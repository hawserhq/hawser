# Housekeeping: disk

Runners die of full disks. Two levers, and a warning that fires before docker
starts erroring on its own.

## `hawser prune`

Reclaims disk on the engine through whatever docker currently targets — the
local engine, or the remote selected with `hawser remote use`:

```
hawser prune                         # stopped containers + dangling images
hawser prune --until 168h            # keep anything from the last week
hawser prune --all --build-cache     # the full sweep after a job
hawser prune --volumes               # also unused volumes (they hold data; off by default)
```

Steps run in the order that frees the most — containers first, so their images
become unused, then images, then volumes and the BuildKit cache when asked. A
failed step never stops the others; the exit code says whether all succeeded,
and `--json` reports bytes reclaimed per step (see
[cli-json.md](cli-json.md)). A runner's post-job step or a scheduled task is
the natural caller:

```powershell
# after_script / post-job
hawser prune --all --until 168h
```

A `prune.schedule` setting that lets the supervisor run this on a cadence is
the follow-up on [#145](https://github.com/zcsizmadia/hawser/issues/145).

## The free-space warning

`hawser doctor` fails below 2 GiB free on the engine data volume and warns below
a floor you can set:

```
hawser config set disk.warn-below 20GB     # default 5GiB; GB/GiB/MB/… accepted
```

Raise it on a runner with a small disk so a filling volume is flagged — with
`hawser prune` named as the remedy — before pulls start failing deep inside
dockerd.

## Giving space back to the host: `hawser compact`

`hawser prune` frees space *inside* the engine. It does not shrink the file on
your drive: a WSL2 distro's `ext4.vhdx` only ever grows, so deleting 50 GB of
images leaves the VHDX exactly as large as it was. Two separate things have to
happen to get that space back, and `hawser compact` does both:

1. **`fstrim` inside the engine** — the guest tells the disk which blocks it
   has freed. Without this, there is nothing for step 2 to find.
2. **`CompactVirtualDisk` on the file** — the disk stops reserving them.

```
hawser compact --restart
hawser-engine: 5.0 GiB reclaimed (14.0 GiB to 9.0 GiB)
```

**No administrator rights are needed.** Compaction of an *unattached* disk is
unelevated, which is what makes this work on Windows Home, where the Hyper-V
module — and therefore `Optimize-VHD` — does not exist.

### The engine has to be stopped, and so does everything else

The engine cannot be compacted while its disk is attached, so `compact` stops
it (`--restart` brings it back). The awkward part is not Hawser's: **the WSL
utility VM keeps every distro's disk open for as long as *any* distro is
running.** Docker Desktop's distros count. So when something else is up,
`compact` refuses and names it:

```
hawser: ext4.vhdx is still open in Ubuntu and docker-desktop
  The WSL utility VM keeps every disk open while any distro runs, so the
  disk cannot be released while those are up. Stop them and re-run;
  Hawser will not stop distros it does not own.
```

Exit code **11**. Hawser will not run `wsl --shutdown` to get around this —
that would kill whatever you had running in another window, containers
included, and that is your call. `hawser compact --dry-run` tells you in
advance whether it would refuse, and who is holding the disk.

With nothing else running, the utility VM releases the disk on its idle timer,
about a minute after the last distro stops (measured at ~66 s against WSL
2.7.8.0, whose `vmIdleTimeout` defaults to 60 s). `compact` waits 90 s for
that; raise it with `--wait 3m` on a slow machine.

### What the numbers mean

`reclaimedBytes` — the difference in the file's **size on disk** — is the only
honest measure. `fstrim` also prints a byte count, and it is wildly
misleading: it reports the free extent of the whole virtual disk, so on the
1 TB default disk it can read as a terabyte "trimmed". `hawser compact` labels
that figure every time it shows it, and `--json` names the field `offeredBytes`
rather than anything that sounds like a result.

### Why not sparse mode

WSL can mark a disk sparse (`wsl --manage <distro> --set-sparse true`) so it
hands space back on its own, which sounds like it makes compaction
unnecessary. Hawser does **not** turn it on, for two reasons:

- Microsoft put automatic sparse mode behind `--allow-unsafe` in WSL 2.5.6
  after reports of data corruption
  ([WSL#12103](https://github.com/microsoft/WSL/issues/12103)).
- It does nothing for a disk that has already grown, which is the disk you
  actually want to shrink.

If you want it anyway, that is a deliberate choice to make yourself, with the
engine stopped.

*The mechanics above — unelevated compaction, the utility VM's release timing,
and `fstrim`'s misleading figure — come from the research in
[zcsizmadia/wsldisk](https://github.com/zcsizmadia/wsldisk), which does this
for any WSL distro (and for Docker Desktop's own `docker_data.vhdx`).*
