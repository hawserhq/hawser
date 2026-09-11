# Spike B — session-0 / no-login WSL2 from a Windows service (issue #3)

**The gate this decides.** PLAN §06 sells Skrog to self-hosted Windows CI runners, and
the differentiating claim there is *"works with no user logged in"* — something Docker
Desktop never solved. If that claim is false, the wedge market narrows and PLAN §06 needs
rewriting before v0.1 goes public. This spike settles it now rather than at v1.0.

**The specific suspicion.** WSL2 registers distros **per user**, under
`HKCU\Software\Microsoft\Windows\CurrentVersion\Lxss`. A service runs in session 0 with
its own hive, so the distro the installing user imported may simply not exist as far as
the service is concerned. That is the first thing the probe reports, because if it fails
everything downstream is moot — and the fix (import the distro *as the service account*)
changes what `skrog install --headless` has to do.

## Requirements

- **Elevated PowerShell** — services, local accounts, and user rights all need it
- Go on PATH (`winget install GoLang.Go`)
- Willingness to **sign out** of the machine for ~3 minutes, or reboot without logging in

## Run order

```powershell
# elevated
.\01-preflight.ps1               # interactive baseline + a distro to probe
.\02-service-localsystem.ps1     # cheapest pattern: LocalSystem
.\03-service-dedicated-account.ps1  # PLAN's proposed pattern: dedicated account
.\04-nologin-window.ps1          # then SIGN OUT (or reboot, no login) for 3+ min
.\05-verdict.ps1                 # after signing back in
.\99-cleanup.ps1                 # removes service, task, account, distros, logs
```

`show-log.ps1` renders the probe log at any point; `-Mode service` filters to service runs.

## How it answers the question

`agent/` is a small Go program that runs either as a console app (the interactive
baseline) or as a Windows service, appending JSON lines to
`C:\ProgramData\skrog-spike-b\probe.log`. It records, in order:

| phase | question |
|---|---|
| `identity` | which account, and which session — **0 means the services session** |
| `wsl-status` | does `wsl.exe` run at all in this context? |
| `wsl-list` | which distros does *this account* see? |
| `distro-visible` | is the target distro there? **the decisive line** |
| `distro-exec` | can the distro actually start and run a command? |
| `socket-up` | does dockerd come up, and how fast? |
| `heartbeat` | every 15s: is the socket still reachable? |

The heartbeat is the whole design. Nothing can be observed live while no user is logged
in, so the service records health continuously and `05-verdict.ps1` reads back what
happened during the window after you sign in again.

## Record in issue #3

- [ ] Which pattern worked: LocalSystem, dedicated account, or neither
- [ ] `identity` output — account and session number in service mode
- [ ] `wsl-list` in service mode: does a service account see the interactive user's distros?
- [ ] Whether importing *as the service account* was required
- [ ] Heartbeat result across the sign-out window (and across a reboot with no login, if run)
- [ ] dockerd cold-start time from `socket-up`
- [ ] Anything that needed manual intervention — each one is a v0.2 task

## Verdict

**GO** — service-mode checks pass and heartbeats stay healthy with nobody logged in.
Record which pattern worked; that becomes the v0.2 service design.

**NO-GO / PARTIAL** — the no-login window fails. This does not kill the project: it
demotes the claim to "requires auto-logon", PLAN §06 is rewritten honestly, and the
fleet-deployment story leans on MSI + version pinning instead.

## Notes

- The dedicated account is created with a random password that is never printed or
  written to disk. It is passed to `sc.exe` and `schtasks`, so it is briefly visible in
  a process command line — acceptable for a throwaway spike, not a pattern for the
  product. A real installer should prefer a virtual service account
  (`NT SERVICE\SkrogEngine`) or a Group Managed Service Account.
- The account is deliberately **not** added to Administrators; whether least privilege
  is sufficient is part of what is being measured.
- `99-cleanup.ps1` cannot unregister a distro owned by another account directly, so it
  removes the account and its profile, which takes the registration with it.
