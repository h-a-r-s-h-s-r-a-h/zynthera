#!/bin/bash
# Desktop: XFCE, LightDM, a browser, the usual small tools, and Flatpak.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

# LightDM is the only display manager; answer the question in advance.
echo "lightdm shared/default-x-display-manager select lightdm" | debconf-set-selections

apt-get install -y \
  xorg xfce4 xfce4-terminal lightdm lightdm-gtk-greeter dbus-x11 \
  thunar thunar-archive-plugin xarchiver mousepad ristretto \
  fonts-dejavu fonts-liberation fonts-noto-color-emoji \
  xdg-utils xdg-user-dirs unzip zip curl wget ca-certificates \
  flatpak gnome-software gnome-software-plugin-flatpak

# Firefox as a normal package from Mozilla's own repository. Ubuntu's
# "firefox" package is a snap, which is slow to start in a VM.
install -d -m 0755 /etc/apt/keyrings
curl -fsSL https://packages.mozilla.org/apt/repo-signing-key.gpg -o /etc/apt/keyrings/packages.mozilla.org.asc
echo "deb [signed-by=/etc/apt/keyrings/packages.mozilla.org.asc] https://packages.mozilla.org/apt mozilla main" \
  > /etc/apt/sources.list.d/mozilla.list
cat > /etc/apt/preferences.d/mozilla <<'PIN'
Package: *
Pin: origin packages.mozilla.org
Pin-Priority: 1000
PIN
apt-get update
apt-get install -y firefox

# An app store: Flathub.
flatpak remote-add --if-not-exists flathub https://dl.flathub.org/repo/flathub.flatpakrepo

# Screen lockers and power management would lock or blank the screen while
# the PC is suspended by the host, which confuses people after a resume. The
# web login already protects the session.
apt-get purge -y xfce4-screensaver light-locker xscreensaver xfce4-power-manager 2>/dev/null || true

systemctl set-default graphical.target
systemctl enable lightdm.service
