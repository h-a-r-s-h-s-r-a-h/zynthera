#!/bin/bash
# Base system: full kernel, guest agent, time sync.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

# Optionally use a nearer or more reliable mirror for the build. seal.sh puts
# the default archive back, so the finished image does not depend on where it
# was built.
if [[ -n "${ZYN_APT_MIRROR:-}" ]]; then
  for f in /etc/apt/sources.list.d/ubuntu.sources /etc/apt/sources.list; do
    [[ -f "$f" ]] && sed -i "s|http://archive.ubuntu.com/ubuntu/\?|${ZYN_APT_MIRROR%/}/|g" "$f"
  done
  echo "${ZYN_APT_MIRROR%/}/" > /etc/zyn-build-mirror
fi

apt-get update
apt-get -y -o Dpkg::Options::=--force-confold dist-upgrade

# Cloud images ship a trimmed kernel that lacks some graphics and input
# modules. The desktop needs the full one.
apt-get install -y linux-generic
apt-get purge -y 'linux-image-*-kvm' 'linux-modules-*-kvm' linux-kvm linux-image-kvm 2>/dev/null || true
apt-get purge -y linux-virtual linux-image-virtual linux-headers-virtual 2>/dev/null || true

# qemu-guest-agent: clean shutdown and clock sync after a resume.
# util-linux-extra: provides hwclock, which the guest agent needs for its
#   set-time command. Ubuntu 24.04 no longer installs it by default; without
#   it "virsh domtime" answers "not currently supported".
# chrony: the second line of defence for the clock.
apt-get install -y qemu-guest-agent util-linux-extra chrony cloud-init cloud-guest-utils
systemctl enable qemu-guest-agent.service chrony.service

# A PC that was suspended for hours wakes up with a clock that is hours
# behind. chrony's default only steps the clock during its first three
# updates after start and slews afterwards, which would take days. Let it
# step whenever the clock is off by more than a second.
if grep -q '^makestep' /etc/chrony/chrony.conf; then
  sed -i 's/^makestep.*/makestep 1 -1/' /etc/chrony/chrony.conf
else
  echo 'makestep 1 -1' >> /etc/chrony/chrony.conf
fi
