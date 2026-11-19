#!/usr/bin/env bash
# Installs Zynthera on one server: the web app, the API, the database and
# the VM host, all behind one domain with automatic HTTPS.
#
#   sudo scripts/install.sh --domain pc.example.com --email you@example.com
#
# Requirements:
#   - a fresh Ubuntu Server 24.04 LTS machine with hardware virtualization
#     (a bare-metal server, or a cloud VM with nested virtualization)
#   - a DNS record for the domain that points at this server
#   - ports 80 and 443 reachable from the internet
#
# Options:
#   --domain <name>        the public address of the site (required)
#   --email <address>      for HTTPS certificate notices and the admin login (required)
#   --smtp-host <host>     outgoing mail server (without it, sign-up emails cannot be sent)
#   --smtp-port <port>     default 587
#   --smtp-user <user>
#   --smtp-pass <password>
#   --mail-from <address>  default no-reply@<domain>
#   --skip-image           do not build the Ubuntu image now (run images/build.sh later)
#
# It is safe to run again: existing secrets, data and images are kept.
# Everything it does is in this file and in infra/host-setup/setup-host.sh.
set -euo pipefail

repo="$(cd "$(dirname "$0")/.." && pwd)"
cd "$repo"

say() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

domain="" email="" smtp_host="" smtp_port="587" smtp_user="" smtp_pass="" mail_from="" skip_image=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --domain) domain="${2:-}"; shift 2 ;;
    --email) email="${2:-}"; shift 2 ;;
    --smtp-host) smtp_host="${2:-}"; shift 2 ;;
    --smtp-port) smtp_port="${2:-}"; shift 2 ;;
    --smtp-user) smtp_user="${2:-}"; shift 2 ;;
    --smtp-pass) smtp_pass="${2:-}"; shift 2 ;;
    --mail-from) mail_from="${2:-}"; shift 2 ;;
    --skip-image) skip_image=1; shift ;;
    -h|--help) sed -n '2,26p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "unknown option: $1 (see --help)" ;;
  esac
done

[[ $EUID -eq 0 ]] || die "run this script as root: sudo $0 --domain ... --email ..."
[[ "$domain" =~ ^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$ ]] || die "--domain must be a host name such as pc.example.com"
[[ "$email" =~ ^[^[:space:]@]+@[^[:space:]@]+\.[^[:space:]@]+$ ]] || die "--email must be an email address"
[[ "$smtp_port" =~ ^[0-9]+$ ]] || die "--smtp-port must be a number"
mail_from="${mail_from:-no-reply@${domain}}"

env_file=/etc/zynthera/control-plane.env
agent_env=/etc/zynthera/agent.env
compose=(docker compose --env-file "$env_file" -f "$repo/infra/docker-compose.prod.yml")
version="$(git -C "$repo" rev-parse --short HEAD 2>/dev/null || date +%Y%m%d%H%M)"

# ---------------------------------------------------------------- 1. the VM host
say "1/8  Preparing the VM host (KVM, libvirt, network, firewall)"
bash "$repo/infra/host-setup/setup-host.sh"

# ---------------------------------------------------------------- 2. Docker and Caddy
say "2/8  Installing Docker and Caddy"
export DEBIAN_FRONTEND=noninteractive
apt-get install -y -qq docker.io docker-compose-v2 caddy openssl uuid-runtime unzip xorriso >/dev/null
systemctl enable --now docker.service >/dev/null
docker compose version >/dev/null || die "docker compose is not available"

# ---------------------------------------------------------------- 3. secrets and settings
say "3/8  Writing settings"
# get <file> <KEY>: the current value of KEY in an env file, or nothing.
get() { [[ -f "$1" ]] && sed -n "s/^$2=//p" "$1" | tail -n 1 || true; }
# keep <KEY> <command...>: reuse the stored secret, or generate a new one.
keep() { local key="$1" old; shift; old="$(get "$env_file" "$key")"; if [[ -n "$old" ]]; then printf '%s' "$old"; else "$@"; fi; }

first_install=0
[[ -f "$env_file" ]] || first_install=1

pg_password="$(keep POSTGRES_PASSWORD openssl rand -hex 24)"
secrets_key="$(keep ZYN_SECRETS_KEY openssl rand -base64 32)"
admin_password="$(keep ZYN_SEED_ADMIN_PASSWORD openssl rand -base64 18)"
host_id="$(keep ZYN_AGENT_HOST_ID uuidgen)"
agent_token="$(keep ZYN_AGENT_TOKEN openssl rand -hex 32)"
stream_secret="$(keep ZYN_AGENT_STREAM_SECRET openssl rand -hex 32)"
# Settings given on the command line win; otherwise keep what is stored.
smtp_host="${smtp_host:-$(get "$env_file" ZYN_SMTP_HOST)}"
smtp_user="${smtp_user:-$(get "$env_file" ZYN_SMTP_USER)}"
smtp_pass="${smtp_pass:-$(get "$env_file" ZYN_SMTP_PASS)}"

