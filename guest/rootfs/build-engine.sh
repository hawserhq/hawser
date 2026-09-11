#!/bin/sh
# Runs INSIDE a pinned golang:alpine container (invoked by build.sh).
# Clones each engine component at its pinned tag and builds a static binary.
# Result: /out/bin/{dockerd,containerd,containerd-shim-runc-v2,ctr,runc,buildkitd,buildctl}
set -eu

apk add --no-cache git make bash musl-dev gcc libseccomp-dev libseccomp-static \
    btrfs-progs-dev linux-headers pkgconf

mkdir -p /out/bin /out/licenses /src
# Start clean: the output dir may be a reused cache directory (see BIN_DIR in
# build.sh), and commits.txt is appended to below.
: > /out/commits.txt
export CGO_ENABLED=0
export GOFLAGS=-trimpath

# Clone at a pinned tag AND verify the resolved commit against an expected
# SHA (#88).
#
# A git tag can be moved upstream and a Hub tag re-pushed; pinning the tag name
# alone trusts a mutable reference that becomes root inside every user's engine
# VM. The expected SHA per component lives in versions.env (<COMPONENT>_SHA);
# the build fails loudly on mismatch. To bump a component you update tag AND
# SHA together in one reviewed commit — the SHA is discoverable by running this
# with the var empty, which logs the resolved value instead of failing.
clone() { # repo tag dir expectedSHA
    git -c advice.detachedHead=false clone --depth 1 --branch "$2" "$1" "/src/$3" 2>&1 |
        grep -v 'is not a commit!' || true
    sha="$(git -C "/src/$3" rev-parse HEAD)"
    if [ -n "$4" ] && [ "$4" != "$sha" ]; then
        echo "FATAL: $3 tag $2 resolved to $sha, expected $4" >&2
        echo "  (a moved tag or a compromised source; refusing to build)" >&2
        exit 1
    fi
    if [ -z "$4" ]; then
        echo "    WARNING: no expected SHA pinned for $3; resolved $sha" >&2
    fi
    printf '%s %s %s\n' "$3" "$2" "$sha" >> /out/commits.txt
    collect_licences "$3"
    echo "    $3 $2 -> $sha (verified)"
}

# collect_licences copies a component's licence text out of the source tree we
# just cloned, so the rootfs can ship it (#205).
#
# Apache-2.0 section 4(a) requires giving recipients a copy of the licence, and
# every engine component here is Apache-2.0. The source is already on disk at
# this point, so this costs a cp and a few kilobytes -- the previous rootfs
# shipped eleven third-party binaries and no licence text at all.
#
# NOTICE is copied when upstream has one (moby and containerd do), which is
# section 4(d).
collect_licences() { # dir
    dest="/out/licenses/$1"
    mkdir -p "$dest"
    found=0
    for name in LICENSE LICENSE.txt LICENSE.md COPYING NOTICE NOTICE.txt; do
        if [ -f "/src/$1/$name" ]; then
            cp "/src/$1/$name" "$dest/$name"
            found=1
        fi
    done
    if [ "$found" -eq 0 ]; then
        # Loud, not fatal: a component that stops shipping a licence at the
        # root is a packaging change worth noticing, but the build should not
        # die at 3am over it. smoke-test.sh enforces the end state.
        echo "    WARNING: no licence file found in /src/$1" >&2
        rmdir "$dest" 2>/dev/null || true
    fi
}

echo "--- runc $RUNC_VERSION"
clone https://github.com/opencontainers/runc.git "$RUNC_VERSION" runc "${RUNC_SHA:-}"
# runc needs cgo for libseccomp; static via the seccomp buildtag.
(cd /src/runc && CGO_ENABLED=1 make static BUILDTAGS="seccomp" && cp runc /out/bin/)

echo "--- containerd $CONTAINERD_VERSION"
clone https://github.com/containerd/containerd.git "$CONTAINERD_VERSION" containerd "${CONTAINERD_SHA:-}"
(cd /src/containerd && make STATIC=1 binaries && \
    cp bin/containerd bin/containerd-shim-runc-v2 bin/ctr /out/bin/)

echo "--- moby (dockerd) $MOBY_TAG"
clone https://github.com/moby/moby.git "$MOBY_TAG" moby "${MOBY_SHA:-}"
# VERSION is what `dockerd --version` reports; without it moby stamps "dev",
# which the smoke test rejects and `hawser version` would misreport.
# docker-proxy ships alongside dockerd and is not optional: with userland-proxy
# enabled (the default) dockerd refuses to start without it -
# "userland-proxy is enabled, but userland-proxy-path is not set". Copying only
# dockerd produced a rootfs that imported cleanly and then would not boot.
# binary-proxy is a separate target: binary-daemon builds only dockerd, and the
# missing docker-proxy is what made the first published rootfs unbootable.
(cd /src/moby && VERSION="$ENGINE_VERSION" ./hack/make.sh binary-daemon binary-proxy && \
    for b in dockerd docker-proxy; do \
        found=$(find bundles -name "$b" -type f | head -1); \
        [ -n "$found" ] || { echo "moby build produced no $b"; exit 1; }; \
        cp "$found" /out/bin/; \
    done)

echo "--- buildkit $BUILDKIT_VERSION"
clone https://github.com/moby/buildkit.git "$BUILDKIT_VERSION" buildkit "${BUILDKIT_SHA:-}"
# Without these ldflags buildkit reports "v0.0.0+unknown" — same trap as moby's
# VERSION, and `hawser version` is supposed to report the truth.
bk_rev="$(git -C /src/buildkit rev-parse HEAD)"
bk_ld="-X github.com/moby/buildkit/version.Version=${BUILDKIT_VERSION#v}"
bk_ld="$bk_ld -X github.com/moby/buildkit/version.Revision=$bk_rev"
(cd /src/buildkit && go build -ldflags "$bk_ld" -o /out/bin/buildkitd ./cmd/buildkitd && \
    go build -ldflags "$bk_ld" -o /out/bin/buildctl ./cmd/buildctl)

strip /out/bin/* 2>/dev/null || true
chmod 0755 /out/bin/*
# Hand the artifacts back to the invoking user; we are root inside the container
# but /out is a host directory (see the HOST_UID comment in build.sh).
chown -R "${HOST_UID:-0}:${HOST_GID:-0}" /out
ls -la /out/bin
echo "--- licences collected"
ls /out/licenses
