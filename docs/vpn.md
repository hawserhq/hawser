# VPNs

A corporate VPN is the second "works at home, breaks at work" failure after a
TLS-inspecting proxy ([corporate-network.md](corporate-network.md)). The tunnel
clamps the path MTU or hijacks DNS, and the symptom is maddeningly indirect:
`docker pull` hangs partway, or a container cannot resolve a hostname — with
nothing that points at the VPN.

`hawser doctor` recognizes the common VPN clients from your active network
adapters and prints the settings that fix egress through the tunnel:

```
hawser doctor
...
[warn] corporate VPN: a VPN is active (Palo Alto GlobalProtect); container egress
       may need MTU/DNS tuning
  Palo Alto GlobalProtect  (adapter: Ethernet 4)
    recommended engine MTU: 1400
    DNS fallback: 1.1.1.1, 8.8.8.8
    GlobalProtect clamps the tunnel MTU and enforces split DNS; ...
```

Detection reads the adapter's **description** (what the vendor's driver sets,
e.g. `PANGP Virtual Ethernet Adapter`), not its connection name, so a renamed
connection is still recognized. Reading adapters uses `Get-NetAdapter` — a
standard-user cmdlet, **no elevation**.

## Recognized clients

| VPN | Recognized by | Recommended MTU |
| --- | --- | --- |
| Palo Alto GlobalProtect | `PANGP` / `globalprotect` | 1400 |
| Cisco AnyConnect / Secure Client | `anyconnect` / `cisco secure client` | 1300 |
| Zscaler | `zscaler` | 1400 |
| Fortinet FortiClient | `forticlient` / `fortissl` | 1400 |
| OpenVPN | `tap-windows` / `openvpn` | 1400 |
| WireGuard | `wireguard` / `wintun` | 1420 |

## Applying the fix

The strongest fix for a split-tunnel VPN is **mirrored networking**, a global
WSL2 setting that lets every distro share the host's VPN routes and DNS. It lives
in `~/.wslconfig`, and because it affects **every** WSL2 distro on the machine —
not just Hawser's — Hawser does not edit it for you. Add it yourself and restart
WSL:

```ini
# ~/.wslconfig
[wsl2]
networkingMode=mirrored
dnsTunneling=true
autoProxy=true
```

`dnsTunneling` and `autoProxy` require WSL 2.0.9+ (`wsl --version`). After editing,
restart WSL for the change to take effect, then `hawser start`.

If the engine still stalls on large pulls, the remaining lever is the interface
MTU inside the distro — clamp it to the value `doctor` reports.

Behind a VPN that also inspects TLS (Zscaler especially), the engine must trust
the VPN's root CA as well, or pulls fail with an x509 error before MTU ever
matters:

```
hawser config set network.import-host-cas on
hawser restart
```

## Why advisory, not automatic

`hawser doctor` names the VPN and shows the exact settings rather than applying
them, because the effective fix (`~/.wslconfig`) is global to your WSL
environment. That is a change you should make deliberately, seeing the diff —
not one a `docker` wrapper should make behind your back. The MTU and DNS values
are conservative starting points; a particular deployment's tunnel overhead may
want a lower MTU still.
