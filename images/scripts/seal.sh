#!/bin/bash
# Seals the image: removes everything that must be unique per PC, then
# powers off. Runs as Packer's shutdown command, as root.
set -uo pipefail
export DEBIAN_FRONTEND=noninteractive

# No SSH server in the finished image. (Packer is connected over SSH right
# now; masking only takes effect on the next boot.)
systemctl disable ssh.service ssh.socket >/dev/null 2>&1
systemctl mask ssh.service ssh.socket >/dev/null 2>&1
rm -f /etc/ssh/ssh_host_*

# If the build used another mirror, point APT back at the default archive.
if [[ -f /etc/zyn-build-mirror ]]; then
  mirror="$(cat /etc/zyn-build-mirror)"
  for f in /etc/apt/sources.list.d/ubuntu.sources /etc/apt/sources.list; do
    [[ -f "$f" ]] && sed -i "s|${mirror}|http://archive.ubuntu.com/ubuntu/|g" "$f"
  done
  rm -f /etc/zyn-build-mirror
fi

apt-get -y autoremove --purge >/dev/null 2>&1
apt-get clean
rm -rf /var/lib/apt/lists/*

# cloud-init must run again, from scratch, on each clone's first boot.
cloud-init clean --logs --seed >/dev/null 2>&1
rm -f /etc/netplan/50-cloud-init.yaml

# Every clone needs its own machine id.
truncate -s 0 /etc/machine-id
rm -f /var/lib/dbus/machine-id
ln -sf /etc/machine-id /var/lib/dbus/machine-id

# The build user and its traces.
rm -f /etc/sudoers.d/90-cloud-init-users
find /var/log -type f -exec truncate -s 0 {} +
rm -rf /tmp/* /var/tmp/* /root/.bash_history /home/zynbuild/.bash_history

# Let the host reclaim the free space when the image is compressed.
fstrim -av >/dev/null 2>&1

# The build user is logged in (this script runs through its SSH session), so
# it is removed by a one-shot unit at the very end of shutdown.
cat > /etc/systemd/system/zyn-remove-build-user.service <<'UNIT'
[Unit]
Description=Remove the image build user
DefaultDependencies=no
Before=poweroff.target halt.target shutdown.target
After=umount.target

[Service]
Type=oneshot
ExecStart=/bin/sh -c 'pkill -KILL -u zynbuild; userdel -rf zynbuild; rm -f /etc/systemd/system/zyn-remove-build-user.service /etc/systemd/system/shutdown.target.wants/zyn-remove-build-user.service /usr/local/sbin/zyn-seal'

[Install]
WantedBy=shutdown.target
UNIT
systemctl enable zyn-remove-build-user.service >/dev/null 2>&1

sync
systemctl poweroff
