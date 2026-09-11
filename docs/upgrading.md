# Staying current

Three things have to stay current, and they are not independent:

```
$ hawser upgrade
app     0.3.0   -> 0.4.0 available
engine  29.8.0  (current)
cli     29.8.0  (current)

app:     download from https://github.com/hawserhq/hawser/releases

note: the engines `hawser engine upgrade` can reach are pinned in this build's
manifest, so hawser 0.4.0 may offer newer engines than the 29.8.0 listed here —
upgrade hawser first, then re-check
```

## The note is the point

The convenience of one answer is the small part. The reason this command
exists is in that last paragraph: **the engines `hawser engine upgrade` can
install are compiled into the hawser binary you are running.**

So "engine: current" is only ever true *of this build*. A newer engine can
require a newer hawser first — and without being told, someone on an older
hawser runs `engine upgrade`, is told they are already current, and is wrong.
The note appears only when the app is actually behind, because a warning
printed every time is a warning nobody reads.

Same verb at two scopes:

| | |
|---|---|
| `hawser upgrade` | everything — app, engine, bundled docker CLI |
| `hawser engine upgrade` | just the engine ([details](engine-upgrade.md)) |

## What it applies, and what it only reports

| stream | |
|---|---|
| app | **reported, never applied** — download the release zip; a signed installer is coming ([#77](https://github.com/hawserhq/hawser/issues/77)) |
| engine | applied — `hawser engine upgrade`, reversible with `hawser engine rollback` |
| bundled CLI | applied — `hawser cli install` |

A running `.exe` cannot cleanly replace itself on Windows, and once there is a
signed distribution channel it owns that path properly — self-replacement earns
its complexity last, if ever.

It always shows you the plan and asks first:

```
will run:
  hawser cli install      29.7.2 -> 29.8.0
  hawser engine upgrade   29.7.2 -> 29.8.0

proceed? [y/N]
```

`--dry-run` prints that and stops. `--check` reports without even planning.
`--yes` skips the question, for runners. `--json` implies `--check`, because
there is no way to ask a question in JSON.

The CLI goes first: it is a file copy costing no downtime, where the engine
upgrade stops and restarts the engine and takes minutes. So a failed engine
upgrade leaves a machine with the CLI already current rather than nothing done,
and the two are independent — nothing is ever half-applied.

## Nothing checks on its own

There is no auto-update, no background poll, and no "latest" resolved at
install time. This runs when you run it.

One outbound request is made, to the GitHub releases API, for the app version.
It sends nothing but the request: no machine identifier, no version, no
telemetry. The server learns an IP asked, which is what downloading anything
would tell it anyway.

## Air-gapped machines

`--offline` skips that request entirely:

```
hawser upgrade --offline
```

The engine and CLI answers are still real, because both manifests are compiled
into the binary — there is nothing to fetch. The app line reports **unknown**,
not "current": a check that did not happen must never read as a check that
passed. See [air-gapped installs](air-gap.md).

## Scripting it

```
hawser upgrade --json
```

Exit codes follow `hawser cli status`, so either can gate a script the same
way: **0** nothing to do (or everything applied), **3** something can be
upgraded, **1** error, **2** usage. The 3 is reported by `--check` and
`--dry-run`; a plain run that applies successfully exits **0**.

Each stream carries a `status` of `current`, `available`, `unknown` or
`not-installed`. `unknown` and `not-installed` are distinct from `current` on
purpose, and neither counts as an upgrade — a component you never installed is
not out of date, and a component that could not be checked is not up to date.

## In the tray

"Check for updates" runs the same command and puts the answer in its tooltip.
It opens the releases page only when something is actually available; being
told "everything is up to date" without a browser window is the common case,
and the better one.
