#!/usr/bin/env bash
# Emit a minimal SPDX 2.3 SBOM for the rootfs from the pinned versions.
# Deliberately dependency-free: a security team can diff this against
# versions.env by eye. Richer scanning (syft) can layer on later.
#   ./sbom.sh versions.env out.spdx.json
set -euo pipefail

versions="${1:?usage: sbom.sh <versions.env> <output.spdx.json>}"
output="${2:?usage: sbom.sh <versions.env> <output.spdx.json>}"
# shellcheck disable=SC1090
. "$versions"

sep=""
pkg() { # name version download-location license
  printf '%s    {\n' "$sep"
  cat <<JSON
      "SPDXID": "SPDXRef-Package-$1",
      "name": "$1",
      "versionInfo": "$2",
      "downloadLocation": "$3",
      "filesAnalyzed": false,
      "licenseDeclared": "$4",
      "licenseConcluded": "$4",
      "supplier": "NOASSERTION"
JSON
  printf '    }'
  sep=",
"
}

{
  cat <<JSON
{
  "spdxVersion": "SPDX-2.3",
  "dataLicense": "CC0-1.0",
  "SPDXID": "SPDXRef-DOCUMENT",
  "name": "hawser-rootfs-${ENGINE_VERSION}-${ROOTFS_REVISION}",
  "documentNamespace": "https://github.com/hawserhq/hawser/spdx/rootfs-${ENGINE_VERSION}-${ROOTFS_REVISION}",
  "creationInfo": {
    "created": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
    "creators": ["Tool: hawser-rootfs-build", "Organization: Hawser"]
  },
  "packages": [
JSON
  pkg "alpine-minirootfs" "$ALPINE_ROOTFS_VERSION" \
      "https://dl-cdn.alpinelinux.org/alpine/$ALPINE_BRANCH/releases/x86_64/" "MIT AND GPL-2.0-only"
  pkg "moby" "$MOBY_TAG" "https://github.com/moby/moby.git" "Apache-2.0"
  pkg "containerd" "$CONTAINERD_VERSION" "https://github.com/containerd/containerd.git" "Apache-2.0"
  pkg "runc" "$RUNC_VERSION" "https://github.com/opencontainers/runc.git" "Apache-2.0"
  pkg "buildkit" "$BUILDKIT_VERSION" "https://github.com/moby/buildkit.git" "Apache-2.0"
  pkg "nvidia-cdi-hook" "$NVIDIA_CDI_HOOK_VERSION" "https://github.com/NVIDIA/nvidia-container-toolkit.git" "Apache-2.0"
  pkg "go" "$GO_VERSION" "https://go.dev/dl/" "BSD-3-Clause"
  # The agent is versioned by this repository itself; the rootfs revision is
  # its packaging version, and the git remote is its provenance.
  pkg "hawser-agent" "rootfs-r${ROOTFS_REVISION}" "https://github.com/hawserhq/hawser.git" "Apache-2.0"
  cat <<'JSON'

  ]
}
JSON
} > "$output"

echo "SBOM: $output"
