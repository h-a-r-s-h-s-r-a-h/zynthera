# Operations

## Everyday commands

| Task | Command |
|---|---|
| Check everything | `sudo scripts/doctor.sh` |
| Update to the checked-out code | `git pull && sudo scripts/update.sh` |
| API log | `docker compose --env-file /etc/zynthera/control-plane.env -f infra/docker-compose.prod.yml logs -f api` |
| Agent log | `journalctl -u zynthera-agent -f` |
| List VMs on the host | `virsh list --all` (names are `zyn-<vm id>`) |
| Reload the VM firewall | `systemctl reload zynthera-firewall` |

## Rebooting the host

Just reboot. `libvirt-guests` suspends every running PC to disk during shutdown and starts
none of them on boot. Afterwards the control plane sees those PCs as suspended, and each one
resumes when its owner opens it.

## A PC in "Needs attention"

The message on the PC says what happened. The owner can try to start it, force stop it, or
discard its saved session. From the host:

```text
virsh domstate zyn-<vm id>
virsh dominfo zyn-<vm id>          # "Managed save: yes" means a saved session exists
journalctl -u zynthera-agent | grep <vm id>
```

If a resume fails, the saved session is kept on purpose. Discarding it (`virsh
managedsave-remove zyn-<vm id>`, or the button in the web app) loses open windows and
unsaved work, never files.

## Removing the VM network

```text
virsh net-destroy zynthera
virsh net-undefine zynthera
```

## Not written yet

Backups (nightly `pg_dump`, VM disk backups to S3-compatible storage, tested restore),
adding a second host, and metrics arrive with milestone 7.
