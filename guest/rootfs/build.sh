#!/usr/bin/env bash
# Build the Hawser rootfs: Alpine + engine binaries compiled from upstream source.
#
# Runs in CI (ubuntu-latest, Docker available) and on any Linux host with Docker —
# including a WSL2 Ubuntu distro, which is how it gets tested locally.
#
#   ./build.sh [output-dir]     default: ./out
#
# Output: hawser-rootfs-<version>.tar.gz, its .sha256, and an SPDX SBOM.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
out="${1:-$here/out}"
# shellcheck source=versions.env
. "$here/versions.env"
rootfs_version="${ENGINE_VERSION}-${ROOTFS_REVISION}"

mkdir -p "$out"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# The base is the official alpine image for the pinned branch, pulled by the
# assemble step below; there is no separate minirootfs download any more.
# ALPINE_ROOTFS_VERSION is retained because the SBOM records it.

# Engine binaries are the slow part (~4 min of compiling). BIN_DIR lets a caller
# supply a directory that persists across runs — CI points it at an actions/cache
# entry keyed on the pins below, so a config-only change re-assembles the rootfs
# without recompiling anything. Layout: $BIN_DIR/bin/* plus $BIN_DIR/commits.txt.
engine="${BIN_DIR:-$work/engine}"
mkdir -p "$engine"

if [ -x "$engine/bin/dockerd" ] && [ -s "$engine/commits.txt" ]; then
    echo "==> reusing engine binaries from $engine"
    # The cache key covers versions.env, but a stale or mismatched entry would
    # otherwise ship silently — so confirm the binary is the pinned version.
    if ! "$engine/bin/dockerd" --version | grep -q "$ENGINE_VERSION"; then
        echo "cached dockerd is not $ENGINE_VERSION - rebuilding" >&2
        rm -rf "$engine"
        mkdir -p "$engine"
    fi
fi

if [ ! -x "$engine/bin/dockerd" ]; then
    echo "==> building engine binaries from source (this is the slow part)"
    # Each component is built in a pinned golang image from its upstream git tag —
    # no download.docker.com artifacts, so provenance is source -> binary end to end.
    # HOST_UID/GID: the build runs as root in the container but writes into a host
    # directory, so it hands ownership back before exiting — otherwise cleanup and
    # the tar step hit permission errors on the CI runner.
    docker run --rm \
      -v "$engine:/out" \
      -e "MOBY_TAG=$MOBY_TAG" \
      -e "ENGINE_VERSION=$ENGINE_VERSION" \
      -e "CONTAINERD_VERSION=$CONTAINERD_VERSION" \
      -e "RUNC_VERSION=$RUNC_VERSION" \
      -e "BUILDKIT_VERSION=$BUILDKIT_VERSION" \
      -e "MOBY_SHA=${MOBY_SHA:-}" \
      -e "CONTAINERD_SHA=${CONTAINERD_SHA:-}" \
      -e "RUNC_SHA=${RUNC_SHA:-}" \
      -e "BUILDKIT_SHA=${BUILDKIT_SHA:-}" \
      -e "HOST_UID=$(id -u)" \
      -e "HOST_GID=$(id -g)" \
      -v "$here/build-engine.sh:/build-engine.sh:ro" \
      "golang:${GO_VERSION}-alpine" sh /build-engine.sh
fi

echo "==> building hawser-agent from this repo"
# The vsock agent (#40) is the one binary in the rootfs that comes from this
# repository rather than an upstream tag, so it is versioned by the rootfs
# release itself. Built in the same pinned toolchain image as the engine, but
# outside the BIN_DIR cache: it changes with this repo, not with versions.env.
repo_root="$(cd "$here/../.." && pwd)"
agent_out="$work/agent"
mkdir -p "$agent_out"
docker run --rm \
  -v "$repo_root:/src:ro" \
  -v "$agent_out:/out" \
  -w /src \
  -e CGO_ENABLED=0 \
  "golang:${GO_VERSION}-alpine" \
  sh -c "go build -trimpath -ldflags '-s -w' -o /out/hawser-agent ./guest/agent \
         && chown $(id -u):$(id -g) /out/hawser-agent"