install -d -m 0750 /etc/zynthera
umask 077
cat >"$env_file" <<EOF
# Zynthera control plane settings. Written by scripts/install.sh.
# Keep this file private and back it up: ZYN_SECRETS_KEY is needed to read
# the host credentials stored in the database.

# ---- Docker Compose
POSTGRES_USER=zynthera
POSTGRES_PASSWORD=${pg_password}
POSTGRES_DB=zynthera
POSTGRES_PORT=5432
REDIS_PORT=6379
ZYN_WEB_PORT=3000
ZYN_ENV_FILE=${env_file}
ZYN_VERSION=${version}

# ---- API
ZYN_ENV=production
ZYN_API_ADDR=127.0.0.1:8080
ZYN_DATABASE_URL=postgres://zynthera:${pg_password}@127.0.0.1:5432/zynthera?sslmode=disable
ZYN_REDIS_URL=redis://127.0.0.1:6379/0
ZYN_WEB_ORIGIN=https://${domain}
ZYN_SECRETS_KEY=${secrets_key}
ZYN_SEED_ADMIN_EMAIL=${email}
ZYN_SEED_ADMIN_PASSWORD=${admin_password}

# ---- outgoing email (needed for sign-up confirmation and password reset)
ZYN_SMTP_HOST=${smtp_host}
ZYN_SMTP_PORT=${smtp_port}
ZYN_SMTP_USER=${smtp_user}
ZYN_SMTP_PASS=${smtp_pass}
ZYN_MAIL_FROM=Zynthera <${mail_from}>

# ---- this server as a VM host (the same values are in agent.env)
ZYN_AGENT_HOST_ID=${host_id}
ZYN_AGENT_TOKEN=${agent_token}
ZYN_AGENT_STREAM_SECRET=${stream_secret}
ZYN_AGENT_ADDR=127.0.0.1:9090
ZYN_SEED_HOST_NAME=host-1
ZYN_SEED_HOST_PUBLIC_URL=https://${domain}
EOF
cat >"$agent_env" <<EOF
# Zynthera host agent settings. Written by scripts/install.sh.
ZYN_ENV=production
ZYN_AGENT_ADDR=127.0.0.1:9090
ZYN_AGENT_HOST_ID=${host_id}
ZYN_AGENT_TOKEN=${agent_token}
ZYN_AGENT_STREAM_SECRET=${stream_secret}
ZYN_AGENT_API_URL=http://127.0.0.1:8080
ZYN_AGENT_WEB_ORIGIN=https://${domain}
ZYN_VM_DRIVER=libvirt
ZYN_STORAGE_DRIVER=qcow2
ZYN_DATA_DIR=/var/lib/zynthera
ZYN_LIBVIRT_URI=qemu:///system
ZYN_LIBVIRT_NETWORK=zynthera
ZYN_MAX_UPLOAD_MB=2048
EOF
chmod 0600 "$env_file" "$agent_env"
umask 022

# ---------------------------------------------------------------- 4. build
say "4/8  Building Zynthera (a few minutes the first time)"
"${compose[@]}" build --quiet
# The agent and the file bridge run outside Docker. They are built inside
# Docker and copied out, so the server needs no Go toolchain.
rm -rf "$repo/bin.new"
docker build --quiet -f "$repo/infra/docker/go.Dockerfile" --target export --output "type=local,dest=$repo/bin.new" "$repo" >/dev/null
install -m 0755 "$repo/bin.new/zynthera-agent" /usr/local/bin/zynthera-agent
install -m 0755 "$repo/bin.new/zynthera-file-bridge" /usr/local/lib/zynthera-file-bridge 2>/dev/null \
  || { install -d /usr/local/lib; install -m 0755 "$repo/bin.new/zynthera-file-bridge" /usr/local/lib/zynthera-file-bridge; }
rm -rf "$repo/bin.new"

# ---------------------------------------------------------------- 5. control plane
say "5/8  Starting the database, the API and the web app"
"${compose[@]}" up -d --wait
"${compose[@]}" run --rm api seed >/dev/null
echo "control plane is up; admin account and this host are registered"

