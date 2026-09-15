# Using wslc as Skrog's engine

**Experimental.** `skrog install` will not set this up for you, and it is not
what a normal install uses. Tracked in
[#316](https://github.com/wslkit/skrog/issues/316).

[wslc and Skrog](wsl-containers.md) explains what a `wslc` session is and why
nothing on your machine can otherwise reach the Docker engine inside it. This
page is the other half: how to point Skrog at one, what you get, what you give
up, and how to decide whether that trade is worth taking.

The short version, if you only read one paragraph: **take this backend if your
source tree lives on a Windows drive, and leave it alone if you need a pinned
engine.** Everything below is the detail behind those two sentences.

## Quick start

All you need is WSL 2.9.3+:

```powershell
skrog proxy --engine wslc
```

No session to create first and no agent to build: Skrog starts the container
session if none is running, and places the guest agent that ships beside
`skrog.exe`. The first run takes a few seconds longer because the session VM
has to boot.

In another shell:

```powershell
docker --context skrog-wslc ps
docker --context skrog-wslc version    # Server: 25.0.3, Microsoft Azure Linux 3.0
```

Ctrl-C stops the bridge and releases everything it held.

### It runs alongside a normal install

The two backends **coexist**. The wslc bridge serves its own pipe
(`\\.\pipe\skrog_wslc`) and its own docker context (`skrog-wslc`), so a normal
Skrog install keeps `\\.\pipe\docker_engine` and the `skrog` context, and
plain `docker` keeps reaching the engine it always did.

Switching is the vocabulary you already have:

```powershell
docker context use skrog-wslc     # Microsoft's engine, in a session VM
docker context use skrog          # your own pinned engine, in a distro
docker context ls
```

The cost of running both at once is a second VM, roughly 820 MB. Stop the
bridge when you are not using it.

### Useful flags

| flag | why |
|---|---|
| `--pipe '\\.\pipe\docker_engine'` | serve somewhere else — including the default pipe, which is reasonable on a machine with no distro install |
| `--no-context` | do not create or update the `skrog-wslc` context; select with `$env:DOCKER_HOST` instead |
| `--state-dir <path>` | where `config.json`, `policy.yaml` and `audit.log` live |
| `--agent <path>` | a specific `skrog-agent` build; by default the one shipped beside `skrog.exe`, falling back to the engine distro's |

## How it works

Skrog does not wrap or drive the `wslc` CLI. The CLI is used exactly twice —
once to find the session, once to stream a guest agent into it — and never
again while the bridge is up:

1. **Find the session**, or start one. `wslc system session list`, falling back
   to the CLI's own default session — created on the spot if nothing is
   running. That last part has a wrinkle worth knowing if you script against
   the CLI: `system session run` *with* `--session` requires the session to
   already exist, and only a bare call without the flag creates it.
2. **Place the agent.** The `skrog-agent` binary and a per-run shared secret are
   streamed over `wslc system session run` on stdin, byte-exact and verified by
   sha256 in the guest.
3. **Talk over vsock.** From then on Windows dials the agent directly over
   AF_HYPERV — around 4 ms — and the agent relays to `/var/run/docker.sock`.
   No process spawn per request, and no `wslc` in the data path.

The VM GUID needed for that dial is discoverable from the registry without
elevation, so none of this needs an administrator.

Two things run alongside the relay:

- **A lease.** A session VM idle-terminates when nothing is using it, which
  would take the agent and your containers with it. Skrog holds one long-lived
  process in the session's root namespace, which takes WSLC's own activity
  reference — the same mechanism a running container uses. It costs no measurable
  CPU, and it is bounded so an orphaned bridge cannot pin a VM up forever.
- **A port watcher.** It follows the engine's `/events` stream and publishes or
  withdraws a host listener as containers with `-p` come and go.

## What works

Verified against a live session on WSL 2.9.11.0 / Windows 10 22H2:

| | |
|---|---|
| Container lifecycle | `run`, `create`, `start`, `stop`, `rm` |
| Streaming | `exec`, `logs`, `logs -f`, `attach`, `stats`, `events` |
| Inspection | `ps`, `inspect`, `images`, `version`, `system df` |
| Images | `pull`, `push`, `commit`, `save`, `load`, `tag` |
| Build | `build` and `buildx` |
| Data | `docker cp`, networks, named volumes |
| **Compose** | including healthchecks, `depends_on` conditions, named volumes and published ports |
| **Testcontainers** | including the Ryuk reaper |
| **Windows-folder bind mounts** | `-v C:\src\app:/app`, over virtiofs |

The last three are the ones that matter, because they are what the `wslc` CLI
cannot do:

- **Ryuk** needs the engine socket bind-mounted into a container. Skrog
  translates a `\\.\pipe\...` source to the session's own
  `/var/run/docker.sock`, which is the only thing a pipe can mean inside a Linux
  container. The `wslc` CLI cannot express that mount at all.
- **Published ports** reach Windows. `dockerd` publishes them inside the session
  VM, and the relay that would carry them to the host is driven from the Windows
  side — so a plain socket relay gets you a port that exists nowhere you can
  reach. Skrog runs its own relay and binds **the address your `-p` asked for**;
  `wslc`'s own relay only ever binds `127.0.0.1`.
- **Windows folders**, which have their own section below.

## What does not work

| | why |
|---|---|
| **UDP published ports** | the relay is a stream transport |
| **Engine pinning** | Microsoft ships the engine; `skrog lock` has nothing to record |
| **A dedicated session** | the shipped CLI cannot create a named session, so Skrog shares the default one |
| `compact`, `snapshot`, `relocate`, `wsl-integrate`, `gpu`, `engine upgrade` | these operate on Skrog's own distro and have no meaning here |
| `skrog install`, `serve`, `supervise`, `status`, `doctor` | not wired to this backend yet ([#335](https://github.com/wslkit/skrog/issues/335)) |

Sharing the default session has a practical consequence worth stating: anything
you run with the `wslc` CLI by hand lands in the same VM as your containers, and
`wslc system session terminate` will take your containers down with it.

## Behaviour differences from the distro backend

Things a `docker` user hits here that they do not hit on `skrog-engine`. All of
it measured on WSL 2.9.11.0 / Windows 10 22H2 rather than inferred from the
`wslc` CLI's surface, which matters — several differences people expect from the
CLI do not exist over the engine socket.

### The one that will actually bite you: API 1.44

| | wslc session | skrog-engine |
|---|---|---|
| engine | 25.0.3 (Microsoft build, `GitCommit f417435`) | 29.8.0 |
| API | **1.44** | 1.56 |
| minimum API | 1.24 | 1.24 |

Docker CLI 29.x's *minimum* supported API is 1.44, so the bundled CLI negotiates
down and works — **with zero headroom**. Anything that requires 1.45 or newer
fails rather than degrades: newer BuildKit attestation and SBOM flags, some
Compose fields, `docker debug`.

`skrog upgrade` cannot fix this. `wsl --update` might, silently, in either
direction.

### No cross-architecture execution

`--platform` itself works — the daemon selects and pulls the requested platform
happily:

```
docker pull --platform linux/arm64 busybox      # succeeds
docker image inspect busybox --format '{{.Architecture}}'   # arm64
```

What is missing is qemu/binfmt, so a foreign binary cannot *run*:

```
docker run --rm --platform linux/arm64 busybox true
exec /bin/true: exec format error
```

A `buildx --platform linux/arm64` build gets through `FROM` and dies at the
first `RUN` for the same reason. So multi-arch builds are out, but pinning a
platform to the host's own architecture is fine.

A trap worth knowing, because it is easy to do to yourself: pulling a foreign
platform **overwrites the local tag**. After the `docker pull --platform
linux/arm64 busybox` above, plain `docker run busybox` fails with
`exec format error` until you re-pull the native one.

### What works here that the `wslc` CLI cannot do

The CLI's limits are the CLI's, not the engine's. Over the socket, all of these
work and are verified:

| | |
|---|---|
| `--network host` | exits 0 and joins the **session VM's** network namespace — not Windows' |
| `--privileged`, `--pid host`, `--cap-add` | all supported by dockerd and all reachable here |
| `docker network create` | bridge, ipvlan, macvlan, overlay and null drivers are present |
| guest-path binds (`-v /var/run/docker.sock:…`, `/tmp`) | the basis of Ryuk and docker-in-docker |
| `buildx build -o type=local` | the artifact lands on Windows |

`--network host` deserves the caveat: "host" is the session VM, so it gets you
the VM's interfaces, not the Windows host's. Published ports do not apply to a
container in host mode, and nothing in that namespace is reachable from Windows
except through Skrog's relay.

### Published ports bind what you asked for

`wslc`'s own relay only ever binds `127.0.0.1`. Skrog runs its own relay and
honours the address in `-p`:

```
docker run -d -p 0.0.0.0:18411:80 busybox httpd -f -p 80
netstat -an | findstr 18411
  TCP    0.0.0.0:18411    LISTENING
  TCP    [::]:18411       LISTENING
```

Reachable from the host's LAN address, not just loopback. TCP only — see
[What does not work](#what-does-not-work).

### Bind-mount metadata is virtiofs-flavoured

Files under a Windows share present as `-rwxrwxrwx root root`, and `chmod` is
silently a no-op:

```
-rwxrwxrwx  1 root root  3 /m/f.txt
chmod 600 /m/f.txt
-rwxrwxrwx  1 root root  3 /m/f.txt
```

Same class of surprise as `drvfs` on a distro, and it breaks anything that
insists on strict permissions — an `ssh` key, or a tool that refuses a
world-writable config.

### Registry credentials

`X-Registry-Auth` passes straight through, so `docker login` works and its state
lives in the Windows CLI's own config, exactly as on the distro backend. WSLC
keeps a *separate* credential store for `wslc registry login`; Skrog's pipe does
not read it, and logging in with one does not log you in on the other.

### GPU

The guest daemon has CDI enabled, so `--gpus` maps to `DeviceRequests` → CDI
inside dockerd 25. The runtime list is `runc` and `io.containerd.runc.v2` only —
there is no `nvidia` runtime, and there does not need to be. **`skrog gpu` is
for Skrog's own distro and will not help here**; the CDI spec comes from the
WSLC guest image.

### Windows 10 is fine

Microsoft's docs say Windows 11 22H2+. All of the above was measured on **Windows
10 22H2 (19045)**. Nothing in Skrog gates on the OS build, and nothing needs to.

## Windows folders, and the reason to use this backend at all

A session has no `/mnt/c`. Each Windows folder is instead its own **virtiofs**
share mounted at `/mnt/{GUID}`, and the GUID changes when the VM restarts.

Skrog builds a share table: the first bind from a drive starts one
`skrog-share-<drive>` holder container that mounts the drive and keeps the share
open, and the bind source is rewritten to the share's guest path. One holder per
drive, not one per bind — a Compose project with a dozen binds under `C:` starts
one. They are removed when the bridge stops, and a cached share is probed before
use so a VM restart re-establishes it rather than handing `dockerd` a path that
silently mounts empty.

Editing a file on Windows is visible in the container immediately, so the
ordinary edit-and-reload loop works.

### Measured: virtiofs versus 9p

The same Windows folder, the same `alpine`, bind-mounted through each backend
and timed **inside** the container. Mount types confirmed from `/proc/mounts`.

| | wslc (virtiofs) | distro (9p) | ratio |
|---|---|---|---|
| write 256 MB (`conv=fsync`) | **216.1 MB/s** | 114.7 MB/s | **1.9×** |
| read 256 MB (warm) | **800.2 MB/s** | 186.6 MB/s | **4.3×** |
| create 1000 files | **1.12 s** | 2.26 s | 2.0× |
| `ls -l` 1000 files | **0.27 s** | 0.71 s | 2.6× |
| read 1000 files | **1.20 s** | 2.67 s | 2.2× |
| delete 1000 files | **0.79 s** | 1.57 s | 2.0× |

So: **roughly 2× write, 4× read, 2–2.6× metadata.** These reproduce an
independent earlier run on the same host to within a few percent
([#326](https://github.com/wslkit/skrog/issues/326)), which is the main reason
to trust them.

The read row is a warm read — reads follow a write in the same VM and the guest
page cache helps both sides. That is the common case for a source tree, so it is
the number worth quoting, but it is not a cold-cache figure.

This is felt on exactly the workloads people complain about: `npm install`, a
gradle build, a large `docker build` context.

## Policy and audit

The engine socket sits behind `wslcsession`, which is where WSL enforces an
administrator's registry allowlist. Talking to the socket directly would bypass
that, so Skrog reads **WSL's own Group Policy configuration**
(`HKLM\Software\Policies\WSL`) and applies it at the pipe. The aim is that a
deployed allowlist means the same thing through this pipe as through `wslc` —
not that Skrog invents a second policy language next to it.

| policy | where Skrog applies it |
|---|---|
| `WSLContainerRegistryAllowlist` | `docker pull`, and the image named by `run`/`create` |
| `AllowWSLContainerPrivileged` | `--privileged` on create |
| `WSLContainerRegistryAllowlist` | `docker build` — **refused outright** while an allowlist is active |

Skrog's own [`policy.yaml`](policy.md) and the [audit log](audit.md) apply on
top. The audit log is worth turning on here for its own sake: it records every
container-affecting call with its outcome, including each denial and the reason,
which is a record WSLC itself does not keep.

A policy that cannot be read is treated as a failure, not as "no policy" — the
bridge refuses to start. Guessing in the permissive direction is how a bypass
ships.

### Verified against a real deployment

With `WSLContainerRegistryAllowlist = contoso.azurecr.io` and
`AllowWSLContainerPrivileged = 0` actually deployed:

| | |
|---|---|
| `docker pull busybox` | 403, naming the allowlist and what it permits |
| `docker pull contoso.azurecr.io/…` | passes the gate and reaches the network |
| `docker run --privileged` | 403, naming `AllowWSLContainerPrivileged` |
| the same image without `--privileged` | runs |
| `docker ps`, `images`, `version` | untouched |
| `wslc pull busybox` | `WSLC_E_REGISTRY_BLOCKED_BY_POLICY` |

That last row is the point: Microsoft's own CLI refuses the same image for the
same reason, so the two agree rather than Skrog inventing its own answer.

### Builds: Skrog is stricter than WSL here

Worth being straight about, because it is the one place the two do not match.

`wslc image build` is **not** refused. It runs, and the allowlist is enforced
per source *inside* BuildKit, so a blocked base image fails with
`source "docker-image://docker.io/library/busybox:latest" denied by policy`.
`wslcsession` can do that because it drives BuildKit directly and attaches a
source policy to the solve request.

That policy is client-side, not daemon configuration — a build sent straight to
the session's `dockerd` inherits none of it, which is measured rather than
assumed. Matching it at the pipe would mean rewriting protobuf inside a hijacked
HTTP/2 stream. Until that exists, Skrog refuses the build instead of letting it
through.

The refusal covers `/session` and `/grpc` as well as `/build`: buildx has been
the default `docker build` since Docker 23 and never touches `/build`, so gating
only the classic endpoint would leave the allowlist void for every build a user
actually runs. When buildx then falls back to booting its own `moby/buildkit`
container, the pull gate stops that too.

## Pros and cons

### What you gain

- **Windows-folder I/O, at roughly 2× write and 4× read** over the 9p transport
  a WSL2 distro uses for `/mnt/c`. This is the real reason to take this backend,
  and it is the only category where the difference is large.
- **Microsoft's engine, supported by Microsoft.** If policy where you work says
  the container runtime has to be first-party, this is a way to have that *and*
  Compose, Testcontainers and the rest of the Docker ecosystem.
- **An administrator's WSL policy keeps working**, rather than being silently
  voided by installing something that talks to `docker.sock` directly.
- **An audit log** over an engine that otherwise keeps no record.
- **No second engine to maintain.** No `skrog engine upgrade`, no rootfs to
  keep current — `wsl --update` handles it.

### What it costs

- **No engine pinning.** This is the big one. `skrog lock` has nothing to
  record, `wsl --update` moves the engine underneath you, and engine drift goes
  back to being a category of bug you can hit. If reproducibility is why you came
  to Skrog, the distro backend is the answer and will stay so.
- **~820 MB for a second VM.** A wslc session is a whole separate VM; running
  both backends at once costs roughly that much extra.
- **An older engine.** 25.0.3 / API 1.44, against 29.x on the distro backend.
- **Experimental, and not wired into the lifecycle.** No `skrog install`, no
  service, no `skrog doctor`. You run `skrog proxy` in a window and it stops
  when you close it.
- **You share the CLI's default session**, including with anything you run with
  `wslc` by hand.
- **Holder containers.** One `skrog-share-<drive>` per drive you bind from
  shows up in `docker ps` for as long as the bridge runs.
- **Sharing a drive exposes it to the session VM.** This is the same exposure a
  distro already has through `/mnt/c`, but it is worth knowing;
  [`allow-bind-sources`](policy.md) is how you narrow what containers may mount.
- **No UDP published ports.**

### What does *not* differ

Worth stating, because it is easy to assume otherwise and the measurements say
no ([#326](https://github.com/wslkit/skrog/issues/326)):

- **The control plane is a wash.** With both engines behind Skrog's own pipe and
  driven by the same `docker.exe` — `version` 69 vs 74 ms, `ps` 59 vs 59 ms,
  `run --rm busybox true` 505 vs 527 ms, `exec` 121 vs 135 ms. All within noise.
  Earlier figures showing wslc's control plane well ahead were comparing two
  CLIs, not two engines.
- **In-VM filesystem I/O is mixed with no winner.** Overlay writes favour wslc,
  named-volume writes favour the distro, reads are page cache on both sides.

### The decision, in one table

| if your… | then |
|---|---|
| source tree is on `C:` and builds/tests hammer it | **wslc backend** — this is the case it exists for |
| work lives inside the Linux filesystem | distro backend; there is little in it |
| CI or team needs a byte-identical engine | distro backend, and it is not close |
| workplace requires a first-party runtime | **wslc backend** |
| machine is short on RAM | distro backend, unless you stop the other one |
| setup needs to survive reboot unattended | distro backend ([#335](https://github.com/wslkit/skrog/issues/335)) |

## Troubleshooting

**The first run is slow** — Skrog starts the container session when none is
running, and a cold session VM takes a few seconds to boot. Later runs join the
running one.

**Containers vanish after about 30 seconds** — the session VM idle-terminated,
which means the lease is not being held. Check the bridge is still running;
Skrog re-bootstraps the agent automatically on the next connection after a VM
restart, but anything that was running is gone.

**A bind mount is empty inside the container** — the share went stale across a
VM restart. Skrog probes for this, but if you see it, restart the bridge.

**A published port is not reachable** — check it is TCP. UDP is not relayed.
Windows Firewall will also prompt the first time Skrog binds a host listener.

**`docker` reaches the wrong engine** — check `docker context ls`. `skrog` is
the distro engine and `skrog-wslc` is the session; `docker context use` picks
one. If a context points somewhere stale, restarting that bridge rewrites it.

## Verifying any of this yourself

The engine inside a session answers directly, with no Skrog involved:

```powershell
wslc system session list
wslc --session <name> system session run curl -s --unix-socket /var/run/docker.sock http://localhost/version
```

If that prints a Docker version payload, you have just talked to the engine
Microsoft ships and the one nothing on your machine can otherwise reach.

Both layers of the protocol are open source, MIT licensed, in
[microsoft/WSL](https://github.com/microsoft/WSL): the COM interfaces in
`src/windows/service/inc/wslc.idl`, the guest message protocol in
`src/shared/inc/lxinitshared.h`, and the policy rules Skrog mirrors in
`src/windows/inc/wslpolicies.h`. Nothing here required reverse engineering.

## Where this should end up

Not a clever workaround. Microsoft exposing an endpoint officially — gated by
the same policy their CLI already enforces — at which point a Skrog backend
becomes a thin adapter and everyone else's tools work too. That is what
[microsoft/WSL#40976](https://github.com/microsoft/WSL/issues/40976) asks for,
and the measurements here exist to argue for it.

Corrections are welcome — this page is
[a markdown file](https://github.com/wslkit/skrog/tree/main/docs) and a pull
request away from being right.
