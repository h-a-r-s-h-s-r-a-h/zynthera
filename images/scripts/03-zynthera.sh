#!/bin/bash
# Zynthera pieces: the file bridge, no screen blanking, no guest sleep.
set -euo pipefail

# --- file bridge: installed, not enabled (cloud-init enables it per VM)
install -m 0755 -o root -g root /tmp/zynthera-file-bridge /usr/local/bin/zynthera-file-bridge
install -m 0644 -o root -g root /tmp/zynthera-file-bridge.service /etc/systemd/system/zynthera-file-bridge.service
install -d -m 0755 /etc/zynthera
rm -f /tmp/zynthera-file-bridge /tmp/zynthera-file-bridge.service

# --- the sealing script used as Packer's shutdown command
install -m 0755 -o root -g root /tmp/zyn-seal /usr/local/sbin/zyn-seal
rm -f /tmp/zyn-seal

# --- never blank the screen
install -d -m 0755 /etc/X11/xorg.conf.d
cat > /etc/X11/xorg.conf.d/10-zynthera-noblank.conf <<'XORG'
Section "ServerFlags"
    Option "BlankTime"   "0"
    Option "StandbyTime" "0"
    Option "SuspendTime" "0"
    Option "OffTime"     "0"
EndSection
Section "Extensions"
    Option "DPMS" "Disable"
EndSection
XORG

# --- the host does the suspending; the guest must never sleep by itself
systemctl mask sleep.target suspend.target hibernate.target hybrid-sleep.target suspend-then-hibernate.target

# --- LightDM: the per-VM autologin drop-in is written by cloud-init
install -d -m 0755 /etc/lightdm/lightdm.conf.d
cat > /etc/lightdm/lightdm.conf.d/40-zynthera.conf <<'LDM'
[Seat:*]
user-session=xfce
greeter-hide-users=false
LDM

# --- default resolution, in case the virtual display offers no preferred mode
cat > /etc/xdg/autostart/zynthera-resolution.desktop <<'DESK'
[Desktop Entry]
Type=Application
Name=Zynthera display size
Exec=sh -c 'xrandr --output "$(xrandr | awk "/ connected/{print \$1; exit}")" --mode 1920x1080 || true'
NoDisplay=true
X-GNOME-Autostart-enabled=true
DESK

# --- no SSH server listening: the only ways in are the web desktop and files
systemctl disable ssh.service ssh.socket 2>/dev/null || true