# ---------------------------------------------------------------- 6. agent
say "6/8  Starting the host agent"
install -m 0644 "$repo/infra/systemd/zynthera-agent.service" /etc/systemd/system/zynthera-agent.service
systemctl daemon-reload
systemctl enable zynthera-agent.service >/dev/null
systemctl restart zynthera-agent.service
for _ in $(seq 1 30); do
  curl -fsS -o /dev/null http://127.0.0.1:9090/health && break
  sleep 1
done
curl -fsS -o /dev/null http://127.0.0.1:9090/health || die "the agent did not start; see: journalctl -u zynthera-agent -n 50"

# ---------------------------------------------------------------- 7. HTTPS
say "7/8  Configuring HTTPS for ${domain}"
install -d -m 0755 /var/log/caddy
chown caddy:caddy /var/log/caddy 2>/dev/null || true
sed -e "s/__ZYN_ACME_EMAIL__/${email}/g" -e "s/__ZYN_DOMAIN__/${domain}/g" \
  "$repo/infra/caddy/single-server.Caddyfile" >/etc/caddy/Caddyfile
caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile >/dev/null 2>&1 \
  || die "the generated /etc/caddy/Caddyfile is not valid; run: caddy validate --config /etc/caddy/Caddyfile"
systemctl enable caddy.service >/dev/null
systemctl reload caddy.service 2>/dev/null || systemctl restart caddy.service

# ---------------------------------------------------------------- 8. OS image
image_slug=ubuntu-24.04-xfce
image_version="$("${compose[@]}" exec -T postgres psql -U zynthera -d zynthera -Atc \
  "select current_version from os_images where slug = '${image_slug}'" 2>/dev/null || true)"
image_version="${image_version:-$(date +%Y.%m).1}"
image_file="/var/lib/zynthera/images/${image_slug}/${image_version}.qcow2"
if [[ -f "$image_file" ]]; then
  say "8/8  OS image already present: ${image_file}"
elif [[ $skip_image -eq 1 ]]; then
  say "8/8  Skipping the OS image (--skip-image)"
  echo "Build it later with:"
  echo "  sudo ZYN_BRIDGE_BINARY=/usr/local/lib/zynthera-file-bridge ZYN_IMAGES_DIR=/var/lib/zynthera/images images/build.sh ${image_slug} ${image_version}"
else
  say "8/8  Building the Ubuntu desktop image (20 to 30 minutes)"
  if ! command -v packer >/dev/null; then
    # Packer from HashiCorp's release server, checked against its checksum file.
    packer_version="$(curl -fsSL https://checkpoint-api.hashicorp.com/v1/check/packer | sed -n 's/.*"current_version":"\([^"]*\)".*/\1/p')"
    [[ -n "$packer_version" ]] || die "could not find the current Packer version; install Packer and run this script again"
    tmp="$(mktemp -d)"
    curl -fsSL -o "$tmp/packer.zip" "https://releases.hashicorp.com/packer/${packer_version}/packer_${packer_version}_linux_amd64.zip"
    curl -fsSL "https://releases.hashicorp.com/packer/${packer_version}/packer_${packer_version}_SHA256SUMS" \
      | grep "linux_amd64.zip" | sed "s| .*| $tmp/packer.zip|" | sha256sum -c - >/dev/null \
      || die "the Packer download does not match its checksum"
    unzip -oq "$tmp/packer.zip" packer -d /usr/local/bin
    rm -rf "$tmp"
  fi
  ZYN_BRIDGE_BINARY=/usr/local/lib/zynthera-file-bridge ZYN_IMAGES_DIR=/var/lib/zynthera/images \
    "$repo/images/build.sh" "$image_slug" "$image_version"
fi

# ---------------------------------------------------------------- done
say "Zynthera is installed"
"$repo/scripts/doctor.sh" || true
cat <<EOF

  Open:   https://${domain}
  Admin:  ${email}
EOF
if [[ $first_install -eq 1 ]]; then
  echo "  Admin password (shown once, also in ${env_file}):  ${admin_password}"
else
  echo "  Admin password: unchanged (it is in ${env_file} as ZYN_SEED_ADMIN_PASSWORD)"
fi
if [[ -z "$smtp_host" ]]; then
  cat <<EOF

  No mail server is configured, so new users cannot confirm their email yet.
  Run this script again with --smtp-host, --smtp-user and --smtp-pass.
EOF
fi
cat <<EOF

  Check the installation:  sudo scripts/doctor.sh
  Update later:            git pull && sudo scripts/update.sh
EOF
