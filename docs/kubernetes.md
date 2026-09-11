# Kubernetes on Hawser

**Hawser will never ship a Kubernetes.** It is a Docker engine; a bundled
control plane is a second product with its own upgrade cycle, its own failure
modes and its own opinions, and Docker Desktop's built-in one is a good example
of what that costs. What Hawser does instead is run the tools that build a
cluster out of containers — and it runs them well, because that is just
containers.

Both [kind](https://kind.sigs.k8s.io) and [k3d](https://k3d.io) were verified
against a Hawser engine end to end: cluster created, node `Ready`, `kubectl`
from **Windows**, and a workload reachable from a browser on the Windows side.

## The one thing you must know: bind on `0.0.0.0`, not `127.0.0.1`

WSL2 mirrored networking projects **wildcard-bound** listeners inside the engine
VM onto the Windows host. A listener bound explicitly to `127.0.0.1` *inside*
the VM stays inside the VM, so nothing on Windows can reach it.

That is a WSL platform behavior, not a Docker or Hawser one, and it is the
whole reason the recipes below set an API-server address. Measured on this
engine:

| published port | reachable from Windows |
| --- | --- |
| `docker run -p 8080:80` (all interfaces) | yes |
| `docker run -p 127.0.0.1:8080:80` (loopback only) | no — with the userland proxy on *or* off |

kind's default API-server address is `127.0.0.1`, which is why an out-of-the-box
`kind create cluster` succeeds and then `kubectl` times out. One line of config
fixes it.

## kind

```yaml
# kind-hawser.yaml
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  apiServerAddress: "0.0.0.0"   # not 127.0.0.1 — see above
  apiServerPort: 6443
nodes:
  - role: control-plane
    extraPortMappings:
      - containerPort: 30080
        hostPort: 30080
        listenAddress: "0.0.0.0"
        protocol: TCP
```

```powershell
$env:DOCKER_HOST = 'npipe:////./pipe/hawser_engine'   # or: docker context use hawser
kind create cluster --name dev --config kind-hawser.yaml --wait 180s
kubectl get nodes
```

Use kind's kubeconfig **as written** — it contains `server: https://0.0.0.0:6443`
and that works from Windows. Do not rewrite it to `127.0.0.1`: the API server's
certificate has `0.0.0.0` in its SAN list and not `127.0.0.1`, so the rewrite
trades a connection error for a certificate error.

Verified: control plane `Ready` in 16 s, all six system pods `Running`,
`kubectl get nodes -o wide` from Windows, and a `NodePort` service on 30080
answering **HTTP 200** in a Windows browser through `extraPortMappings`.

## k3d

Lighter, and the better default recommendation for most people — one k3s server
container plus a proxy, and its kubeconfig needs nothing done to it:

```powershell
$env:DOCKER_HOST = 'npipe:////./pipe/hawser_engine'
k3d cluster create dev --api-port 0.0.0.0:6550 -p "8080:80@loadbalancer" --wait
kubectl get nodes
```

Verified: node `Ready` in 20 s, `kubectl` from Windows against
`https://0.0.0.0:6550` (k3d writes that itself), and an `Ingress` through the
k3d load balancer answering **HTTP 200** on the Windows side. The engine sat at
about **2.7 GB** of the VM's memory with the cluster and a workload running.

`--api-port 0.0.0.0:6550` is the same rule as kind's `apiServerAddress`: k3d's
default binds the API to the host gateway address, which is inside the VM.

## Why this is nicer on Hawser than on Docker Desktop

- **The engine is pinned.** A cluster built on a `hawser.lock`-pinned engine is
  the same engine your CI runner uses — see [local-ci.md](local-ci.md).
- **Idle stop still applies to the engine, not to your cluster.** A running
  cluster keeps the engine busy, so it stays up; delete the cluster and the
  engine parks itself again (`hawser config set idle-timeout 30m`).
- **`hawser prune` will not eat your cluster.** It reclaims dangling images and
  stopped containers; a running kind/k3d node container is neither. Stop the
  cluster before a `--all` prune if you want its images gone too.
- **`hawser snapshot` covers the whole engine**, cluster included: save before
  a risky Helm chart, `hawser reset --to <snapshot>` after —
  [snapshots.md](snapshots.md).
- **GPU workloads work** in a cluster the same way they do in a container, once
  `hawser enable-gpu` has run ([gpu.md](gpu.md)); the device plugin still needs
  installing inside the cluster.

## What was not verified

- Multi-node clusters (`kind` with worker nodes, `k3d --agents N`). Nothing
  suggests a problem — they are more containers on the same bridge — but the
  spike used single-node clusters.
- LoadBalancer services beyond k3d's built-in proxy (MetalLB and friends).
- Anything on a machine where an EDR agent destabilizes the supervisor; see
  [#166](https://github.com/hawserhq/hawser/issues/166) and the
  `hawser doctor` injected-modules check.
