# Deploy Zynthera on one server

One server runs everything: the website, the API, the database and the virtual machines.

## What you need

| Thing | Detail |
|---|---|
| A server | Ubuntu Server 24.04 LTS, with hardware virtualization: a bare-metal (dedicated) server, or a cloud VM that offers nested virtualization. Most ordinary cloud VMs do not. |
| Size | Every running PC holds its full RAM. A 64 GB server runs about 14 PCs of 4 GB at once (4 GB stays reserved for the server itself). Disk: each PC needs its disk size plus its RAM size. |
| A domain | For example `pc.example.com`, with a DNS A record pointing at the server. |
| Open ports | 80 and 443 from the internet. Nothing else needs to be open. |
| A mail service | Any SMTP provider. Without it nobody can confirm their email, so nobody but the admin can create a PC. |

## Install

```text
git clone <your repository> zynthera
cd zynthera
sudo scripts/install.sh --domain pc.example.com --email you@example.com \
     --smtp-host smtp.example.com --smtp-user USER --smtp-pass PASSWORD
```

It takes about 30 to 40 minutes, most of it building the Ubuntu desktop image. At the end it
prints the address, the admin email and the admin password (shown once).

What the script does, in order:

1. Installs KVM and libvirt, creates the private VM network and loads the firewall that
   isolates PCs from each other, from the server and from private networks.
2. Installs Docker and Caddy.
3. Generates every secret and writes `/etc/zynthera/control-plane.env` and
   `/etc/zynthera/agent.env` (both readable by root only).
4. Builds the API, the web app, the host agent and the file bridge.
5. Starts Postgres, Redis, the API and the web app, applies database migrations, creates
   the admin account and registers this server as a VM host.
6. Starts the host agent as a systemd service.
7. Configures Caddy for your domain. Caddy fetches the HTTPS certificate by itself.
8. Builds the Ubuntu 24.04 desktop image.

The script can be run again at any time. It keeps existing secrets, data and images.

## Check

```text
sudo scripts/doctor.sh
```

It tests every part and, for anything wrong, prints the command that fixes it.

## Update

```text
git pull
sudo scripts/update.sh
```

Running PCs are not affected. People with an open desktop see a short reconnect.

## Where things are

| Path | Contents |
|---|---|
| `/etc/zynthera/control-plane.env` | Settings and secrets of the API. **Back this file up.** |
| `/etc/zynthera/agent.env` | Settings and secrets of the host agent |
| `/var/lib/zynthera/images` | OS images |
| `/var/lib/zynthera/vms` | Each PC's disk |
| `/var/lib/libvirt/qemu/save` | Saved memory of suspended PCs |
| Docker volume `zynthera_pgdata` | The database |
| `journalctl -u zynthera-agent` | Agent log |
| `docker compose -f infra/docker-compose.prod.yml logs api` | API log |

## Status of this guide

Tested: the production Docker images and Compose file (started locally, migrations, admin
seed, secure cookies, refusal of placeholder secrets), both Caddy configurations (validated
with Caddy 2.6 and 2.11), the firewall rules (loaded in an isolated network namespace), the
OS image build, and the complete product on real KVM virtual machines.

Not yet run from start to finish: `install.sh`, `doctor.sh` and `update.sh` as root on a
fresh server. They are syntax-checked and pass shellcheck, and every command in them was
tried by hand, but the first real run may still need small fixes.
