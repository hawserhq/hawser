#!/bin/sh
# Spike A step 3 (runs INSIDE the skrog-spike distro, as root, invoked by 02-import.ps1).
# $1 = /mnt/... path to docker-29.7.2.tgz
set -eu
TGZ="$1"

apk add --no-cache socat iptables ip6tables iproute2 ca-certificates

tar -xzf "$TGZ" -C /usr/local/bin --strip-components=1
mkdir -p /etc/docker /var/log
cat > /etc/docker/daemon.json <<'EOF'
{
  "log-driver": "json-file",
  "log-opts": { "max-size": "10m", "max-file": "3" }
}
EOF

echo "setup OK: $(dockerd --version)"
