# The desktop shown by every mock VM. Development only: the VNC server has no
# password, so the port must stay bound to 127.0.0.1 (see docker-compose.yml).
FROM debian:trixie-slim

RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      tigervnc-standalone-server \
      xfce4 xfce4-terminal dbus-x11 \
      fonts-dejavu-core adwaita-icon-theme \
 && rm -rf /var/lib/apt/lists/*

RUN useradd --create-home --shell /bin/bash zyn
USER zyn
WORKDIR /home/zyn

COPY --chown=zyn:zyn start.sh /home/zyn/start.sh

EXPOSE 5901
CMD ["/bin/sh", "/home/zyn/start.sh"]
