# WSL containers (wslc) and Skrog

Microsoft ships `wslc.exe` with WSL 2.9.3+, and it is heading for general
availability. If you have updated WSL recently you already have it. The question
this page answers is the one people actually arrive with:

> *Can I use Compose, Testcontainers, Dev Containers or buildx with `wslc`?*

**Today, no — and the reason is more interesting than "it's a different tool".**

## The short answer

Every `wslc` session boots a real Docker engine. Not a lookalike, not a
compatible reimplementation: **stock Moby**, built by Microsoft, listening on
`/var/run/docker.sock` inside the session VM.

What it does not have is an **endpoint**. `dockerd` is started with no `-H`
flag, so it listens on that guest unix socket and nowhere else. There is no
named pipe, no TCP port, and no inbound route into the session VM that a Docker
client could name. So the engine is complete, unmodified — and unreachable.

That is the whole gap. Not a missing API. A missing listener.

## What is actually inside a session

Verified on WSL 2.9.11.0, Windows 10 22H2 (build 19045), by asking the engine
directly from inside the session VM:

| | |
|---|---|
| Engine | dockerd **25.0.3**, API **1.44** (min 1.24), built 2026-05-31 |
| Runtime | containerd 2.2.4, runc 1.3.3, docker-init 0.19.0 |
| Socket | `srw-rw---- root docker /var/run/docker.sock` |
| Daemon flags | `dockerd --containerd /run/containerd/containerd.sock` — **no `-H`** |
| Storage | overlay2 on a per-session ext4 VHD |
| Builder | BuildKit (`Builder-Version: 2`) |
| GPU | CDI, enabled in `/etc/docker/daemon.json` |
| Guest OS | Azure Linux 3.0, kernel 6.18.40.1-microsoft-standard-WSL2 |
| File sharing | **virtiofs** — each Windows folder becomes its own share at `/mnt/{GUID}` |

The only TCP listener anywhere in that VM belongs to containerd's debug socket.

## Why `docker` cannot connect

`DOCKER_HOST` accepts four transports. Every one is a dead end here:

| scheme | why it cannot work |
|---|---|
| `unix://` | The socket is inside the VM's filesystem namespace, and Windows has no path to it |
| `tcp://` | dockerd is not listening on TCP — and nothing routes into the session VM from the host anyway |
| `npipe://` | A Windows construct; nothing inside a Linux VM can create one |
| `ssh://` | No sshd in the guest — it is a minimal image with `docker`, `curl`, `wget` and busybox |

The only channel in or out of the VM is WSL's own hvsocket message protocol,
spoken by `wslcsession.exe` on the Windows side. That protocol is not Docker. It
carries messages like `Mount`, `Exec`, `Connect`, `MapPort` and `UnixConnect` —
a general-purpose way to drive a Linux VM from Windows.

And here is the part worth knowing: **`wslcsession.exe` is itself a Docker
client.** It connects to that socket, consumes the full Docker API, and
re-publishes it as a different CLI and a different SDK. The API is spoken
fluently at both ends. It simply never leaves the VM.

## So what breaks

Anything that speaks Docker to a socket or a pipe, which is most of the
ecosystem:

- **Compose** — no endpoint to point at
- **Testcontainers** — needs both the API *and* mapped ports
- **Dev Containers**, **buildx**, **`act`**, **`gitlab-ci-local`**, **Dagger**
- Anything that bind-mounts `docker.sock` (docker-in-docker, Ryuk, agent sandboxes)

The `wslc` CLI itself is capable and pleasant — it runs, builds and networks
containers, with GPU support and a NuGet SDK for driving it from a Windows app.
If a first-party runtime with Microsoft behind it is what you want, use it. This
page is not an argument against `wslc`.

### This is a deliberate narrowing, not an oversight

