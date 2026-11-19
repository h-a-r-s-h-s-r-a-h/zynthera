#!/bin/sh
# Starts Xvnc (an X server and a VNC server in one process) on display :1,
# port 5901, then runs XFCE on it.
set -eu

rm -f /tmp/.X1-lock /tmp/.X11-unix/X1

Xvnc :1 \
  -rfbport 5901 \
  -SecurityTypes None \
  -geometry 1920x1080 \
  -depth 24 \
  -AlwaysShared \
  -localhost=0 &
xvnc_pid=$!

# Wait for the X socket before starting the desktop.
i=0
while [ ! -S /tmp/.X11-unix/X1 ] && [ "$i" -lt 50 ]; do
  i=$((i + 1))
  sleep 0.1
done

DISPLAY=:1 dbus-launch --exit-with-session startxfce4 &

wait "$xvnc_pid"
