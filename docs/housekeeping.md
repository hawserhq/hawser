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

## Giving space back to the host: sparse VHDX

Freed blocks inside the engine's virtual disk do not shrink the VHDX file on
the host by themselves. WSL 2.x can mark a distro's disk **sparse**, so it
returns freed space automatically:

```
wsl --manage hawser-engine --set-sparse true
```

Run it once, with the engine stopped (`hawser stop`). Whether this needs an
elevated shell varies by WSL version — try it unelevated first; if it refuses,
it needs an administrator terminal. Making `hawser install` set this by default
(with `--no-sparse` to opt out) is tracked on
[#145](https://github.com/zcsizmadia/hawser/issues/145) pending that check on a
throwaway distro. Full VHDX compaction (`Optimize-VHD`) is the elevated path and
lives with [#64](https://github.com/zcsizmadia/hawser/issues/64).
