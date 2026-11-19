# Zynthera

**A personal Linux computer in a browser tab.**

Zynthera is a web platform where each user gets their own Linux PC. The PC is a real virtual machine that runs on a server and is used through the browser like a local computer: install software, download files, run applications. When the user logs out, the PC is frozen to disk and stops using server resources. When they log back in, it resumes exactly where they left it, with the same open windows, files and installed apps.

The operating system does not run inside the browser. It runs as a KVM virtual machine on a Linux server. The browser only shows the PC's screen and sends keyboard and mouse input.

---

## Table of contents

1. [What you can do with it](#what-you-can-do-with-it)
2. [How it works](#how-it-works)
3. [Tech stack](#tech-stack)
4. [Project status](#project-status)
5. [Requirements](#requirements)
6. [Local setup, step by step](#local-setup-step-by-step)
7. [Using the app](#using-the-app)
8. [Mock mode: running without virtual machines](#mock-mode-running-without-virtual-machines)
9. [Stopping and cleaning up](#stopping-and-cleaning-up)
10. [Commands](#commands)
11. [Configuration](#configuration)
12. [Repository layout](#repository-layout)
13. [API overview](#api-overview)
14. [How a PC's lifecycle works](#how-a-pcs-lifecycle-works)
15. [Security](#security)
16. [Testing](#testing)
17. [Deploying to a server](#deploying-to-a-server)
18. [Troubleshooting](#troubleshooting)
19. [Known limits](#known-limits)
20. [Documentation](#documentation)
21. [Author](#author)
22. [License](#license)

---

## What you can do with it

**As a user**

- Sign up with email and password, confirm the email, log in, reset a forgotten password.
- Create a PC in a four-step wizard: operating system, memory, CPU cores, disk size, Linux username and password.
- Use the PC's full desktop in the browser: keyboard, mouse, scaling to the window, fullscreen, Ctrl+Alt+Del, a clipboard panel.
- Have `sudo` inside the PC and install anything.
- Suspend the PC on demand. It is also suspended automatically on logout and after being idle.
- Resume it later and find it exactly as it was, including unsaved work in open windows.
- Start, restart, shut down, force stop and delete the PC from a dashboard.
- Take disk snapshots, restore them and delete them.
- Change the PC's memory, CPU count and disk size while it is shut down. A disk can grow but not shrink.
- Upload files from your own computer into the PC, and download files out of it.
- Keep the PC running for 1, 4, 8 or 12 hours after disconnecting, for long jobs.
- See the history of everything that happened to the PC.
- Delete your account, which deletes your PCs and data.

**As an administrator**

- See every server (host): online or offline, memory, CPU and disk in use, OS images present.
- Add a host, and drain or disable one.
- See every user's PC, filter by state, host or owner, and suspend or force stop any of them.
- See every account, set per-user limits (number of PCs, memory, CPUs, disk) and disable accounts.
- See how many hours each user's PCs ran, per PC size, for any date range, and export it as CSV. This is recorded so billing can be added later.

---

## How it works

```text
Browser
  |  HTTPS: REST + Server-Sent Events          |  WSS: desktop stream
  |                                            |  HTTPS: file upload / download
  v                                            v
+---------------------------+   HTTP(S)  +------------------------------+
|  zynthera-api             | ---------> |  zynthera-agent              |
|  control plane (Go)       | <--------- |  one per VM host (Go)        |
|  HTTP + job workers       | heartbeat  |  runs as a systemd service   |
+---------------------------+            +------------------------------+
     |              |                        |  virsh, qemu-img, cloud-localds
     v              v                        v
 PostgreSQL       Redis                 KVM virtual machines
 (all state)   (job queue, pub/sub)     each runs zynthera-file-bridge inside
```

There are four pieces.

| Piece | Folder | What it does |
|---|---|---|
| **Web app** | `web/` | The user interface: landing page, sign-up and login, dashboard, creation wizard, desktop viewer, settings, admin area. |
| **API** (control plane) | `cmd/api` | Owns all state in PostgreSQL. Authenticates users, decides which host runs a PC, queues and runs background jobs, and issues short-lived tokens for the desktop stream and for file transfer. |
| **Host agent** | `cmd/agent` | A small service on each VM host. It is the only component that touches libvirt. It creates, starts, suspends and deletes virtual machines, and relays the desktop stream and file transfers between the browser and the VM. |
| **Guest image** | `images/` | A prebuilt Ubuntu disk image that new PCs are cloned from, with a small file transfer service (`cmd/file-bridge`) built in. |

The API and the agent are separate on purpose. The API can run anywhere and can be redeployed without touching running PCs. The agent runs with high privileges on the host, so it is kept small and exposes a narrow interface.

### What happens when you create a PC

1. The browser sends `POST /api/vms`.
2. The API checks the sizes and the user's limits, picks a host with enough disk, saves the PC in state `provisioning` and queues a job.
3. A job worker calls the agent. The agent clones the OS image, reserves a fixed private IP address, writes a cloud-init seed (hostname, Linux user, password hash) and defines the virtual machine in libvirt.
4. A second job starts the virtual machine and marks it `running`.
5. The browser, listening on a live event stream, sees `running`, asks for a stream token and opens the desktop. The user can watch the first boot.

### What happens when you open the desktop

1. The page asks the API for a stream token. The API checks that the PC belongs to you and is running, and returns a signed token valid for 60 seconds, single use.
2. The browser opens a WebSocket to the agent with that token.
3. The agent verifies the token and copies bytes between the WebSocket and the virtual machine's VNC port. The VNC port only listens on the host's loopback address, so the agent is the only way in.

### What happens when you log out

1. The API queues a suspend job for each of your running PCs (unless keep-alive is on) and ends your session.
2. The agent runs `virsh managedsave`: the PC's memory is written to disk and its QEMU process exits. The memory is now free for other users.
3. When you come back, `virsh start` restores the saved memory. The PC continues from the exact instruction where it stopped, and its clock is corrected.

---

## Tech stack

| Part | Choice |
|---|---|
| Web | Next.js 16 (App Router), React 19, TypeScript, Tailwind CSS 4 |
| Web data layer | TanStack Query, zod |
| Desktop viewer | noVNC |
| API | Go, `chi` router, `pgx`, `sqlc` (typed SQL) |
| Migrations | `golang-migrate`, plain SQL files embedded in the binary |
| Job queue | `asynq` on Redis |
| Host agent | Go, running `virsh`, `qemu-img` and `cloud-localds` |
| Virtualization | KVM, QEMU, libvirt |
| Storage | qcow2 files (thin clones of a golden image) |
| Guest setup | cloud-init with a NoCloud seed |
| Image build | Packer with the QEMU plugin |
| Database | PostgreSQL 18 |
| Queue and pub/sub | Redis 8 |
| Reverse proxy in production | Caddy (automatic HTTPS) |
| Tests | Go `testing`, Vitest, Playwright |

---

## Project status

**Built and tested**

| Area | State |
|---|---|
| Accounts: sign-up, email confirmation, login, password reset, account deletion | Working |
| Creation wizard, dashboard, desktop viewer, PC settings | Working, tested in a real browser |
| Suspend and resume | Working on real KVM virtual machines |
| Snapshots, resize, file upload and download | Working on real KVM virtual machines |
| Admin area: hosts, PCs, users and limits, usage with CSV export | Working |
| Ubuntu 24.04 desktop image | Builds with one command |
| Production Docker images, Compose file, Caddy configuration | Tested locally |

**Written but not yet proven on a real server**

- `scripts/install.sh`, `scripts/doctor.sh` and `scripts/update.sh` have not been run from start to finish on a fresh server. Every part was tested separately.
- The host firewall rules load correctly, but isolation between PCs has not been tested against real virtual machines.
- A host reboot with suspended PCs has not been tested.

**Not built yet**

- Backups, metrics endpoints, a ZFS storage driver, a Debian image, a load test, a join script for a second host.
- Sound and smooth video in the PC (a WebRTC stream).
- Payments. Usage is recorded, nothing is charged.
- Windows guests and GPU support.

`docs/PROGRESS.md` has the full task checklist and what was verified. `docs/SECURITY_REVIEW.md` lists the open security points. **Do not open this to the public before the isolation checks in that document have been done on a real host.**

---

## Requirements

**To run real virtual machines** (the full product):

| Requirement | Notes |
|---|---|
| Linux, x86_64 | Developed on Linux Mint 22.2 (Ubuntu 24.04 base). Ubuntu 24.04 is the reference. |
| A CPU with hardware virtualization | Intel VT-x or AMD-V, enabled in the BIOS. `/dev/kvm` must exist. |
| RAM | 8 GB minimum, 16 GB recommended. Each running PC holds its full memory. |
| Disk | About 15 GB free for the tools, the image build and one PC. |
| Docker with Compose | For PostgreSQL, Redis, Mailpit and Adminer. |
| Go 1.27 or newer | For the API, the agent and the file bridge. |
| Node.js 24 | For the web app. |
| libvirt, QEMU, cloud-image-utils | For virtual machines. |
| Packer with the QEMU plugin, xorriso | For building the OS image. |

**To run in mock mode only** (no virtual machines, works on any OS that runs Docker): Docker, Go and Node.js. See [Mock mode](#mock-mode-running-without-virtual-machines).

---

## Local setup, step by step

These steps set up the full product with real virtual machines on a Linux machine. The first setup takes about 40 minutes, most of it building the OS image.

### Step 1. Check that your CPU can run virtual machines

```bash
grep -Ec '(vmx|svm)' /proc/cpuinfo    # must print a number above 0
ls -l /dev/kvm                        # must exist
```

If the first command prints `0`, enable virtualization in the BIOS. Without it, use [mock mode](#mock-mode-running-without-virtual-machines).

### Step 2. Install the system packages

```bash
sudo apt update
sudo apt install -y \
  qemu-system-x86 qemu-utils \
  libvirt-daemon-system libvirt-clients libvirt-daemon-config-nwfilter \
  cloud-image-utils libguestfs-tools virtinst \
  xorriso unzip curl git make
```

Allow your user to manage virtual machines without `sudo`, then **log out and back in** (or reboot) so the new groups take effect:

```bash
sudo usermod -aG libvirt,kvm $USER
```

After logging back in, this must work and print an (empty) table:

```bash
virsh -c qemu:///system list --all
```

> If you do not want to log out yet, put `sg libvirt -c "..."` around every command below that uses `virsh` or runs the agent, for example `sg libvirt -c "make dev"`.

### Step 3. Install Docker, Go, Node.js and Packer

**Docker.** Follow the official instructions for your distribution, or on Ubuntu:

```bash
sudo apt install -y docker.io docker-compose-v2
sudo usermod -aG docker $USER      # log out and back in afterwards
docker compose version             # must print a version
```

**Go 1.27 or newer.** Download from https://go.dev/dl/ and unpack it:

```bash
curl -fsSLO https://go.dev/dl/go1.27.4.linux-amd64.tar.gz
mkdir -p ~/.local && tar -C ~/.local -xzf go1.27.4.linux-amd64.tar.gz
ln -sf ~/.local/go/bin/go ~/.local/go/bin/gofmt ~/.local/bin/
go version
```

`~/.local/bin` must be on your `PATH`.

**Node.js 24.** With nvm (https://github.com/nvm-sh/nvm):

```bash
nvm install 24
node -v
```

**Packer and its QEMU plugin.** Download Packer from https://developer.hashicorp.com/packer/install, put the `packer` binary in `~/.local/bin`, then:

```bash
packer plugins install github.com/hashicorp/qemu
packer version
```

**Developer tools** (only needed to regenerate SQL code or run the linter):

```bash
GOBIN=$HOME/.local/bin go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
GOBIN=$HOME/.local/bin go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
```

### Step 4. Get the code

```bash
git clone https://github.com/h-a-r-s-h-s-r-a-h/zynthera.git
cd zynthera
cd web && npm install && cd ..
```

### Step 5. Create your settings file

```bash
cp .env.example .env
```

Open `.env` and set these two lines so the agent uses real virtual machines:

```ini
ZYN_VM_DRIVER=libvirt
ZYN_DATA_DIR=/var/tmp/zynthera-dev
```

`ZYN_DATA_DIR` holds the OS image and every PC's disk. It must **not** be inside your home folder: QEMU runs as its own system user and cannot read private home directories. `/var/tmp/zynthera-dev` works without `sudo`.

**If PostgreSQL or Redis already run on your machine** (ports 5432 or 6379 are taken), change the ports in `.env`. All four lines must agree:

```ini
POSTGRES_PORT=5433
REDIS_PORT=6380
ZYN_DATABASE_URL=postgres://zynthera:zynthera_dev@127.0.0.1:5433/zynthera?sslmode=disable
ZYN_REDIS_URL=redis://127.0.0.1:6380/0
```

### Step 6. Create the private network for the PCs

This is done once. It creates a libvirt network named `zynthera` (bridge `zynbr0`, addresses `10.210.0.0/16`, internet access through NAT).

```bash
mkdir -p /var/tmp/zynthera-dev && chmod 711 /var/tmp/zynthera-dev
virsh -c qemu:///system net-define infra/host-setup/network.xml
virsh -c qemu:///system net-start zynthera
```

### Step 7. Start the database, the queue and the mail catcher

```bash
make up
```

This starts four containers: PostgreSQL, Redis, Mailpit (catches every email the app sends) and Adminer (a database browser).

### Step 8. Create the tables and the first data

```bash
make migrate
make seed
```

`make seed` creates the administrator account, the list of operating systems, and registers your machine as the VM host named `local`. It is safe to run again.

### Step 9. Build the Ubuntu desktop image

```bash
ZYN_IMAGES_DIR=/var/tmp/zynthera-dev/images images/build.sh ubuntu-24.04-xfce 2026.11.2
```

This takes 20 to 30 minutes. It downloads Ubuntu's official cloud image, boots it once under KVM, installs the XFCE desktop, Firefox and the Zynthera file bridge, and seals it. The result is one compressed file of about 3.7 GB:

```text
/var/tmp/zynthera-dev/images/ubuntu-24.04-xfce/2026.11.2.qcow2
```

- The version (`2026.11.2`) must match what `make seed` wrote into the database. Use it exactly.
- If Ubuntu's main package archive is slow or unreachable from your network, put a mirror in front of the command, for example:
  `ZYN_APT_MIRROR=http://in.archive.ubuntu.com/ubuntu ZYN_IMAGES_DIR=... images/build.sh ...`
- You only do this once. Every PC is a thin clone of this file.

### Step 10. Start the app

```bash
make dev
```

This builds and runs the API, the agent and the web app together, with their logs prefixed `[api]`, `[agent]` and `[web]`. Press Ctrl+C to stop all three.

Open **http://localhost:3000**.

### Starting it again later

After a reboot, only three commands are needed. The image, the database and the settings are still there.

```bash
cd zynthera
virsh -c qemu:///system net-start zynthera
make up
make dev
```

To have the network start by itself after a reboot: `virsh -c qemu:///system net-autostart zynthera`.

---

## Using the app

### Addresses

| Address | What |
|---|---|
| http://localhost:3000 | The web app |
| http://localhost:3000/admin | The admin area (administrators only) |
| http://localhost:8025 | Mailpit: every email the app sends in development |
| http://localhost:8081 | Adminer: database browser (server `postgres`, user `zynthera`, password `zynthera_dev`, database `zynthera`) |
| http://127.0.0.1:8080/health | API health check |
| http://127.0.0.1:9090/health | Agent health check |

### The administrator account

Created by `make seed`:

| | |
|---|---|
| Email | `admin@zynthera.local` |
| Password | `dev-only-admin-password` |

These are the development defaults from `.env.example` (`ZYN_SEED_ADMIN_EMAIL`, `ZYN_SEED_ADMIN_PASSWORD`). The API refuses to start in production with this password.

To open the admin area, log in as the administrator and click **Admin** in the top bar.

### Creating your first PC

1. Open http://localhost:3000 and click **Create your PC** to sign up.
2. Open http://localhost:8025, find the confirmation email and click its link.
3. Log in. Click **Create your first PC**.
4. Choose Ubuntu 24.04, a size, a name for the PC, and a Linux username and password. The password is what `sudo` asks for inside the PC.
5. Click **Create PC**. The page shows progress, then the desktop. A fresh PC shows its desktop about 30 seconds after you click create.

### The desktop viewer

A small tab sits at the top centre of the desktop. Hover over it, or click it, to open the toolbar.

| Button | What it does |
|---|---|
| Fullscreen | Fills the screen. In fullscreen, keys such as Esc and Alt+Tab go to the PC (in browsers that support keyboard lock). |
| Ctrl+Alt+Del | Sends that key combination to the PC. |
| Clipboard | Text copied inside the PC appears here. To paste into the PC, put text here, click **Send to PC**, then paste inside the PC. |
| Files | Upload files (they land in the PC's `~/Uploads` folder) and browse the PC's home folder to download files. You can also drop files onto the desktop. |
| Suspend | Freezes the PC to disk and returns to the dashboard. |
| Shut down | Shuts the PC down cleanly and returns to the dashboard. |
| Your PCs | Back to the dashboard. The PC keeps running. |

If you open the same PC in a second tab, the second tab takes over and the first shows **Use here**.

---

## Mock mode: running without virtual machines

Mock mode runs the whole product (accounts, wizard, dashboard, viewer, files, snapshots, admin) against fake virtual machines. It needs no KVM, no libvirt and no OS image, and works on macOS and Windows too. Every fake PC shows the same shared XFCE desktop, served from a Docker container.

Use it for developing the web app or the API, and for the browser tests.

In `.env`:

```ini
ZYN_VM_DRIVER=mock
ZYN_DATA_DIR=./.data/agent
```

Then:

```bash
make up        # also builds and starts the mock desktop container
make migrate
make seed
make dev
```

Skip steps 1, 2, 6 and 9 of the local setup. To switch between modes, change the two lines in `.env`, run `make up` again and restart `make dev`.

---

## Stopping and cleaning up

| Goal | Command |
|---|---|
| Stop the app | Ctrl+C in the terminal running `make dev` |
| Stop the containers, keep the data | `make down` |
| Stop the containers and **delete the database** | `make nuke` |
| Stop the VM network | `virsh -c qemu:///system net-destroy zynthera` |
| Remove the VM network for good | `virsh -c qemu:///system net-undefine zynthera` |
| Delete the OS image and all PC disks | `rm -rf /var/tmp/zynthera-dev` |
| Delete Packer's download cache | `rm -rf ~/.cache/packer` |

After `make nuke`, run `make up`, `make migrate` and `make seed` again before `make dev`. After deleting the image, build it again (step 9) before anyone can create a PC; until then the wizard says no PCs can be created.

---

## Commands

Run `make help` for this list.

| Command | What it does |
|---|---|
| `make up` | Start PostgreSQL, Redis, Mailpit and Adminer (and the mock desktop in mock mode) |
| `make down` | Stop the containers |
| `make nuke` | Stop the containers and delete their data |
| `make migrate` | Apply all database migrations |
| `make migrate-new name=add_things` | Create a new pair of migration files |
| `make seed` | Create the admin user, the OS image list and the local host |
| `make generate` | Regenerate the typed SQL code with `sqlc` |
| `make dev` | Run the API, the agent and the web app together |
| `make test` | Go tests and web unit tests |
| `make test-e2e` | Playwright browser tests (mock mode) |
| `make lint` | `golangci-lint`, `eslint` and `prettier --check` |
| `make fmt` | Format Go and web code |
| `make build` | Build the three binaries into `bin/` |

The API binary has subcommands:

```bash
zynthera-api serve [--no-worker] [--no-scheduler]
zynthera-api migrate up | down [N] | version
zynthera-api seed
```

---

## Configuration

Every setting is an environment variable. `.env.example` documents all of them with a comment each. The most useful ones:

| Variable | Default | Meaning |
|---|---|---|
| `ZYN_ENV` | `development` | `development` or `production` |
| `ZYN_VM_DRIVER` | `mock` | `mock` (fake PCs) or `libvirt` (real KVM virtual machines) |
| `ZYN_DATA_DIR` | `./.data/agent` | Where the agent keeps OS images and PC disks |
| `ZYN_WEB_ORIGIN` | `http://localhost:3000` | Public address of the web app; used in emails and for origin checks |
| `ZYN_DATABASE_URL` | local PostgreSQL | Database connection |
| `ZYN_REDIS_URL` | local Redis | Queue, rate limits and live updates |
| `ZYN_SECRETS_KEY` | development key | 32 random bytes, base64. Encrypts host credentials in the database |
| `ZYN_ALLOWED_RAM_MB` | `2048,4096,8192,16384` | Memory sizes offered in the wizard |
| `ZYN_ALLOWED_VCPUS` | `1,2,4,8` | CPU counts offered |
| `ZYN_ALLOWED_DISK_GB` | `40,80,160` | Disk sizes offered |
| `ZYN_DEFAULT_MAX_VMS` | `1` | PCs per user (can be changed per user in the admin area) |
| `ZYN_DEFAULT_MAX_RAM_MB` | `16384` | Total memory per user |
| `ZYN_IDLE_SUSPEND_MINUTES` | `15` | Suspend a PC after this long with no viewer connected |
| `ZYN_IDLE_INPUT_MINUTES` | `60` | The viewer asks "Still there?" after this long without input |
| `ZYN_MAX_SNAPSHOTS_PER_VM` | `5` | Snapshots per PC |
| `ZYN_MAX_UPLOAD_MB` | `2048` | Largest single upload |
| `ZYN_SMTP_HOST`, `ZYN_SMTP_PORT`, `ZYN_SMTP_USER`, `ZYN_SMTP_PASS` | Mailpit | Outgoing email |

Secrets in `.env.example` contain the text `dev-only`. In production the API and the agent refuse to start while any secret still holds such a placeholder.

### Ports in development

All ports listen on `127.0.0.1` only.

| Service | Port |
|---|---|
| Web app | 3000 |
| API | 8080 |
| Agent | 9090 |
| PostgreSQL | 5432 |
| Redis | 6379 |
| Adminer | 8081 |
| Mailpit SMTP / web | 1025 / 8025 |
| Mock desktop VNC | 5901 |

---

## Repository layout

```text
/
├── README.md                    this file
├── LICENSE                      MIT license
├── PROJECT_SPEC.md              the full specification the project was built from
├── Makefile                     developer commands
├── .env.example                 every setting, with comments
├── go.mod
├── cmd/
│   ├── api/                     zynthera-api: serve, migrate, seed
│   ├── agent/                   zynthera-agent: runs on each VM host
│   └── file-bridge/             zynthera-file-bridge: runs inside each PC
├── internal/
│   ├── shared/                  used by more than one binary
│   │   ├── config/              reading settings from the environment
│   │   ├── httpx/               error format, request ids, logging, graceful shutdown
│   │   ├── logging/             structured JSON logs
│   │   ├── token/               signed tokens for the desktop stream and file transfer
│   │   └── vmtypes/             types exchanged between the API and the agent
│   ├── api/
│   │   ├── httpapi/             routes, middleware and handlers
│   │   ├── auth/                password hashing, tokens, rate limits
│   │   ├── mail/                emails and SMTP
│   │   ├── store/               database pool, migrations, generated SQL code
│   │   ├── vms/                 the PC state machine (pure rules)
│   │   ├── vmsvc/               the one place that changes a PC's state
│   │   ├── scheduler/           host choice and capacity arithmetic
│   │   ├── jobs/                background jobs and periodic tasks
│   │   ├── events/              live updates through Redis
│   │   ├── heartbeats/          the hosts' recent reports, for the reconciler
│   │   ├── agentclient/         HTTP client for the agent
│   │   ├── linuxpw/             SHA-512 crypt hashing of the Linux password
│   │   └── secrets/             AES-GCM encryption of host credentials
│   ├── agent/
│   │   ├── server/              the agent's HTTP API
│   │   ├── driver/              the Driver interface and its test suite
│   │   │   ├── libvirt/         real KVM virtual machines
│   │   │   └── mock/            fake virtual machines
│   │   ├── storage/qcow2/       PC disks as qcow2 files
│   │   ├── cloudinit/           first-boot configuration of a PC
│   │   ├── domainxml/           the libvirt definition of a PC
│   │   ├── netalloc/            MAC and IP address reservation
│   │   ├── stream/              WebSocket to VNC proxy
│   │   ├── files/               file transfer proxy
│   │   ├── heartbeat/           reports to the API
│   │   └── run/                 running host commands safely
│   └── bridge/                  the file transfer service inside a PC
├── db/
│   ├── migrations/              SQL migrations
│   ├── queries/                 SQL queries, compiled by sqlc
│   └── sqlc.yaml
├── web/                         the Next.js app
│   ├── src/app/                 pages
│   ├── src/components/          UI components
│   ├── src/lib/                 API client, validation, pure UI logic
│   └── e2e/                     Playwright browser tests
├── images/
│   ├── packer/                  the image template
│   ├── scripts/                 what gets installed into the image
│   ├── files/                   files copied into the image
│   └── build.sh                 builds one image
├── infra/
│   ├── docker-compose.yml       development services
│   ├── docker-compose.prod.yml  production control plane
│   ├── docker/                  production Dockerfiles
│   ├── dev-desktop/             the mock desktop container
│   ├── host-setup/              network, firewall and host preparation script
│   ├── systemd/                 service units
│   └── caddy/                   reverse proxy configurations
├── scripts/                     install, update, doctor and developer helpers
└── docs/                        progress, decisions, deployment, operations, security
```

---

## API overview

All routes are under `/api`. Requests and responses are JSON with `snake_case` keys. Authentication is a session cookie. Errors always have this shape:

```json
{ "error": { "code": "vm_invalid_state", "message": "This is not possible in the PC's current state.", "details": {} } }
```

| Group | Routes |
|---|---|
| Accounts | `POST /api/auth/signup`, `login`, `logout`, `verify-email`, `resend-verification`, `forgot-password`, `reset-password`, `delete-account`; `GET /api/auth/me` |
| Catalogue | `GET /api/images`, `GET /api/options` |
| Live updates | `GET /api/events` (Server-Sent Events with the live state of your PCs) |
| PCs | `GET /api/vms`, `POST /api/vms`, `GET`/`PATCH`/`DELETE /api/vms/:id` |
| PC actions | `POST /api/vms/:id/start`, `suspend`, `shutdown`, `force-stop`, `reboot`, `discard-saved-state` |
| Desktop and files | `POST /api/vms/:id/stream-token`, `POST /api/vms/:id/files-token` |
| History | `GET /api/vms/:id/events` |
| Snapshots | `GET`/`POST /api/vms/:id/snapshots`, `POST .../snapshots/:sid/restore`, `DELETE .../snapshots/:sid` |
| Admin | `GET`/`POST /api/admin/hosts`, `PATCH /api/admin/hosts/:id`, `GET /api/admin/vms`, `POST /api/admin/vms/:id/suspend`, `force-stop`, `GET /api/admin/users`, `PATCH /api/admin/users/:id`, `GET /api/admin/usage` |
| Internal (agents only) | `POST /internal/agent/heartbeat`, `POST /internal/agent/stream-event` |
| Health | `GET /health` |

Every PC object carries an `allowed_actions` list. The web app enables its buttons from that list and never re-implements the rules.

A PC that belongs to someone else answers `404`, exactly like a PC that does not exist, so ids cannot be probed.

---

## How a PC's lifecycle works

A PC is always in one state. Four are stable: `stopped`, `running`, `suspended`, `error`. The rest are transitional and mean a background job is working on it.

```text
  create
    |
    v
provisioning --> starting --> running <---------------+
                    ^           |  |  |               |
                    |   suspend |  |  | reboot        |
                    |           v  |  +--> rebooting -+
                    |      suspending
                    |           |  | shutdown / force stop
                    |           v  v
                    +----- suspended   stopping
                    |                     |
                    |                     v
                    +------------------ stopped

 stopped / suspended / running --(snapshot, restore, resize, discard)--> maintenance --> back to a stable state
 any stable state --(delete)--> deleting --> deleted
 any job that gives up --> error
```

How this is kept correct:

- **The transitional state is the lock.** A request moves a PC out of a stable state with one atomic database update. If two requests arrive together, exactly one wins and the other is told the PC is busy.
- **Capacity is checked under a lock.** Starting a PC locks its host's row first, so two starts can never both take the last free memory. Memory is never overcommitted.
- **Jobs can run twice safely.** A job first checks that the PC is still in the state it expects, and otherwise does nothing.
- **Failures are readable.** A job that runs out of retries puts the PC in `error` with a message a person can understand and offers recovery actions: start, force stop, discard the saved session, or delete.
- **The record follows reality.** Each host reports its PCs every ten seconds. A periodic task compares those reports with the database and corrects differences, but only after two reports in a row agree. For example, shutting a PC down from inside it shows up as "Shut down from inside the PC".
- **Usage is recorded.** An interval is opened when a PC starts running and closed when it stops, with the PC's size at that time.

Periodic tasks: suspend idle PCs (every minute), reconcile with the hosts (every 30 seconds), rescue PCs stuck in a transitional state (every 2 minutes), finish account deletions (every minute), clean up expired sessions (every hour).

---

## Security

The platform gives strangers root on a virtual machine, so this is treated as a requirement, not a feature.

- **No shell injection.** Host commands are always an argument list; nothing passes through a shell. Names on disk and in libvirt are identifiers generated by the system, never text typed by a user.
- **Structured rendering.** cloud-init YAML and libvirt XML are produced by serializers, never by pasting values into text.
- **Nothing in a PC is reachable from outside the host.** The display listens on the host's loopback address only. The file service is reached only through the agent, with a signed, short-lived, purpose-bound token.
- **Isolation between PCs.** PCs cannot talk to each other (port isolation) and cannot fake another PC's address (libvirt's `clean-traffic` filter). The host firewall blocks PCs from the host itself, from private network ranges and from sending mail directly.
- **Passwords.** Account passwords are hashed with argon2id. The Linux password is hashed in the request handler, the plain text is discarded immediately, and the hash is removed from the database once the PC is set up.
- **Sessions.** `HttpOnly`, `SameSite=Lax` cookies (`Secure` in production). Only a SHA-256 hash of the cookie is stored. Requests that change something must come from the web app's own origin. A password reset ends every session.
- **Rate limits** on sign-up, login, password reset, tokens and PC actions.
- **Secrets.** Host credentials in the database are encrypted. Logs never contain passwords, tokens or cookies.
- **Deletion is real.** Deleting a PC removes its disk, saved memory, snapshots, seed and network reservation from the host.

`docs/SECURITY_REVIEW.md` goes through each requirement, says how it was verified, and lists what is still open.

---

## Testing

| Layer | What | Command |
|---|---|---|
| Go unit and integration tests | State machine, scheduler, tokens, path safety, rendering, the whole API against a real database and Redis with real job workers | `make test` |
| Web unit tests | Validation and UI state logic | `make test` |
| Browser tests | The complete user journeys in a real browser | `make test-e2e` |
| Driver tests on real KVM | The virtual machine driver against real virtual machines | see below |

`make test` needs the containers from `make up`. The Go integration tests use a separate database (`zynthera_test`) and a separate Redis database, so they never touch development data.

`make test-e2e` needs mock mode and `make dev` running (or it starts it).

To run the driver tests against real virtual machines:

```bash
ZYN_TEST_LIBVIRT=1 \
ZYN_TEST_LIBVIRT_IMAGE=/var/tmp/zynthera-dev/images/ubuntu-24.04-xfce/2026.11.2.qcow2 \
go test -timeout 1500s ./internal/agent/driver/libvirt
```

---

## Deploying to a server

One command installs everything on a fresh Ubuntu Server 24.04 machine: KVM and libvirt, the firewall, Docker, the database, the API, the web app, the agent, HTTPS with an automatic certificate, and the OS image.

```bash
sudo scripts/install.sh --domain pc.example.com --email you@example.com \
     --smtp-host smtp.example.com --smtp-user USER --smtp-pass PASSWORD
```

You need a server with hardware virtualization (bare metal, or a cloud VM with nested virtualization), a domain pointing at it, ports 80 and 443 open, and an SMTP account.

```bash
sudo scripts/doctor.sh     # checks every part and prints the fix for anything wrong
sudo scripts/update.sh     # after "git pull": updates without touching running PCs
```

Read `docs/DEPLOY_QUICKSTART.md` first. Note the status above: the installer has not yet been run from start to finish on a fresh server.

---

## Troubleshooting

| Problem | Cause and fix |
|---|---|
| `make up` fails with "port is already allocated" | PostgreSQL or Redis already runs on your machine. Change the ports in `.env` (step 5). |
| `virsh` says "Permission denied" on `libvirt-sock` | Your user is not in the `libvirt` group yet in this session. Log out and back in, or wrap the command in `sg libvirt -c "..."`. |
| `virsh net-start zynthera` says the network is not found | You forgot `-c qemu:///system`. Without it `virsh` looks at your personal session, not the system. |
| The wizard says "No PCs can be created right now" | The host is offline or has no OS image. Check that `make dev` is running, and that the image file exists with the version the database expects (step 9). The admin area's Hosts tab shows which images the host has. |
| Sign-up or login returns an error right after a fresh start | The tables do not exist yet. Run `make migrate` and `make seed`. |
| A PC fails to start with "permission denied" in the agent log | `ZYN_DATA_DIR` is inside a private folder. Use a path QEMU can reach, such as `/var/tmp/zynthera-dev`. |
| The image build hangs at `apt-get update` | Ubuntu's main archive is unreachable from your network. Set `ZYN_APT_MIRROR` (step 9). |
| The landing page says "API is not reachable" | The API is not running, or failed to start. Look at the `[api]` lines of `make dev`; a missing or invalid setting is listed there. |
| No confirmation email arrives | In development emails go to Mailpit, not to a real inbox. Open http://localhost:8025. |
| "Too many attempts" during testing | A rate limit. Wait, or clear them: `docker compose --env-file .env -f infra/docker-compose.yml exec redis redis-cli FLUSHDB` (this also clears queued jobs). |
| The desktop shows "Could not reconnect" | The browser cannot reach the agent on port 9090. Check that `make dev` is still running. |

---

## Known limits

- **No GPU.** 3D games, video editing and model training run on the CPU and are slow.
- **No sound, choppy video.** The stream is fine for desktop work, browsing and coding.
- **One host.** A PC can only run on the host that holds its disk. There is no shared storage and no live migration.
- **No backups yet.** With one host and no replication, a disk failure on the host loses users' PCs. Use mirrored disks.
- **Linux guests only.**
- **Every running PC holds its full memory.** A 64 GB server runs about 14 PCs of 4 GB at the same time.

---

## Documentation

| File | Contents |
|---|---|
| `PROJECT_SPEC.md` | The complete specification: architecture, schema, API, state machine, security requirements, acceptance test |
| `docs/PROGRESS.md` | The task checklist, what was verified and how, measured timings |
| `docs/DECISIONS.md` | Every choice that departs from the specification, with the reason |
| `docs/DEPLOY_QUICKSTART.md` | Deploying to one server |
| `docs/DEPLOY_HOST.md` | Preparing a VM host by hand, the firewall explained |
| `docs/DEPLOY_CONTROL_PLANE.md` | The API, web app, database and routing in production |
| `docs/OPERATIONS.md` | Everyday commands, rebooting a host, recovering a PC |
| `docs/SECURITY_REVIEW.md` | The security requirements, how each is met, and open points |

---

## Author

**Harsh**

- GitHub: [h-a-r-s-h-s-r-a-h](https://github.com/h-a-r-s-h-s-r-a-h)
- LinkedIn: [harsh-55a0b325b](https://www.linkedin.com/in/harsh-55a0b325b)

---

## License

Released under the [MIT License](LICENSE). Copyright (c) 2026 Harsh.
