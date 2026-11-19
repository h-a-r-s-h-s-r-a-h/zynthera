#!/usr/bin/env bash
# Checks a Zynthera server and says what is wrong and how to fix it.
#
#   sudo scripts/doctor.sh
#
# Exit code 0 means every check passed.
set -uo pipefail

env_file=/etc/zynthera/control-plane.env
agent_env=/etc/zynthera/agent.env
repo="$(cd "$(dirname "$0")/.." && pwd)"
failures=0

ok()   { printf '  \033[32mok\033[0m    %s\n' "$1"; }
bad()  { printf '  \033[31mFAIL\033[0m  %s\n        fix: %s\n' "$1" "$2"; failures=$((failures + 1)); }
warn() { printf '  \033[33mwarn\033[0m  %s\n        %s\n' "$1" "$2"; }
get()  { [[ -f "$1" ]] && sed -n "s/^$2=//p" "$1" | tail -n 1 || true; }

[[ $EUID -eq 0 ]] || { echo "run as root: sudo $0" >&2; exit 2; }
[[ -f "$env_file" ]] || { echo "Zynthera is not installed here ($env_file is missing). Run scripts/install.sh." >&2; exit 2; }

origin="$(get "$env_file" ZYN_WEB_ORIGIN)"
domain="${origin#https://}"
compose=(docker compose --env-file "$env_file" -f "$repo/infra/docker-compose.prod.yml")
psql() { "${compose[@]}" exec -T postgres psql -U zynthera -d zynthera -Atc "$1" 2>/dev/null; }

echo "Virtualization"
if [[ -e /dev/kvm ]]; then ok "KVM is available"; else bad "/dev/kvm is missing" "enable virtualization in the BIOS, or use a server with nested virtualization"; fi
if systemctl is-active --quiet libvirtd; then ok "libvirt is running"; else bad "libvirtd is not running" "systemctl start libvirtd"; fi
if [[ "$(virsh net-info zynthera 2>/dev/null | awk '/^Active:/{print $2}')" == "yes" ]]; then ok "VM network 'zynthera' is active"; else bad "VM network 'zynthera' is not active" "virsh net-start zynthera   (or run infra/host-setup/setup-host.sh)"; fi
if nft list table inet zynthera >/dev/null 2>&1; then ok "VM firewall is loaded"; else bad "VM firewall is not loaded: VMs are not isolated" "systemctl restart zynthera-firewall"; fi
if grep -q '^ON_SHUTDOWN=suspend' /etc/default/libvirt-guests 2>/dev/null; then ok "a host shutdown suspends running VMs"; else bad "a host shutdown would kill running VMs" "run infra/host-setup/setup-host.sh"; fi

echo "Control plane"
for svc in postgres redis api web; do
  state="$("${compose[@]}" ps --format '{{.State}}' "$svc" 2>/dev/null | head -n 1)"
  if [[ "$state" == "running" ]]; then ok "$svc container is running"; else bad "$svc container is ${state:-not created}" "cd $repo && sudo scripts/update.sh   (logs: docker compose -f infra/docker-compose.prod.yml logs $svc)"; fi
done
health="$(curl -fsS -m 5 http://127.0.0.1:8080/health 2>/dev/null || true)"
if [[ "$health" == *'"status":"ok"'* ]]; then ok "API is healthy (database and queue reachable)"; else bad "API health check failed: ${health:-no answer}" "docker compose -f infra/docker-compose.prod.yml logs api"; fi

echo "VM host"
if systemctl is-active --quiet zynthera-agent; then ok "agent is running"; else bad "agent is not running" "systemctl restart zynthera-agent; journalctl -u zynthera-agent -n 50"; fi
if [[ "$(get "$agent_env" ZYN_AGENT_TOKEN)" == "$(get "$env_file" ZYN_AGENT_TOKEN)" && -n "$(get "$agent_env" ZYN_AGENT_TOKEN)" ]]; then ok "agent and API share the same token"; else bad "agent.env and control-plane.env disagree on ZYN_AGENT_TOKEN" "run scripts/install.sh again"; fi
age="$(psql "select coalesce(extract(epoch from now() - max(last_heartbeat_at))::int, -1) from hosts")"
if [[ "$age" =~ ^[0-9]+$ && "$age" -lt 45 ]]; then ok "host is online (last heartbeat ${age}s ago)"; else bad "the API has not heard from the agent recently" "journalctl -u zynthera-agent -n 50"; fi
image_version="$(psql "select current_version from os_images where slug = 'ubuntu-24.04-xfce' and enabled")"
image_file="/var/lib/zynthera/images/ubuntu-24.04-xfce/${image_version}.qcow2"
if [[ -n "$image_version" && -f "$image_file" ]]; then ok "OS image present: ${image_file}"; else bad "OS image missing: ${image_file}" "sudo ZYN_BRIDGE_BINARY=/usr/local/lib/zynthera-file-bridge ZYN_IMAGES_DIR=/var/lib/zynthera/images images/build.sh ubuntu-24.04-xfce ${image_version:-<version>}"; fi
free_gb="$(df -BG --output=avail /var/lib/zynthera 2>/dev/null | tail -n 1 | tr -dc '0-9')"
if [[ -n "$free_gb" && "$free_gb" -ge 50 ]]; then ok "${free_gb} GB free for VM disks"; else warn "only ${free_gb:-?} GB free for VM disks" "each PC needs its disk size plus its RAM size on /var/lib/zynthera"; fi

echo "Public access"
if systemctl is-active --quiet caddy; then ok "Caddy is running"; else bad "Caddy is not running" "systemctl restart caddy; journalctl -u caddy -n 50"; fi
public="$(curl -fsS -m 10 "${origin}/api/health" 2>/dev/null || true)"
if [[ "$public" == *'"status":"ok"'* ]]; then ok "${origin} answers over HTTPS"; else bad "${origin} is not reachable over HTTPS" "check that DNS for ${domain} points here and that ports 80 and 443 are open; journalctl -u caddy -n 50"; fi
internal="$(curl -s -o /dev/null -m 10 -w '%{http_code}' -X POST "${origin}/internal/agent/heartbeat" 2>/dev/null || true)"
if [[ "$internal" == "404" ]]; then ok "internal agent routes are closed to the internet"; else bad "${origin}/internal/ answered ${internal:-nothing} (expected 404)" "reinstall /etc/caddy/Caddyfile with scripts/install.sh"; fi
if [[ -n "$(get "$env_file" ZYN_SMTP_HOST)" ]]; then ok "a mail server is configured"; else warn "no mail server configured" "new users cannot confirm their email; run install.sh again with --smtp-host"; fi

echo
if [[ $failures -eq 0 ]]; then echo "All checks passed."; else echo "${failures} check(s) failed."; fi
exit $(( failures > 0 ? 1 : 0 ))
