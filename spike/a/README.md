# Spike A — manual end-to-end path (issue #2)

Throwaway kit. Proves: Alpine + static dockerd in a dedicated WSL2 distro, reachable from
stock `docker.exe` through a named pipe relayed over `wsl.exe` stdio. **Nothing here is
product code** — it exists to produce the measurements listed at the bottom, recorded in
issue #2, then `99-cleanup.ps1` erases all trace.

Pins: Alpine minirootfs **3.24.1** · Docker static **29.7.2** · pipe `\\.\pipe\skrog_spike`
(never `docker_engine` — Docker Desktop owns that here, and coexistence is part of the test).

## Prerequisites

- WSL2 working (`wsl --status`)
- Go on Windows: `winget install GoLang.Go` (new terminal afterwards so PATH updates)
- Any `docker.exe` on PATH (Docker Desktop's is fine — we point it at our pipe via `DOCKER_HOST`)

## Run order

```powershell
.\01-fetch.ps1        # download + verify alpine rootfs, download docker static tgz
.\02-import.ps1       # wsl --import skrog-spike, run 03-setup.sh inside it
.\04-start-engine.ps1 # start dockerd (hidden), wait for the socket
cd relay; go mod tidy; go build; .\relay.exe   # leave running in this terminal
# new terminal:
.\05-verify.ps1       # automated checks + latency numbers
# then the manual checks below, then:
.\99-cleanup.ps1      # unregister distro, delete downloads
```

## What to record in issue #2

Automated (05-verify prints these):
- [ ] `docker version` round-trip latency: avg / min / max over 10 runs
- [ ] `docker run --rm hello-world` end-to-end
- [ ] binary safety: `docker save` twice → identical sha256
- [ ] per-connection socat spawn time (relay.exe log lines)

Manual:
- [ ] `docker run -it --rm alpine sh` — interactive shell usable, Ctrl-D exits cleanly (hijack)
- [ ] `docker exec -it <ctr> sh` into a running container
- [ ] `docker logs -f <ctr>` streams; Ctrl-C detaches without killing the container
- [ ] half-close: `docker build -t spike-test .` with a small context dir (client sends tar,
      half-closes, then waits for the build stream back)
- [ ] coexistence: switch back to Desktop's context — `docker context use desktop-linux; docker ps`
      still works while relay.exe is running
- [ ] gotchas: anything about wsl.exe stdio (buffering, CRLF, exit codes), cgroups,
      iptables, DNS inside the spike distro

## GO/NO-GO

GO = all of the above pass or have understood workarounds. NO-GO = the wsl.exe stdio
relay is not binary-safe or hijacked streams can't survive it — that would force the
vsock agent forward from v0.2 into v0.1 (or a rethink).
