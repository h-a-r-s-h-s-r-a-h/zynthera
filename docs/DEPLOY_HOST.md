# Preparing a VM host by hand

`scripts/install.sh` does all of this for a single server. This document explains each step,
for people who want to do it by hand or run VM hosts on separate machines.

Recommended OS: Ubuntu Server 24.04 LTS.

## 1. Host basics

```text
sudo infra/host-setup/setup-host.sh
```

The script is safe to run again. It:

| Step | What | Why |
|---|---|---|
| Packages | `qemu-system-x86`, `qemu-utils`, `libvirt-daemon-system`, `libvirt-clients`, `libvirt-daemon-config-nwfilter`, `cloud-image-utils`, `libguestfs-tools`, `nftables`, `zstd` | KVM, libvirt, and the tools the agent runs |
| Directories | `/var/lib/zynthera/{images,vms}` (mode 0711), `/etc/zynthera` (mode 0750) | QEMU runs as its own user and must reach disk files without being able to list them |
| Network | defines and starts the libvirt network `zynthera` (`infra/host-setup/network.xml`): bridge `zynbr0`, `10.210.0.0/16`, NAT | every VM gets a fixed private address |
| Firewall | `/etc/zynthera/firewall.nft`, loaded by `zynthera-firewall.service` | see below |
| Host shutdown | `ON_SHUTDOWN=suspend`, `ON_BOOT=ignore` in `/etc/default/libvirt-guests` | a host reboot suspends running VMs instead of killing them; they resume when their owners return |
| Saved images | `save_image_format = "zstd"` in `/etc/libvirt/qemu.conf` | smaller, faster suspend |

### The firewall

All rules are in one nftables table, `inet zynthera`, so they never touch libvirt's own NAT
rules. It is loaded by its own systemd unit and not from `/etc/nftables.conf`, because that
file starts with `flush ruleset` on Debian and Ubuntu and would delete libvirt's rules.

| Traffic | Rule |
|---|---|
| VM to the host | only DHCP and DNS to `10.210.0.1`; everything else dropped (the agent, SSH and all other services are unreachable) |
| VM to the internet | allowed, except TCP port 25 (no direct mail) |
| VM to private ranges | dropped: `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `169.254.0.0/16` (the provider's internal network, metadata services) |
| VM to VM | blocked by port isolation in each VM's network interface |
| IPv6 from VMs | dropped |
| Per-VM speed | capped in the VM definition (`ZYN_VM_BANDWIDTH_KBYTES`) |

## 2. The agent

Build the binaries (no Go needed on the host):

```text
docker build -f infra/docker/go.Dockerfile --target export --output type=local,dest=bin .
sudo install -m 0755 bin/zynthera-agent /usr/local/bin/
sudo install -m 0644 infra/systemd/zynthera-agent.service /etc/systemd/system/
```

Register the host in the admin area (or `POST /api/admin/hosts`). The agent token and the
stream secret are shown once. Put them in `/etc/zynthera/agent.env` (mode 0600):

```text
ZYN_ENV=production
ZYN_AGENT_ADDR=127.0.0.1:9090
ZYN_AGENT_HOST_ID=<id of the host>
ZYN_AGENT_TOKEN=<agent token>
ZYN_AGENT_STREAM_SECRET=<stream secret>
ZYN_AGENT_API_URL=https://app.example.com
ZYN_AGENT_WEB_ORIGIN=https://app.example.com
ZYN_VM_DRIVER=libvirt
ZYN_STORAGE_DRIVER=qcow2
ZYN_DATA_DIR=/var/lib/zynthera
ZYN_MAX_UPLOAD_MB=2048
```

```text
sudo systemctl enable --now zynthera-agent
```

The host shows as online in the admin area within ten seconds.

## 3. Caddy in front of the agent

For a separate VM host use `infra/caddy/host.Caddyfile`: replace `__ZYN_HOST_DOMAIN__` with
the host's name and `__ZYN_CONTROL_PLANE_IP__` with the control plane's address. Browsers
reach `/stream` and `/files/*`; every other path is limited to the control plane.

## 4. The OS image

```text
sudo ZYN_BRIDGE_BINARY=$PWD/bin/zynthera-file-bridge \
     ZYN_IMAGES_DIR=/var/lib/zynthera/images \
     images/build.sh ubuntu-24.04-xfce 2026.11.1
```

Needs Packer with the QEMU plugin, `xorriso` and KVM. It takes 20 to 30 minutes and
produces a compressed image of about 3.5 GB. The version must match
`os_images.current_version` in the database.

An image file is never changed or deleted while any PC was cloned from it: clones read from
it for their whole life. A rebuilt image is a new version.

## Things to know

- **AppArmor.** libvirt generates a profile per VM that allows its disk and the image it
  was cloned from. Images and disks outside `/var/lib/zynthera` work, but the directories
  above them must be reachable by the `libvirt-qemu` user.
- **Ownership.** libvirt changes the owner of a disk while its VM runs, and of an image once
  a VM cloned from it has run. The agent runs as root, so this does not matter.
- **Clock after resume.** The agent sets the guest clock through the guest agent. If the
  guest's agent cannot set the time (Ubuntu 24.04 without `util-linux-extra`), the agent
  sets it with `date` inside the guest instead.
- **One host, no replication.** A disk failure on the host loses the PCs on it. Use
  mirrored disks until backups exist (milestone 7).
