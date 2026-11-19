#!/bin/bash
# Base system: full kernel, guest agent, time sync.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

apt-get update
apt-get -y -o Dpkg::Options::=--force-confold dist-upgrade

# Cloud images ship a trimmed kernel that lacks some graphics and input
# modules. The desktop needs the full one.
apt-get install -y linux-generic
apt-get purge -y 'linux-image-*-kvm' 'linux-modules-*-kvm' linux-kvm linux-image-kvm 2>/dev/null || true
apt-get purge -y linux-virtual linux-image-virtual linux-headers-virtual 2>/dev/null || true

# qemu-guest-agent: clean shutdown and clock sync after a resume.
# chrony: the second line of defence for the clock.
apt-get install -y qemu-guest-agent chrony cloud-init cloud-guest-utils
systemctl enable qemu-guest-agent.service chrony.service
