#!/usr/bin/env bash
# One-time VPS preparation: Docker + compose, nginx, certbot, firewall, swap.
# Idempotent - safe to run again. Run as root (or with sudo).
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then echo "run as root"; exit 1; fi
. /etc/os-release
echo "== OS: $PRETTY_NAME"

export DEBIAN_FRONTEND=noninteractive
apt-get update -y
apt-get install -y ca-certificates curl gnupg nginx certbot python3-certbot-nginx ufw openssl jq

if ! command -v docker >/dev/null 2>&1; then
  curl -fsSL https://get.docker.com | sh
fi
systemctl enable --now docker
docker compose version

# Swap: the Go image build and MongoDB both want headroom on a small VPS.
if ! swapon --show | grep -q .; then
  mem_mb=$(awk '/MemTotal/ {print int($2/1024)}' /proc/meminfo)
  if [ "$mem_mb" -lt 7000 ]; then
    fallocate -l 2G /swapfile && chmod 600 /swapfile && mkswap /swapfile && swapon /swapfile
    grep -q '^/swapfile' /etc/fstab || echo '/swapfile none swap sw 0 0' >> /etc/fstab
    echo "== added 2G swap (RAM ${mem_mb} MB)"
  fi
fi

# Firewall: SSH, HTTP, HTTPS only. Docker publishes nothing public in prod.
ufw allow OpenSSH >/dev/null
ufw allow 'Nginx Full' >/dev/null
ufw --force enable >/dev/null
ufw status | head -8

mkdir -p /var/www/wintratrade/client /var/www/wintratrade/admin /opt/wintratrade
echo "== bootstrap done"