echo "==> building nvidia-cdi-hook $NVIDIA_CDI_HOOK_VERSION (#139)"
# The one glibc binary in the rootfs. Its presence when dockerd starts is what
# makes moby route `docker run --gpus all` to the CDI spec that
# `hawser enable-gpu` installs; the spec is hookless, so this binary is never
# executed for GPU injection — it only has to exist and start. It cannot be
# built on musl (go-nvml's dlopen shim uses glibc-only RTLD flags), so it is
# built in the pinned golang Debian image as a static glibc binary, which runs
# on Alpine. Pinned by tag AND commit like every other component (#88).
hook_out="$work/hook"
mkdir -p "$hook_out"
docker run --rm \
  -v "$hook_out:/out" \
  -e "HOOK_TAG=$NVIDIA_CDI_HOOK_VERSION" \
  -e "HOOK_SHA=${NVIDIA_CDI_HOOK_SHA:-}" \
  -e "HOST_UID=$(id -u)" \
  -e "HOST_GID=$(id -g)" \
  "golang:${GO_VERSION}-bookworm" \
  sh -euc '
    git -c advice.detachedHead=false clone --depth 1 --branch "$HOOK_TAG" \
      https://github.com/NVIDIA/nvidia-container-toolkit.git /src/toolkit >/dev/null 2>&1
    sha="$(git -C /src/toolkit rev-parse HEAD)"
    if [ -n "$HOOK_SHA" ] && [ "$HOOK_SHA" != "$sha" ]; then
      echo "FATAL: nvidia-container-toolkit $HOOK_TAG resolved to $sha, expected $HOOK_SHA" >&2
      echo "  (a moved tag or a compromised source; refusing to build)" >&2
      exit 1
    fi
    [ -n "$HOOK_SHA" ] || echo "    WARNING: no expected SHA pinned for nvidia-cdi-hook; resolved $sha" >&2
    cd /src/toolkit
    CGO_ENABLED=1 go build -trimpath \
      -ldflags "-s -w -linkmode external -extldflags -static -X github.com/NVIDIA/nvidia-container-toolkit/internal/info.version=${HOOK_TAG#v}" \
      -o /out/nvidia-cdi-hook ./cmd/nvidia-cdi-hook 2>&1 | grep -v "statically linked applications" || true
    [ -x /out/nvidia-cdi-hook ] || { echo "nvidia-cdi-hook build produced no binary" >&2; exit 1; }
    /out/nvidia-cdi-hook --version
    printf "nvidia-container-toolkit %s %s\n" "$HOOK_TAG" "$sha" > /out/commit.txt
    chown -R "$HOST_UID:$HOST_GID" /out'

echo "==> assembling rootfs"
# Everything the image needs, staged as a build context.
ctx="$work/ctx"
mkdir -p "$ctx/bin"
cp "$engine/bin/"* "$ctx/bin/"
cp "$agent_out/hawser-agent" "$ctx/bin/"
cp "$hook_out/nvidia-cdi-hook" "$ctx/bin/"
cp "$engine/commits.txt" "$ctx/commits"
cat "$hook_out/commit.txt" >> "$ctx/commits"
printf '%s\n' "$ENGINE_VERSION" > "$ctx/engine-version"
# The agent states its own identity (static linux binary, runnable right
# here); asking it beats duplicating the constant in shell.
"$agent_out/hawser-agent" -version > "$ctx/agent-version"

cat > "$ctx/daemon.json" <<'JSON'
{
  "log-driver": "json-file",
  "log-opts": { "max-size": "10m", "max-file": "3" }
}
JSON

cat > "$ctx/wsl.conf" <<'CONF'
[boot]
systemd=false

[automount]
enabled=true
options="metadata"

[interop]
enabled=true
appendWindowsPath=false
CONF

cp "$here/assemble.Dockerfile" "$ctx/Dockerfile"

tag="hawser-rootfs:${ENGINE_VERSION}"
docker build --build-arg "ALPINE_TAG=${ALPINE_BRANCH#v}" --build-arg "ALPINE_DIGEST=${ALPINE_DIGEST}" -t "$tag" "$ctx"

tarball="$out/hawser-rootfs-${rootfs_version}.tar.gz"
echo "==> exporting $tarball"
# docker export writes the container filesystem with correct ownership and
# without the pseudo-filesystems, which is exactly what `wsl --import` wants.
# Byte-for-byte reproducibility is not claimed: apk stamps install times into
# the image. The published .sha256 is what `hawser install` verifies against.
cid="$(docker create "$tag")"
trap 'docker rm -f "$cid" >/dev/null 2>&1 || true; rm -rf "$work"' EXIT
docker export "$cid" | gzip -n > "$tarball"
docker rm -f "$cid" >/dev/null

(cd "$out" && sha256sum "$(basename "$tarball")" > "$(basename "$tarball").sha256")

echo "==> SBOM"
"$here/sbom.sh" "$here/versions.env" "$out/hawser-rootfs-${rootfs_version}.spdx.json"

ls -la "$out"
echo "OK"
