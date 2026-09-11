# Design: per-workspace engines

Status: **proposal** for [#140](https://github.com/zcsizmadia/hawser/issues/140)
— the CLI shape and the trade-offs, before code. Owner review decides.

## The capability

Today Hawser runs one engine (`hawser-engine`) that every project shares. The
ask is "this workspace uses *its own* engine": isolated images, containers and
volumes per project — for security (a poisoned image in project A cannot be
`docker run` from project B), for hygiene (no cross-project image sprawl), and
for reproducibility (project A pins engine 29.7, project B needs 29.9). Docker
Desktop is structurally one global engine; this is a capability it cannot offer.

Hawser is already most of the way there: `install --distro X --state-dir Y`
creates a fully independent engine, and the acceptance suite runs one beside a
real install on every run. What is missing is making that first-class:
naming, switching, discovery, and a workspace file that selects it.

## Decision 1 — one supervisor + pipe per engine (not a multiplexer)

Two models were on the table:

- **A. One supervisor per engine** (today's model, N times). Each engine has its
  own state dir, pipe, supervisor process, docker context. Isolation is total
  and everything that exists today keeps working unchanged.
- **B. One supervisor multiplexing N engines.** One process, one pipe with a
  routing layer, shared idle logic.

**A.** Model B saves one small process per engine and buys a routing layer,
coupled lifecycles (one crash takes down all pipes), and a second implementation
of idle-stop, hooks, audit and health that must stay in step with the first.
Model A reuses the supervisor, reconciler, idle-stop, hooks, audit log, doctor
and the entire `--json` contract per engine for free. The cost — a few MB of RAM
per idle supervisor — is noise next to the engine VMs themselves.

## Decision 2 — engines and remotes share one namespace: *targets*

`hawser remote add desk` already creates docker context `hawser-desk`. A local
per-workspace engine `proj-a` would naturally be `hawser-proj-a`. Rather than two
prefixes and two switch commands, both are **targets**, switched by one verb:

```
hawser use desk          # a remote (registered with `hawser remote add`)
hawser use proj-a        # a local per-workspace engine
hawser use local         # the default shared engine (context: hawser)
```

Names are unique across remotes and engines; `local` is reserved (it already is
for remotes). `hawser remote use` stays as an alias so nothing documented
breaks. The VS Code extension's status bar shows the current target either way.

## Layout

An engine named `<name>` is a complete, independent Hawser install:

| Piece | Default engine | Engine `<name>` |
| --- | --- | --- |
| state dir | `%LOCALAPPDATA%\Hawser` | `%LOCALAPPDATA%\Hawser\engines\<name>` |
| distro | `hawser-engine` | `hawser-<name>` |
| data (VHDX) | `<state>\distro` | `<state>\engines\<name>\distro` |
| pipe | `\\.\pipe\docker_engine` (or Hawser's own) | `\\.\pipe\hawser-<name>` |
| docker context | `hawser` | `hawser-<name>` |
| config, snapshots, audit, logs | per engine, in its state dir | same |

Nothing is shared between engines except the rootfs download cache (the same
pinned tarball, verified once, imported N times).

## CLI shape

The instance verbs live under `target`, not `engine`. `hawser engine` already
ships as the **version** dimension — `engine list` is "which dockerd releases
can this build install", and `engine upgrade|rollback` move between them — so
`engine list` cannot also mean "which engines do I have". Decision 2 already
named the right noun; these are targets.

```
hawser target create <name> [--engine-version <v>] [--locked hawser.lock]
hawser target list [--json]              # remotes + local engines + `local`; state, disk, current
hawser target remove <name> [--yes]      # unregister distro, delete state, drop context
hawser use <name>|local                  # switch docker (and Dev Containers) to it
hawser status --state-dir …              # everything else already works per engine

hawser engine list|upgrade|rollback      # unchanged: the VERSION of the current target
```

Reading the two nouns together: a *target* is which engine you are talking to;
the *engine* commands are which dockerd version that one runs. `target list` is
also the only sensible home for Decision 2's shared namespace, since remotes
are not engines anyone can create.

**Consequence:** `engine upgrade`, `engine rollback` and `config set engine.*`
become scoped to the current target. That keeps `hawser use` the single switch
anybody has to learn, and it is the least surprising rule when more than one
engine exists.

`target create` is `install` into the engine's state dir with the engine's
distro name and pipe, honoring the same pins (`--engine-version`, `--locked`,
`--offline` bundle). `target remove` is `uninstall` scoped to that engine. Both
are thin: the work is in `provision`, which already takes these as options.

## Workspace file

`hawser.yaml` (declarative install, #69) gains one key:

```yaml
engine: proj-a          # this workspace uses the engine named proj-a
engine-version: 29.7.2  # …pinned, as today
```

`hawser install --config hawser.yaml` in that directory creates the engine if
missing and switches to it; the VS Code extension (hawser-vscode #8) reads the
key on workspace open and runs `hawser use`. A workspace without the key uses
`local`, so nothing changes for anyone who does not opt in.

## Lifecycle

- **Start on demand, not at logon.** The default engine keeps its logon
  autostart. A per-workspace engine's supervisor starts when you `hawser use` it
  (or run `hawser start --state-dir …`), and idle-stops like any engine — so ten
  project engines cost RAM only while one is in use. Autostart for a named
  engine is opt-in (`hawser autostart enable --engine <name>`), which means the
  Run key holds one entry per autostarted engine, each pointing at its state dir.
- **Idle-stop per engine** is already how the supervisor works; nothing to add.
- **Docker Desktop coexistence** is unchanged: the default engine's pipe logic
  handles it; named engines always use their own pipe.

## Cost, honestly

Each engine is a WSL distro with its own VHDX: hundreds of MB of base plus
whatever it pulls, per engine. RAM is fine (idle-stop); **disk adds up**, which
is why `hawser prune` and the sparse-VHDX default (#145) matter more once this
lands. `target list` shows per-engine **and total** disk so the sprawl is visible.

Doctor's free-space floor (`disk.warn-below`, #145) needs to follow. It checks
the data volume for *the* engine today; with N engines on one drive the honest
question is the sum, and disk sprawl is precisely the failure this feature
invites. Left as-is, the first symptom would be a full drive.


## Sharing

Two workspaces that name the same engine share it — that is simply the default
engine's behavior with a name. There is no per-engine ACL; isolation is between
engines, not between users of one engine (the pipe ACL is per user as today).

## The tray

The tray is one status dot with a six-item cap (PLAN §03) and today it shows
*the* engine. With N engines it has to mean something specific: the dot follows
the **current target**, and the tooltip names it. That needs no new menu item
and no new decision surface. Anything richer — a list of engines with per-engine
state — is a control panel, which is the scope tripwire.

It also reads the supervisor's published state from one state dir (#192), so
per-engine state dirs mean reading the current target's.

## Phasing

1. **`target create|list|remove` + `use`** — the model, on top of what
   `provision` already supports. Ships with `--json` and e2e (the suite already
   proves a second engine beside a real one; it gains a `hawser use` round trip
   that switches back, since it must not leave the machine's context changed).
2. **`hawser.yaml` `engine:`** and the extension picking it up (hawser-vscode #8).
3. **Per-engine autostart policy**, `target list` disk accounting, and
   doctor's free-space floor across every engine.

## Open questions for review

Still the owner's to settle; each carries a recommendation and the reasoning
behind it.

- **Version for a new target: the default engine's, or the build's?**
  *Recommend the build's default, like `install`.* Inheriting the shared
  engine's version makes the result depend on invisible local history — the
  same `target create` on two machines, or before and after an upgrade, would
  produce different engines. `hawser.yaml` already has `engine-version:` for
  anyone who wants to pin deliberately.
- **Should `hawser use` export `DOCKER_HOST`?**
  *Recommend no, firmly.* A process cannot set a variable in its parent shell,
  so this would have to become something you `eval` — a different and worse
  UX. It would also bypass the context that Dev Containers and the extension
  read, leaving the two disagreeing about which engine you are on, which is the
  confusion this feature exists to remove. `DOCKER_CONTEXT` covers scripts.
- **Cap the number of engines?**
  *Recommend visibility, not a cap* — provided visibility includes the total
  disk and doctor's floor accounts for every engine (see Cost above). A cap
  would be an arbitrary number that a fleet eventually needs to exceed.
