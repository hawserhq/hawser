# Spike C: AF_HYPERV from Windows into the WSL2 utility VM

The mini-spike gating #40 (vsock guest agent). Question: can a **standard,
non-elevated** Windows process dial into a vsock listener inside the WSL2
utility VM — and in particular, can it discover the VM's GUID, which
`hcsdiag list` refuses to reveal without Hyper-V-admin rights?

## Verdict: GO

Run on 2026-09-01, Windows 11 Pro 26200, WSL2, standard (non-admin) user,
with Docker Desktop's distro running in the same utility VM:

```
discovered 1 compute system(s) without elevation: [EAAC9D05-…]
vm EAAC9D05-…: CONNECTED, banner="skrog-spike-c hello from the guest", first connect+banner=4.005ms
per-connection cost over 50 dials: avg 574.456µs
PASS
```

Findings, in the order #40 needs them:

1. **VM-GUID discovery needs no elevation.** The Host Compute Service mirrors
   every running compute system into
   `HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion\HostComputeService\VolatileStore\ComputeSystem`,
   and that key is readable by standard users. All WSL2 distros of a session
   share one utility VM, so with WSL running there is exactly one candidate
   (Docker Desktop's distro lives in the same VM — verified live). The key
   only exists while something is running, which is fine: the supervisor
   starts the distro before the agent.
2. **The dial itself is unprivileged.** go-winio's `Dial` with
   `HvsockAddr{VMID, VsockServiceID(port)}` connects as a plain user; no
   service registration under `GuestCommunicationServices` is needed in the
   host→guest direction (registration is only for guest→host listeners).
3. **The guest side is plain AF_VSOCK.** Bind `VMADDR_CID_ANY`, listen,
   accept. `/dev/vsock` is present in stock WSL2 distros.
4. **The host arrives as CID 2** (`VMADDR_CID_HOST`), so the agent can and
   should reject any peer with a different CID.
5. **Per-connection cost ~0.6 ms** vs the ~165 ms socat path measured in
   Spike A — the #40 latency target ("within 2× Docker Desktop") is cleared
   by two orders of magnitude.

One sharp edge to carry into the real design: the VolatileStore key lists
*compute systems*, which on a busy machine could include non-WSL utility VMs
(other HCS consumers). The spike handles it the way the agent should: try each
candidate; only the VM actually running our listener answers on our port.
The agent's banner/handshake makes a wrong-VM connection fail closed.

## Kit

- `guest/`: AF_VSOCK echo listener (Go, linux) — the exact socket code the
  real agent will use. `GOOS=linux go build`, copy into any WSL2 distro, run
  with a port argument.
- `host/`: discovery + dial + latency probe (Go, windows). `go build`, run
  `host.exe -n 50`; `-vm` overrides discovery, `-port` matches the guest.
- `run.ps1`: builds both, starts the listener in a distro (default Ubuntu),
  runs the probe, cleans up.

## What this unlocks in #40

Owning both ends removes the EOF ambiguity that made #35 only *boundable*
with socat (a Ctrl-C'd CLI and a build's half-close look identical to socat's
stdin). The agent relays vsock⇄`/var/run/docker.sock` per connection, the
supervisor keeps it alive with the engine, and the socat path stays as the
automatic fallback for a rootfs that predates the agent.
