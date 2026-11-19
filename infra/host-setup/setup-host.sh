#!/usr/bin/env bash
# Prepares a Linux server to run Zynthera virtual machines.
#
#   sudo infra/host-setup/setup-host.sh
#
# It is safe to run again: every step checks what is already there.
# Tested target: Ubuntu Server 24.04 LTS. It should also work on Debian 12.
#
# What it does (spec section 12.8):
#   1. installs KVM, libvirt and the tools the agent uses
#   2. creates /var/lib/zynthera and /etc/zynthera
#   3. defines the private VM network "zynthera" (bridge zynbr0, 10.210.0.0/16)
#   4. loads the firewall rules that isolate VMs, and keeps them across reboots
#   5. makes a host shutdown suspend running VMs instead of killing them
#   6. compresses saved memory images with zstd
#
# It does NOT install the agent, Caddy or the OS image; scripts/install.sh
# does everything in one go, or see docs/DEPLOY_HOST.md for the manual steps.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo="$(cd "$here/../.." && pwd)"

say() { printf '\n==> %s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

[[ $EUID -eq 0 ]] || die "run this script as root (sudo $0)"
[[ -r /etc/os-release ]] || die "cannot identify the operating system"
# shellcheck disable=SC1091
. /etc/os-release
case "${ID:-}:${ID_LIKE:-}" in
  ubuntu:*|debian:*|*:*debian*|*:*ubuntu*) ;;
  *) die "this script supports Ubuntu and Debian; found ${PRETTY_NAME:-unknown}" ;;
esac

# ---------------------------------------------------------------- 0. KVM
say "Checking for KVM"
if ! grep -Eq '(vmx|svm)' /proc/cpuinfo; then
  die "this CPU has no hardware virtualization (or it is disabled in the BIOS, or this is a VM without nested virtualization)"
fi

# ---------------------------------------------------------------- 1. packages
say "Installing packages"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq \
  qemu-system-x86 qemu-utils \
  libvirt-daemon-system libvirt-clients libvirt-daemon-config-nwfilter \
  cloud-image-utils libguestfs-tools nftables zstd curl ca-certificates

systemctl enable --now libvirtd.service >/dev/null
[[ -e /dev/kvm ]] || die "/dev/kvm is missing even though the CPU supports virtualization; check that the kvm kernel modules load (modprobe kvm_intel or kvm_amd)"

# ---------------------------------------------------------------- 2. directories
say "Creating directories"
# 0711: QEMU runs as its own user and must reach the disk files, but nobody
# may list what is there.
install -d -m 0711 -o root -g root /var/lib/zynthera /var/lib/zynthera/images /var/lib/zynthera/vms
install -d -m 0750 -o root -g root /etc/zynthera

# ---------------------------------------------------------------- 3. network
say "Setting up the VM network"
if ! virsh net-info zynthera >/dev/null 2>&1; then
  virsh net-define "$here/network.xml" >/dev/null
  echo "defined network zynthera"
fi
virsh net-autostart zynthera >/dev/null
if [[ "$(virsh net-info zynthera | awk '/^Active:/{print $2}')" != "yes" ]]; then
  virsh net-start zynthera >/dev/null
  echo "started network zynthera"
fi
virsh nwfilter-info clean-traffic >/dev/null 2>&1 \
  || die "the libvirt network filter 'clean-traffic' is missing (package libvirt-daemon-config-nwfilter)"

# ---------------------------------------------------------------- 4. firewall
say "Loading the VM firewall"
install -m 0644 -o root -g root "$here/firewall.nft" /etc/zynthera/firewall.nft
install -m 0644 -o root -g root "$repo/infra/systemd/zynthera-firewall.service" /etc/systemd/system/zynthera-firewall.service
systemctl daemon-reload
systemctl enable zynthera-firewall.service >/dev/null
systemctl restart zynthera-firewall.service
nft list table inet zynthera >/dev/null || die "the firewall table did not load"

# ---------------------------------------------------------------- 5. host shutdown
say "Making a host shutdown suspend running VMs"
guests=/etc/default/libvirt-guests
set_kv() { # set_kv <file> <KEY> <value>: replace the line (commented or not), or append it
  local file="$1" key="$2" value="$3"
  if grep -Eq "^#?[[:space:]]*${key}=" "$file"; then
    sed -i -E "s|^#?[[:space:]]*${key}=.*|${key}=${value}|" "$file"
  else
    printf '%s=%s\n' "$key" "$value" >>"$file"
  fi
}
touch "$guests"
# On shutdown: save each VM's memory to disk. On boot: start nothing; a VM
# resumes when its owner comes back.
set_kv "$guests" ON_SHUTDOWN suspend
set_kv "$guests" ON_BOOT ignore
# Saving several large VMs takes time; do a few at once and wait for them.
set_kv "$guests" PARALLEL_SHUTDOWN 4
set_kv "$guests" SHUTDOWN_TIMEOUT 600
systemctl enable libvirt-guests.service >/dev/null

# ---------------------------------------------------------------- 6. saved image compression
say "Compressing saved memory images with zstd"
qemu_conf=/etc/libvirt/qemu.conf
if grep -Eq '^save_image_format[[:space:]]*=[[:space:]]*"zstd"' "$qemu_conf"; then
  echo "already set"
else
  if grep -Eq '^#?[[:space:]]*save_image_format[[:space:]]*=' "$qemu_conf"; then
    sed -i -E 's|^#?[[:space:]]*save_image_format[[:space:]]*=.*|save_image_format = "zstd"|' "$qemu_conf"
  else
    printf '\nsave_image_format = "zstd"\n' >>"$qemu_conf"
  fi
  # Restarting libvirtd does not touch running VMs.
  systemctl restart libvirtd.service
  echo "set save_image_format = zstd"
fi

say "Host is ready"
cat <<EOF
  KVM:        $(virsh version --daemon 2>/dev/null | awk -F': ' '/Running hypervisor/{print $2}')
  libvirt:    $(virsh --version)
  network:    zynthera ($(virsh net-info zynthera | awk '/^Bridge:/{print $2}'), active)
  firewall:   table inet zynthera loaded
  data:       /var/lib/zynthera
  config:     /etc/zynthera

Next: install the agent and an OS image (scripts/install.sh does both), or
follow docs/DEPLOY_HOST.md.
EOF
