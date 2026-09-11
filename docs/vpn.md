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

If the engine still stalls on large pulls, the remaining lever is the engine's
own MTU. Unlike `~/.wslconfig`, that is Hawser's distro, so there is a command
for it — use the value `doctor` reports:

```
hawser config set engine.mtu 1400
```

It goes into the engine's `daemon.json` as the MTU for the default bridge
network, is checked with `dockerd --validate` before it applies, and rolls back
if the engine will not come back. With two tunnels up at once, `doctor`
recommends the **smallest** clamp, because the larger one still fragments.


Behind a VPN that also inspects TLS (Zscaler especially), the engine must trust
the VPN's root CA as well, or pulls fail with an x509 error before MTU ever
matters:

```
hawser config set network.import-host-cas on
hawser restart
```

## Published ports under mirrored networking

Mirrored networking changes how `docker run -p` has to be plumbed, and Hawser
sets the engine up for it: `engine.userland-proxy` defaults to **false**, so
published ports are plain iptables NAT.

With the userland proxy on (dockerd's own default), a connection from Windows to
`127.0.0.1:<port>` is DNAT'd into the container with its loopback source address
intact — the container answers into its own loopback and the connection hangs,
while `docker ps`, logs and everything else look perfectly healthy (#163).
Turning the proxy off makes dockerd install the `MASQUERADE` rule that fixes the
return path.

Hawser applies the default to installs that predate it, on the next engine
start, and never overrides a value you set yourself:

```
hawser config get engine.userland-proxy     # false
hawser config set engine.userland-proxy on  # your choice wins from then on
```

The one thing the userland proxy still does better is publishing on an address
the NAT path cannot see; if you need that, turn it back on and reach containers
by their IP instead.

## Why the global settings stay advisory

`hawser doctor` names the VPN and shows the exact settings rather than applying
them, because the effective fix (`~/.wslconfig`) is global to your WSL
environment. That is a change you should make deliberately, seeing the diff —
not one a `docker` wrapper should make behind your back.

The engine's own settings are a different matter: `engine.mtu` and `engine.dns`
change only Hawser's distro, so those are commands rather than advice. The line
is ownership — Hawser configures what it owns and tells you about the rest.

The MTU and DNS values are conservative starting points; a particular
deployment's tunnel overhead may want a lower MTU still.
