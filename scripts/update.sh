#!/usr/bin/env bash
# Updates a Zynthera server to the code in this checkout.
#
#   git pull && sudo scripts/update.sh
#
# Running PCs are not touched: virtual machines belong to libvirt, and
# neither the control plane nor the agent restarting affects them. People
# with an open desktop see a short reconnect.
set -euo pipefail

repo="$(cd "$(dirname "$0")/.." && pwd)"
env_file=/etc/zynthera/control-plane.env
say() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

[[ $EUID -eq 0 ]] || die "run as root: sudo $0"
[[ -f "$env_file" ]] || die "Zynthera is not installed here; run scripts/install.sh first"
cd "$repo"

version="$(git rev-parse --short HEAD 2>/dev/null || date +%Y%m%d%H%M)"
sed -i "s/^ZYN_VERSION=.*/ZYN_VERSION=${version}/" "$env_file"
compose=(docker compose --env-file "$env_file" -f "$repo/infra/docker-compose.prod.yml")

say "Building version ${version}"
"${compose[@]}" build --quiet
rm -rf "$repo/bin.new"
docker build --quiet -f "$repo/infra/docker/go.Dockerfile" --target export --output "type=local,dest=$repo/bin.new" "$repo" >/dev/null

say "Updating the control plane (database migrations run first)"
"${compose[@]}" up -d --wait
"${compose[@]}" run --rm api seed >/dev/null

say "Updating the host agent"
install -m 0755 "$repo/bin.new/zynthera-agent" /usr/local/bin/zynthera-agent
install -m 0755 "$repo/bin.new/zynthera-file-bridge" /usr/local/lib/zynthera-file-bridge
rm -rf "$repo/bin.new"
install -m 0644 "$repo/infra/systemd/zynthera-agent.service" /etc/systemd/system/zynthera-agent.service
install -m 0644 "$repo/infra/host-setup/firewall.nft" /etc/zynthera/firewall.nft
systemctl daemon-reload
systemctl reload zynthera-firewall.service
systemctl restart zynthera-agent.service

say "Removing old images"
docker image prune -f >/dev/null

"$repo/scripts/doctor.sh"