Worth stating plainly, because it explains why the gap is unlikely to close by
accident: `wslc`'s registry allowlists, Group Policy/Intune controls and plugin
hooks are enforced in `wslcsession.exe` **in front of** the daemon. A narrow,
non-Docker API is what makes those controls enforceable. Exposing raw `dockerd`
would undo them, which is very likely why
[the endpoint request](https://github.com/microsoft/WSL/issues/40976) has sat
without a reply.

Any serious proposal for a Docker endpoint on `wslc` has to answer that
objection, not ignore it.

## What Skrog does instead

Skrog does not wrap or patch `wslc`. It runs **upstream `dockerd` in its own
WSL2 distro** and serves the endpoint Windows is missing:
`\\.\pipe\docker_engine` — the pipe `docker.exe` already talks to.

```powershell
irm https://wslkit.github.io/skrog/install.ps1 | iex
skrog install
docker run --rm hello-world
```

Nothing on that list above has to know Skrog exists. Compose, Testcontainers,
Dev Containers, buildx, `act` and Dagger work because the thing answering is
genuinely `dockerd`, byte for byte.

What you get today, on a shipping release:

- **The real Docker API** on the pipe, at Docker Desktop's speed — the transport
  is a vsock guest agent, not a per-command process spawn
- **A pinned engine.** `skrog lock` writes `skrog.lock` naming dockerd,
  containerd, runc and BuildKit to the commit; CI installs that same file. Docker
  Desktop cannot pin an engine version, and neither can `wslc` — `wsl --update`
  moves it underneath you. Engine drift stops being a category of bug
- **Lifecycle that stays out of the way** — starts at logon, survives reboot,
  sleep/resume and `wsl --shutdown`, idle-stops when you are not using it
- **`skrog doctor`** for the WSL/VPN/DNS/proxy failures that make WSL2 engines
  miserable in corporate networks
- **Policy and audit** at the pipe: deny `--privileged`, restrict bind-mount
  sources, allowlist registries, and log every container-affecting API call
- **No licence fee, no Electron, no Kubernetes, no tray-app ceremony.**
  Coexists with Docker Desktop if you still need it

It is Windows-only and Linux-containers-only, one maintainer, and not yet
code-signed — so SmartScreen warns on first run. Those are the honest edges.

## Using a wslc session as Skrog's engine

**Experimental.** It works, and it is one command, but it is not something
`skrog install` will set up for you yet and it is not what a normal install
uses. Tracked in [#316](https://github.com/wslkit/skrog/issues/316).

Skrog already serves a pipe and relays it to a `dockerd` socket, and
`internal/pipeproxy` does not care which VM that socket lives in. Point it at a
wslc session and stock `docker` talks to the engine Microsoft ships:

```powershell
skrog proxy --engine wslc
```

That places Skrog's guest agent in the session VM, serves the Docker API on a
named pipe, and publishes container ports back to Windows. In another shell:

```powershell
docker --context skrog ps
docker version          # Server: 25.0.3, Microsoft Azure Linux 3.0
```

Requirements: WSL 2.9.3+ with a wslc session already running (`wslc run --rm
hello-world` creates one — the CLI cannot create a *named* session, so Skrog
shares the default one). Ctrl-C stops the bridge and releases everything.

### What works

Container lifecycle, `exec`, `logs` and `logs -f`, `stats`, `inspect`,
`build` and `buildx`, `docker cp`, networks, volumes, `commit`, `save`/`load`,
`events` — and the two that matter most:

- **Compose**, including healthchecks, `depends_on` conditions, named volumes
  and published ports.
- **Testcontainers**, including the Ryuk reaper, which needs the engine socket
  bind-mounted into a container — something the `wslc` CLI cannot express at all.

Published ports reach Windows, which they do not over a plain socket relay:
dockerd publishes them inside the session VM and the relay that would carry them
to the host is driven from the Windows side. Skrog runs its own relay, and binds
the address your `-p` asked for — `wslc`'s own relay only ever binds
`127.0.0.1`.

**Windows folders work too**, which the `wslc` CLI cannot do for an arbitrary
path and which is the main reason to prefer this backend: a session has no
`/mnt/c`, so Skrog shares the drive into the session and rewrites `C:\src\app`
to the share's guest path. The mount lands on **virtiofs**, and on the same
folder that is around 1.6× the write and 3.4× the read of the 9p transport a
WSL2 distro uses for `/mnt/c`. Editing a file on Windows is visible in the
container immediately, so the ordinary edit-and-reload loop works.

The cost is one `skrog-share-<drive>` container per drive you bind from, holding
the share open — the share exists only while something mounts it. It is removed
when the bridge stops. Sharing a drive exposes it to the session VM, which is
the same exposure a distro already has through `/mnt/c`;
[`allow-bind-sources`](policy.md) is the way to narrow what containers may mount.

### What does not work

- **UDP published ports.** The relay is a stream transport.
- **Engine pinning.** `skrog lock` has nothing to pin: Microsoft ships the
  engine and `wsl --update` moves it underneath you.
- **`compact`, `snapshot`, `relocate`, `wsl-integrate`, `gpu`, `engine upgrade`**
  — all of these operate on Skrog's own distro and have no meaning here.

### Policy and audit

The engine socket sits behind `wslcsession`, which is where WSL enforces an
administrator's registry allowlist. Talking to the socket directly would bypass
that, so Skrog reads the same Group Policy configuration WSL reads
(`HKLM\Software\Policies\WSL`) and applies it at the pipe:

| policy | where Skrog applies it |
|---|---|
| `WSLContainerRegistryAllowlist` | `docker pull`, and the image named by `docker run`/`create` |
| `AllowWSLContainerPrivileged` | `--privileged` on create |
| `WSLContainerRegistryAllowlist` | `docker build` — **refused outright** while an allowlist is active |

Verified against a machine with a real allowlist deployed: a blocked `docker
pull` and a `--privileged` run are both refused with a 403 naming the policy
that stopped them, `docker ps`, `images` and `version` are untouched, and
`wslc pull` refuses the same image with `WSLC_E_REGISTRY_BLOCKED_BY_POLICY` —
so the two agree rather than Skrog inventing its own answer.

Builds are the one place Skrog is **stricter than WSL**, and it is worth being
straight about why. `wslc image build` is not refused: it runs, and the
allowlist is enforced per source *inside* BuildKit, so a blocked base image
fails with `source "docker-image://docker.io/library/busybox:latest" denied by
policy`. `wslcsession` can do that because it drives BuildKit directly and
attaches a source policy to the solve request. That policy is client-side and
not daemon configuration — a build sent straight to the session's `dockerd`
inherits none of it, which is measured rather than assumed. Matching it at the
pipe would mean rewriting protobuf inside a hijacked HTTP/2 stream, so until
that exists Skrog refuses the build instead of letting it through.

Note that the refusal covers `/session` and `/grpc` as well as `/build`: buildx
has been the default `docker build` since Docker 23 and never touches `/build`,
so gating only the classic endpoint would leave the allowlist void for every
build a user actually runs.

Skrog's own [`policy.yaml`](policy.md) and the [audit log](audit.md) apply here
too. The audit log is worth turning on: it records every container-affecting
call with its outcome, including each denial and the reason — a record WSLC
itself does not keep.

### Should you use it?

It depends on what your work looks like. Measured on one machine, the two
backends are within noise on the control plane and on in-VM filesystem I/O, and
a wslc session costs roughly **820 MB** for a second VM.

What decides it is where your source tree lives. If you build and test against
files on a Windows drive, virtiofs is meaningfully faster than the 9p transport
a distro uses for `/mnt/c`, and that gap is felt on exactly the workloads people
complain about — `npm install`, a gradle build, a large `docker build` context.
If your work lives inside the Linux filesystem, there is little in it.

There is also one thing this backend can never do: **pin the engine.** Microsoft
ships it and `wsl --update` moves it, so `skrog lock` has nothing to record and
the reproducibility this project is otherwise built around does not apply here.
If that is why you came, the distro backend is the answer and will stay so.

Where this should end up is not a clever workaround. It is Microsoft exposing an
endpoint officially — gated by the same policy their CLI enforces — at which
point a Skrog backend becomes a thin adapter and everyone else's tools work too.
That is what [microsoft/WSL#40976](https://github.com/microsoft/WSL/issues/40976)
asks for, and the measurements above exist to argue for it.

## Verifying any of this yourself

Every claim on this page came from asking the engine directly. With a `wslc`
session running:

```powershell
wslc system session list
wslc --session <name> system session run curl -s --unix-socket /var/run/docker.sock http://localhost/version
```

If that prints a Docker version payload, you have just talked to the engine
Microsoft ships and the one nothing on your machine can otherwise reach.

Both layers of the protocol are open source, MIT licensed, in
[microsoft/WSL](https://github.com/microsoft/WSL): the COM interfaces in
`src/windows/service/inc/wslc.idl`, and the guest message protocol in
`src/shared/inc/lxinitshared.h`. Nothing here required reverse engineering.

Corrections are welcome — this page is
[a markdown file](https://github.com/wslkit/skrog/tree/main/docs) and a pull
request away from being right.
