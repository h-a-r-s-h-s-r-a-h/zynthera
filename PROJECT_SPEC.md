# Zynthera V1: Build Specification and Implementation Plan

Zynthera is a web platform where each user gets a personal Linux computer that runs on our servers and is used through the browser. This document is the complete brief for building version 1.

You (Claude Code) are the engineer building it. Sections 1 to 17 are the specification: what to build and why each decision was made. Section 18 is the implementation plan: every task in order, grouped into milestones, each with a check for when it is done. Section 19 is the final acceptance test.

---

## 0. Start here

1. Read this whole file once before writing any code.
2. Run the environment check in section 2. Tell the user what you found and which mode (real KVM or mock) you will build in.
3. Create `docs/PROGRESS.md` by copying the task checklist from section 18. Tick tasks off there as you finish them. That file is how you pick the work up again in a later session.
4. Start milestone 0 and work through the milestones in order. Later milestones depend on earlier ones.
5. At the end of each milestone: run the tests, update `docs/PROGRESS.md`, commit, and give the user a short summary of what works now and how to try it. Then continue to the next milestone unless you are blocked on something only the user can provide.

Questions you may ask the user up front. None of them block milestones 0 to 4, so use the default if there is no answer.

| Question | Default if unanswered |
|---|---|
| Is there a Linux server with KVM available for real VMs? | No. Build in mock mode. |
| Is there a Git remote? | Local repository only. Go module name `zynthera`. |
| What domain will production use? | None yet. Everything runs on `localhost`. |

---

## 1. What Zynthera is

A user signs up, picks an operating system, RAM, CPU and disk size, and gets a real virtual machine with a full desktop. They use it in a browser tab like a local computer: install software, download files, run applications. When they log out, the machine is suspended: its memory is written to disk and it stops using server resources. When they log back in, it resumes exactly where they left it, with the same open windows, files and installed apps.

The OS does not run inside the browser. It runs as a KVM virtual machine on a Linux server. The browser shows the VM's screen and sends keyboard and mouse input.

### 1.1 In scope for V1

- Email and password sign-up, login, email verification, password reset
- Creation wizard: OS image, RAM, vCPU count, disk size, Linux username and password
- Full desktop in the browser: keyboard, mouse, scaling, fullscreen
- Users have sudo inside their own VM and can install anything
- Suspend on logout, on idle timeout and on demand; resume on login
- Start, restart, shut down, force stop and delete from a dashboard
- A persistent disk per VM that survives suspend, shutdown and host reboot
- Disk snapshots: create, list, restore, delete
- File upload from the user's computer into the VM, and download out of it
- Admin area: hosts, capacity, all VMs, suspend or stop any VM, usage totals
- Usage tracking (how long each VM ran at which size), stored so billing can be added later

### 1.2 Out of scope for V1

Do not build these, but do not make them impossible.

- GPU of any kind.
- Windows guests. Licensing is unresolved. The display comes from QEMU itself and needs nothing installed in the guest, which keeps Windows possible later.
- Payments. Record usage only.
- Shared storage across hosts. V1 targets one host. The control plane still models hosts as a list so a second host can be added.
- Audio and high frame rate video. These arrive with the WebRTC upgrade in milestone 8, which is optional.

### 1.3 Known limits of V1 (tell the user if they ask)

- Without a GPU, 3D games, video editing and model training run on the CPU and are slow.
- The VNC stream is fine for desktop work, browsing and coding. Video playback inside the VM looks choppy and there is no sound.
- A VM can only resume on the host that holds its disk.
- With one host and no replication, a disk failure on the host loses user data. Milestone 7 adds a backup procedure. Until then, the host should at least use mirrored disks.

---

## 2. Environment check and build modes

The real VM driver needs Linux with KVM. Check these first and report the results:

- OS and architecture: `uname -a`
- KVM: does `/dev/kvm` exist and is it usable (`kvm-ok` or `virt-host-validate` if installed)
- Docker and Docker Compose: installed and running
- Go and Node.js: installed, and which versions
- Free RAM, CPU cores and disk space

Then choose the mode.

**KVM mode** (Linux with a working `/dev/kvm`): build and test against the real `libvirt` driver from milestone 5 onward. Milestones 0 to 4 still use the mock driver because it is faster to iterate with.

**Mock mode** (macOS, Windows, WSL without nested virtualization, or a cloud VM without it): build everything against the mock driver described in section 11.4. The whole product flow works in mock mode, including a real desktop in the browser, served from a Docker container. Write the `libvirt` driver, the host setup scripts and the image templates anyway, mark them "written, not tested on real hardware" in `docs/PROGRESS.md`, and write `docs/DEPLOY_HOST.md` so the user can run them on a KVM server later.

If something about the environment is unclear and it changes what you would build, ask the user instead of guessing.

---

## 3. Architecture

```text
Browser
  |  HTTPS: REST + Server-Sent Events          |  WSS: desktop stream
  |                                            |  HTTPS: file upload / download
  v                                            v
+---------------------------+   HTTPS    +------------------------------+
|  zynthera-api             | ---------> |  zynthera-agent              |
|  control plane (Go)       | <--------- |  one per VM host (Go)        |
|  HTTP + job workers       | heartbeat  |  runs as a systemd service   |
+---------------------------+            +------------------------------+
     |              |                        |  virsh, qemu-img, zfs
     v              v                        v
 PostgreSQL       Redis                 KVM virtual machines
 (all state)   (job queue, pub/sub)     each runs zynthera-file-bridge inside
```

Four pieces:

1. **Web app** (`web/`): the UI. Landing page, auth, dashboard, creation wizard, desktop viewer, admin.
2. **API** (`cmd/api`): the control plane. Owns all state in Postgres, authenticates users, decides which host runs a VM, queues and runs jobs, issues short-lived tokens for the stream and for file transfer.
3. **Host agent** (`cmd/agent`): a small service on each VM host. It is the only thing that touches libvirt. It creates, starts, suspends and deletes VMs, and proxies the desktop stream and file transfers between the browser and the VM.
4. **Guest image** (`images/`): prebuilt OS disk images that new VMs are cloned from, with a small file transfer service (`cmd/file-bridge`) baked in.

Why the API and the agent are separate: the API can run anywhere and can be redeployed without touching running VMs. The agent runs with high privileges on the host, so it is kept small and exposes a narrow interface.

### 3.1 Main flows

**Creating a PC**

1. Browser sends `POST /api/vms`.
2. API validates sizes and limits, picks a host, inserts the VM row in state `provisioning`, queues `vm:provision`.
3. A worker calls the agent. The agent clones the image, reserves an IP address, writes the cloud-init seed and defines the libvirt domain.
4. The worker moves the VM to `starting` and queues `vm:start`.
5. The agent starts the domain. The worker marks it `running`.
6. The browser, listening on the event stream, sees `running`, asks for a stream token and opens the desktop. The user can watch the first boot happen.

**Opening the desktop**

1. Browser opens `/vms/:id/desktop`. If the VM is `stopped` or `suspended`, the page calls `POST /api/vms/:id/start` and shows progress.
2. Once `running`, the browser calls `POST /api/vms/:id/stream-token`.
3. The API checks ownership and state and returns a signed token valid for 60 seconds, plus the agent's WebSocket URL.
4. The browser opens the WebSocket to the agent. The agent verifies the token and pipes bytes between the WebSocket and the VM's VNC port.

**Logging out**

1. Browser sends `POST /api/auth/logout`.
2. API queues `vm:suspend` for each of the user's running VMs that does not have keep-alive active, then ends the session.
3. A worker calls the agent, which runs `virsh managedsave`. The QEMU process exits and the VM's RAM is free for other users.

---

## 4. Tech stack

| Part | Choice | Reason |
|---|---|---|
| Web | Next.js (App Router), TypeScript, Tailwind CSS | Standard and fast to build |
| Web data layer | TanStack Query, zod for validation | Simple server-state handling |
| Desktop viewer | noVNC (`@novnc/novnc`) | Speaks VNC over WebSocket and works with QEMU's built-in VNC server |
| API | Go, `chi` router, `pgx`, `sqlc` | Single binary, good fit for infrastructure code, typed SQL |
| Migrations | `golang-migrate` with plain SQL files | Easy to review |
| Job queue | `asynq` on Redis | Retries, timeouts, scheduled tasks |
| Host agent | Go, shelling out to `virsh`, `qemu-img`, `cloud-localds`, `zfs` | Avoids cgo libvirt bindings; every action can be reproduced by hand |
| Virtualization | KVM, QEMU, libvirt | Proven, and supports saving a VM's memory to disk |
| Storage | qcow2 files first, ZFS volumes second, behind one interface | qcow2 works on any Linux box; ZFS is better in production |
| Guest setup | cloud-init with a NoCloud seed ISO | Standard way to set the user, password and hostname at first boot |
| Image build | Packer with the QEMU plugin | Repeatable golden images |
| Database | PostgreSQL in Docker | Section 7 |
| Queue backend | Redis in Docker | Section 7 |
| Reverse proxy (production) | Caddy | Automatic TLS certificates |

Use the current stable version of each tool and library. Do not pin versions from memory. Check the package registry or the official documentation when you add a dependency, then pin what you chose in `go.mod`, `package.json` and the Compose file.

---

## 5. Conventions

### 5.1 Names

| Thing | Value |
|---|---|
| Product name | Zynthera |
| Binaries | `zynthera-api`, `zynthera-agent`, `zynthera-file-bridge` |
| Environment variable prefix | `ZYN_` |
| Database name | `zynthera` (tests use `zynthera_test`) |
| Session cookie | `zyn_session` |
| libvirt domain name | `zyn-<vm uuid>` |
| libvirt network / bridge | `zynthera` / `zynbr0` |
| VM subnet | `10.210.0.0/16`, gateway `10.210.0.1` |
| Guest hostname | `zynthera-<first 6 hex chars of the VM id>` |

User-supplied text never appears in a file path, a libvirt name, a snapshot name on disk or a shell argument. Those always use UUIDs generated by us.

### 5.2 Paths on a VM host

| Path | Contents |
|---|---|
| `/etc/zynthera/agent.env` | Agent configuration and secrets, mode 0600 |
| `/var/lib/zynthera/images/<slug>/<version>.qcow2` | Golden images. Immutable once a VM has been cloned from them |
| `/var/lib/zynthera/vms/<vm id>/disk.qcow2` | The VM's disk |
| `/var/lib/zynthera/vms/<vm id>/seed.iso` | cloud-init seed |
| `/var/lib/zynthera/vms/<vm id>/meta.json` | What the agent needs to remember: MAC, IP, file bridge token, image, sizes. Mode 0600 |
| `/var/lib/libvirt/qemu/save/` | Saved memory images written by `virsh managedsave` (libvirt's default location) |

### 5.3 Ports in development

| Service | Port |
|---|---|
| Web (Next.js) | 3000 |
| API | 8080 |
| Agent | 9090 |
| PostgreSQL | 5432 |
| Redis | 6379 |
| Adminer | 8081 |
| Mailpit SMTP / web UI | 1025 / 8025 |
| Mock desktop VNC | 5901 |

All development ports bind to `127.0.0.1` only.

### 5.4 Code conventions

- Go: standard layout, `log/slog` with JSON output, errors wrapped with context, `context.Context` on every call that does I/O, no global state apart from configuration loaded at startup.
- Configuration comes from environment variables, is parsed into a typed struct at startup and validated. A missing or invalid required value stops the process with a clear message.
- TypeScript: strict mode, no `any`, API types defined once in `web/src/lib/api/types.ts` and kept in step with the Go response structs.
- JSON uses `snake_case` keys. Timestamps are RFC 3339 in UTC.
- Prefer boring, readable code. Someone with modest experience will maintain this.

---

## 6. Repository layout

One Go module holds all Go code, so the API, the agent and the file bridge can share types and the token package.

```text
/
├── PROJECT_SPEC.md              this file
├── README.md                    quick start
├── Makefile
├── .env.example
├── go.mod
├── cmd/
│   ├── api/                     zynthera-api: serve, migrate, seed subcommands
│   ├── agent/                   zynthera-agent
│   └── file-bridge/             zynthera-file-bridge (runs inside guests)
├── internal/
│   ├── shared/                  used by more than one binary
│   │   ├── config/              env loading helpers
│   │   ├── logging/
│   │   ├── token/               signed stream and file tokens
│   │   └── vmtypes/             VMSpec, VMStatus, Capacity, heartbeat payloads
│   ├── api/
│   │   ├── httpapi/             router, middleware, handlers, error envelope
│   │   ├── auth/                passwords, sessions, email tokens, rate limits
│   │   ├── mail/
│   │   ├── store/               sqlc-generated code and thin wrappers
│   │   ├── vms/                 state machine, validation, allowed actions
│   │   ├── scheduler/           host choice and capacity checks
│   │   ├── jobs/                asynq tasks, handlers, periodic tasks
│   │   ├── agentclient/         HTTP client for the agent API
│   │   ├── events/              Redis pub/sub and the SSE endpoint
│   │   └── secrets/             AES-GCM encryption for host credentials
│   ├── agent/
│   │   ├── server/              agent HTTP API
│   │   ├── driver/              Driver interface, conformance tests
│   │   │   ├── libvirt/
│   │   │   └── mock/
│   │   ├── storage/             Storage interface
│   │   │   ├── qcow2/
│   │   │   └── zfs/
│   │   ├── cloudinit/           seed rendering and ISO creation
│   │   ├── domainxml/           libvirt XML builder with golden-file tests
│   │   ├── netalloc/            MAC and IP reservation
│   │   ├── stream/              WebSocket to VNC proxy
│   │   ├── files/               proxy to the guest file bridge
│   │   └── heartbeat/
│   └── bridge/                  file bridge implementation
├── db/
│   ├── migrations/              golang-migrate SQL files
│   ├── queries/                 sqlc query files
│   └── sqlc.yaml
├── web/                         Next.js app
├── images/
│   ├── packer/                  one template per OS image
│   ├── scripts/                 provisioning and cleanup scripts run inside the image
│   └── build.sh
├── infra/
│   ├── docker-compose.yml
│   ├── dev-desktop/             Dockerfile for the mock desktop
│   ├── host-setup/              scripts and XML for preparing a VM host
│   ├── systemd/                 zynthera-agent.service
│   ├── caddy/                   Caddyfiles for the control plane and for a host
│   └── docker/                  production Dockerfiles for api and web
├── scripts/                     acceptance test, load test, dev helpers
└── docs/
    ├── PROGRESS.md              task checklist and current status
    ├── DECISIONS.md             choices that depart from this spec, with reasons
    ├── DEPLOY_HOST.md           preparing a real VM host
    ├── DEPLOY_CONTROL_PLANE.md  running the API and web app in production
    └── OPERATIONS.md            backups, adding a host, recovering a broken VM
```

---

## 7. Local development setup

Postgres, Redis and the other supporting services run through Docker Compose so nobody installs them by hand. The API, the agent and the web app run directly on the machine during development so reloads are fast.

After `git clone`, the path to a working environment must be exactly:

```text
cp .env.example .env
make up
make migrate
make seed
make dev
```

### 7.1 Docker Compose

Create `infra/docker-compose.yml` along these lines. Replace `<pin>` with the current stable version you chose.

```yaml
name: zynthera

services:
  postgres:
    image: postgres:<pin>
    environment:
      POSTGRES_USER: ${POSTGRES_USER}
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}
      POSTGRES_DB: ${POSTGRES_DB}
    ports:
      - "127.0.0.1:${POSTGRES_PORT:-5432}:5432"
    volumes:
      # Check the image documentation for the data path of the version you pin.
      - pgdata:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U ${POSTGRES_USER} -d ${POSTGRES_DB}"]
      interval: 5s
      timeout: 3s
      retries: 10

  redis:
    image: redis:<pin>
    command: ["redis-server", "--appendonly", "yes"]
    ports:
      - "127.0.0.1:${REDIS_PORT:-6379}:6379"
    volumes:
      - redisdata:/data
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 5s
      timeout: 3s
      retries: 10

  adminer:
    image: adminer:<pin>
    ports:
      - "127.0.0.1:8081:8080"
    depends_on:
      postgres:
        condition: service_healthy

  mailpit:
    image: axllent/mailpit:<pin>
    ports:
      - "127.0.0.1:1025:1025"
      - "127.0.0.1:8025:8025"

  dev-desktop:
    profiles: ["mock"]
    build: ./dev-desktop
    ports:
      - "127.0.0.1:5901:5901"

volumes:
  pgdata:
  redisdata:
```

Notes:

- Redis runs with append-only persistence so queued jobs survive a restart.
- `make up` also creates the `zynthera_test` database (an init script mounted into the Postgres container is the simplest way).
- `dev-desktop` is built from `infra/dev-desktop/Dockerfile`: a Debian base with XFCE and a VNC server (TigerVNC's `Xvnc` is the simplest, since it is an X server and a VNC server in one process) listening on 5901 with no password. It exists only so the desktop viewer has something real to connect to in mock mode.
- The agent is never containerized. On a real host it needs direct access to libvirt and runs under systemd.

### 7.2 Makefile targets

| Target | What it does |
|---|---|
| `make up` | Start Compose services. Includes the `mock` profile when `ZYN_VM_DRIVER=mock` |
| `make down` | Stop them. `make nuke` also removes the volumes |
| `make migrate` | Apply all migrations (`zynthera-api migrate up`) |
| `make migrate-new name=...` | Create a new pair of migration files |
| `make seed` | Create the admin user, the OS image rows and, in development, the local host row |
| `make generate` | Run `sqlc generate` |
| `make dev` | Run API, agent and web together with prefixed logs |
| `make test` | Go tests and web unit tests |
| `make test-e2e` | Playwright tests against mock mode |
| `make lint` | `golangci-lint`, `eslint`, `prettier --check` |
| `make build` | Build the three binaries into `bin/` |

### 7.3 One API process in V1

`zynthera-api serve` runs the HTTP server, the job workers and the periodic task scheduler in one process. Add flags (`--no-worker`, `--no-scheduler`) so they can be split later. Only one instance may run the scheduler, because two would queue every periodic task twice. V1 runs one instance.

### 7.4 Development seed

`make seed` must leave a fresh checkout ready to use with no manual steps:

- Creates the admin user from `ZYN_SEED_ADMIN_EMAIL` and `ZYN_SEED_ADMIN_PASSWORD`, already verified.
- Inserts the OS image rows from section 13.
- When `ZYN_ENV=development`, inserts a host named `local` whose id, agent token and stream secret are read from `ZYN_AGENT_HOST_ID`, `ZYN_AGENT_TOKEN` and `ZYN_AGENT_STREAM_SECRET` in `.env`, so the API and the local agent already agree.
- Is safe to run twice.

---

## 8. Configuration reference

Put every variable in `.env.example` with a comment. Development defaults are shown. Secrets in `.env.example` must be obviously fake placeholders that the app refuses to start with when `ZYN_ENV=production`.

### 8.1 Compose services

| Variable | Default |
|---|---|
| `POSTGRES_USER` | `zynthera` |
| `POSTGRES_PASSWORD` | `zynthera_dev` |
| `POSTGRES_DB` | `zynthera` |
| `POSTGRES_PORT` | `5432` |
| `REDIS_PORT` | `6379` |

### 8.2 API

| Variable | Default | Meaning |
|---|---|---|
| `ZYN_ENV` | `development` | `development` or `production` |
| `ZYN_API_ADDR` | `127.0.0.1:8080` | Listen address |
| `ZYN_DATABASE_URL` | `postgres://zynthera:zynthera_dev@127.0.0.1:5432/zynthera?sslmode=disable` | |
| `ZYN_REDIS_URL` | `redis://127.0.0.1:6379/0` | |
| `ZYN_WEB_ORIGIN` | `http://localhost:3000` | Used for links in emails and for Origin checks |
| `ZYN_SECRETS_KEY` | placeholder | 32 random bytes, base64. Encrypts host credentials stored in the database |
| `ZYN_SESSION_TTL_HOURS` | `720` | |
| `ZYN_SMTP_HOST` / `ZYN_SMTP_PORT` | `127.0.0.1` / `1025` | Mailpit in development |
| `ZYN_SMTP_USER` / `ZYN_SMTP_PASS` | empty | |
| `ZYN_MAIL_FROM` | `Zynthera <no-reply@zynthera.local>` | |
| `ZYN_SEED_ADMIN_EMAIL` | `admin@zynthera.local` | |
| `ZYN_SEED_ADMIN_PASSWORD` | placeholder | |
| `ZYN_ALLOWED_RAM_MB` | `2048,4096,8192,16384` | Choices in the wizard |
| `ZYN_ALLOWED_VCPUS` | `1,2,4,8` | |
| `ZYN_ALLOWED_DISK_GB` | `40,80,160` | |
| `ZYN_DEFAULT_MAX_VMS` | `1` | Per-user limits, overridable per user in the database |
| `ZYN_DEFAULT_MAX_RAM_MB` | `16384` | |
| `ZYN_DEFAULT_MAX_VCPUS` | `8` | |
| `ZYN_DEFAULT_MAX_DISK_GB` | `160` | |
| `ZYN_IDLE_SUSPEND_MINUTES` | `15` | Suspend after this long with no viewer connected |
| `ZYN_IDLE_INPUT_MINUTES` | `60` | Viewer asks "still there?" after this long without input |
| `ZYN_MAX_KEEP_ALIVE_HOURS` | `12` | Longest keep-alive a user may set |
| `ZYN_MAX_SNAPSHOTS_PER_VM` | `5` | |
| `ZYN_MAX_UPLOAD_MB` | `2048` | |
| `ZYN_STREAM_TOKEN_TTL_SECONDS` | `60` | |
| `ZYN_FILES_TOKEN_TTL_SECONDS` | `600` | |
| `ZYN_HOST_OFFLINE_AFTER_SECONDS` | `45` | No heartbeat for this long means the host is offline |
| `ZYN_DISK_HEADROOM_PERCENT` | `10` | Share of host disk the scheduler never allocates |

### 8.3 Agent

In development these live in the same `.env`. On a real host they live in `/etc/zynthera/agent.env`.

| Variable | Default | Meaning |
|---|---|---|
| `ZYN_AGENT_ADDR` | `127.0.0.1:9090` | Listen address. Stays on loopback in production; Caddy fronts it |
| `ZYN_AGENT_HOST_ID` | fixed dev UUID | This host's id in the `hosts` table |
| `ZYN_AGENT_TOKEN` | placeholder | Shared secret for API to agent and agent to API calls |
| `ZYN_AGENT_STREAM_SECRET` | placeholder | Key for verifying browser tokens |
| `ZYN_AGENT_API_URL` | `http://127.0.0.1:8080` | Where to send heartbeats |
| `ZYN_AGENT_WEB_ORIGIN` | `http://localhost:3000` | The only Origin allowed on browser-facing endpoints |
| `ZYN_VM_DRIVER` | `mock` | `mock` or `libvirt` |
| `ZYN_STORAGE_DRIVER` | `qcow2` | `qcow2` or `zfs` |
| `ZYN_DATA_DIR` | `./.data/agent` in development, `/var/lib/zynthera` in production | |
| `ZYN_ZFS_DATASET` | `tank/zynthera` | Used by the ZFS driver |
| `ZYN_LIBVIRT_URI` | `qemu:///system` | |
| `ZYN_LIBVIRT_NETWORK` | `zynthera` | |
| `ZYN_VM_CPU_MODE` | `host-model` | Or a named CPU model, for moving saved VMs between hosts later |
| `ZYN_VM_BANDWIDTH_KBYTES` | `12500` | Per-VM cap in kilobytes per second (12500 is 100 Mbit/s) |
| `ZYN_FILE_BRIDGE_PORT` | `7070` | Port the file bridge listens on inside guests |
| `ZYN_MOCK_VNC_ADDR` | `127.0.0.1:5901` | The dev-desktop container |
| `ZYN_MOCK_FAIL` | empty | Comma-separated operations the mock driver should fail, for example `suspend,start` |
| `ZYN_HEARTBEAT_SECONDS` | `10` | |

### 8.4 Web

| Variable | Default | Meaning |
|---|---|---|
| `NEXT_PUBLIC_APP_NAME` | `Zynthera` | |
| `API_INTERNAL_URL` | `http://127.0.0.1:8080` | Target of the `/api` rewrite |

---

## 9. Database schema

All IDs are UUIDs and all timestamps are `timestamptz`. This is the target schema. Split it into migrations that match the milestones in section 18 (auth tables first, then hosts, then VMs). Queries live in `db/queries` and are compiled with `sqlc`.

```sql
CREATE EXTENSION IF NOT EXISTS citext;

-- ---------- auth ----------

CREATE TABLE users (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  email              citext NOT NULL UNIQUE,
  password_hash      text NOT NULL,
  role               text NOT NULL DEFAULT 'user' CHECK (role IN ('user', 'admin')),
  email_verified_at  timestamptz,
  disabled_at        timestamptz,
  -- per-user limit overrides; NULL means "use the default from config"
  max_vms            int,
  max_ram_mb         int,
  max_vcpus          int,
  max_disk_gb        int,
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id       uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash    bytea NOT NULL UNIQUE,          -- SHA-256 of the cookie value
  expires_at    timestamptz NOT NULL,
  last_seen_at  timestamptz NOT NULL DEFAULT now(),
  ip            inet,
  user_agent    text,
  created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sessions_user_id_idx ON sessions (user_id);
CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);

CREATE TABLE email_tokens (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  purpose     text NOT NULL CHECK (purpose IN ('verify_email', 'reset_password')),
  token_hash  bytea NOT NULL UNIQUE,
  expires_at  timestamptz NOT NULL,
  used_at     timestamptz,
  created_at  timestamptz NOT NULL DEFAULT now()
);

-- ---------- hosts ----------

CREATE TABLE hosts (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name               text NOT NULL UNIQUE,
  agent_url          text NOT NULL,   -- where the API calls the agent
  public_url         text NOT NULL,   -- where browsers reach the agent (https/wss base URL)
  agent_token_enc    bytea NOT NULL,  -- AES-GCM with ZYN_SECRETS_KEY
  stream_secret_enc  bytea NOT NULL,  -- AES-GCM with ZYN_SECRETS_KEY
  admin_state        text NOT NULL DEFAULT 'active'
                       CHECK (admin_state IN ('active', 'draining', 'disabled')),
  total_ram_mb       int NOT NULL DEFAULT 0,      -- reported by the agent
  total_vcpus        int NOT NULL DEFAULT 0,
  total_disk_gb      int NOT NULL DEFAULT 0,
  free_disk_gb       int NOT NULL DEFAULT 0,
  reserved_ram_mb    int NOT NULL DEFAULT 4096,   -- kept for the host OS
  cpu_overcommit     numeric(4,2) NOT NULL DEFAULT 4.0,
  images             jsonb NOT NULL DEFAULT '[]', -- [{slug, version}] present on the host
  agent_version      text,
  last_heartbeat_at  timestamptz,
  created_at         timestamptz NOT NULL DEFAULT now()
);

-- ---------- images ----------

CREATE TABLE os_images (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  slug             text NOT NULL UNIQUE,     -- e.g. ubuntu-24.04-xfce
  display_name     text NOT NULL,
  description      text NOT NULL DEFAULT '',
  current_version  text NOT NULL,            -- version new VMs are cloned from
  min_ram_mb       int NOT NULL,
  min_disk_gb      int NOT NULL,
  enabled          boolean NOT NULL DEFAULT true,
  sort_order       int NOT NULL DEFAULT 0,
  created_at       timestamptz NOT NULL DEFAULT now()
);

-- ---------- VMs ----------

CREATE TABLE vms (
  id                     uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id                uuid NOT NULL REFERENCES users(id),
  host_id                uuid NOT NULL REFERENCES hosts(id),
  os_image_id            uuid NOT NULL REFERENCES os_images(id),
  image_version          text NOT NULL,
  name                   text NOT NULL,             -- the user's label, shown in the UI only
  ram_mb                 int NOT NULL CHECK (ram_mb > 0),
  vcpus                  int NOT NULL CHECK (vcpus > 0),
  disk_gb                int NOT NULL CHECK (disk_gb > 0),
  linux_username         text NOT NULL,
  pending_password_hash  text,                      -- cleared once provisioning succeeds
  state                  text NOT NULL CHECK (state IN (
                           'provisioning', 'stopped', 'starting', 'running',
                           'suspending', 'suspended', 'stopping', 'rebooting',
                           'maintenance', 'deleting', 'deleted', 'error')),
  state_changed_at       timestamptz NOT NULL DEFAULT now(),
  state_detail           text,                      -- readable reason for error, or what maintenance is doing
  has_saved_state        boolean NOT NULL DEFAULT false,
  viewer_connected       boolean NOT NULL DEFAULT false,
  last_active_at         timestamptz,
  keep_alive_until       timestamptz,
  disk_used_bytes        bigint NOT NULL DEFAULT 0,
  created_at             timestamptz NOT NULL DEFAULT now(),
  updated_at             timestamptz NOT NULL DEFAULT now(),
  deleted_at             timestamptz
);
CREATE INDEX vms_user_idx ON vms (user_id) WHERE deleted_at IS NULL;
CREATE INDEX vms_host_state_idx ON vms (host_id, state) WHERE deleted_at IS NULL;

CREATE TABLE vm_events (
  id          bigserial PRIMARY KEY,
  vm_id       uuid NOT NULL REFERENCES vms(id) ON DELETE CASCADE,
  type        text NOT NULL,   -- created, started, resumed, suspended, stopped, error, ...
  actor       text NOT NULL CHECK (actor IN ('user', 'admin', 'system')),
  message     text NOT NULL DEFAULT '',
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX vm_events_vm_idx ON vm_events (vm_id, created_at DESC);

CREATE TABLE snapshots (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),  -- also the snapshot's name on disk
  vm_id       uuid NOT NULL REFERENCES vms(id) ON DELETE CASCADE,
  name        text NOT NULL,                               -- the user's label
  status      text NOT NULL DEFAULT 'creating' CHECK (status IN ('creating', 'ready', 'error')),
  size_bytes  bigint NOT NULL DEFAULT 0,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX snapshots_vm_idx ON snapshots (vm_id);

-- One row per period a VM was running. Billing will read this later.
CREATE TABLE usage_intervals (
  id          bigserial PRIMARY KEY,
  vm_id       uuid NOT NULL REFERENCES vms(id),
  user_id     uuid NOT NULL REFERENCES users(id),
  host_id     uuid NOT NULL REFERENCES hosts(id),
  ram_mb      int NOT NULL,
  vcpus       int NOT NULL,
  started_at  timestamptz NOT NULL,
  ended_at    timestamptz
);
CREATE UNIQUE INDEX usage_one_open_per_vm ON usage_intervals (vm_id) WHERE ended_at IS NULL;
CREATE INDEX usage_user_idx ON usage_intervals (user_id, started_at);
```

Notes:

- A host is "online" when `last_heartbeat_at` is newer than `ZYN_HOST_OFFLINE_AFTER_SECONDS`. That is computed, not stored. `admin_state` records what the admin wants.
- `vms.state` uses `starting` for both a fresh boot and a resume. The UI says "Resuming" when `has_saved_state` is true.
- The file bridge token is not in the database. The agent keeps it in the VM's `meta.json`.

---

## 10. Control plane (API)

### 10.1 HTTP conventions

- All routes are under `/api`. In development the Next.js app rewrites `/api/*` to the API, and in production Caddy routes `/api/*` to it, so the browser always talks to one origin and no CORS setup is needed for the API.
- Request and response bodies are JSON. State-changing requests must send `Content-Type: application/json`.
- Errors use one envelope:

```json
{ "error": { "code": "vm_invalid_state", "message": "This PC is already starting.", "details": {} } }
```

| Code | HTTP status | When |
|---|---|---|
| `validation_failed` | 422 | Bad input. `details.fields` maps field names to messages |
| `unauthenticated` | 401 | No valid session |
| `forbidden` | 403 | Not allowed (for example a non-admin on an admin route) |
| `email_not_verified` | 403 | Action needs a verified email |
| `not_found` | 404 | Missing, or owned by someone else |
| `vm_invalid_state` | 409 | Action not allowed in the VM's current state |
| `limit_exceeded` | 422 | Over the user's VM, RAM, CPU, disk or snapshot limit |
| `no_capacity` | 503 | No host can run this VM right now |
| `host_unavailable` | 503 | The VM's host is offline |
| `rate_limited` | 429 | Too many requests. Sets `Retry-After` |
| `internal` | 500 | Anything unexpected. Log the cause; never return it |

- Every `/api/vms/:id/...` route checks that the VM belongs to the logged-in user. A VM owned by someone else returns 404, never 403, so IDs cannot be probed.
- Middleware order: request ID, panic recovery, access log, security headers, body size limit, session loading, Origin check, rate limit, handler.

### 10.2 Auth

- Passwords: minimum 10 characters, maximum 128. Hash with argon2id (`golang.org/x/crypto/argon2`), parameters in config, encoded in the standard PHC string format so they can be raised later.
- Session: 32 random bytes, base64url, set in the `zyn_session` cookie with `HttpOnly`, `SameSite=Lax`, `Path=/`, and `Secure` in production. Only the SHA-256 hash is stored.
- CSRF: because the API is same-origin and cookie-authenticated, every request with an unsafe method must carry an `Origin` header equal to `ZYN_WEB_ORIGIN` (fall back to checking `Sec-Fetch-Site: same-origin`). Reject otherwise.
- Email verification is required before creating a VM. Unverified users can log in and see the dashboard with a prompt to verify.
- Verification and reset tokens: 32 random bytes, stored hashed, single use. Verification expires after 24 hours, reset after 1 hour. A successful password reset deletes all of the user's sessions.
- Sign-up and forgot-password return the same response whether or not the email exists, so accounts cannot be enumerated.
- Rate limits, kept in Redis:

| Route | Limit |
|---|---|
| Sign-up | 5 per hour per IP |
| Login | 10 per 15 minutes per IP and email pair |
| Forgot password | 3 per hour per email, 10 per hour per IP |
| Resend verification | 3 per hour per user |
| Stream and file tokens | 30 per minute per user |
| VM actions | 20 per minute per user |

### 10.3 Endpoints

```text
POST   /api/auth/signup                 { email, password }
POST   /api/auth/login                  { email, password }
POST   /api/auth/logout                 suspends the user's running VMs (unless keep-alive is active)
GET    /api/auth/me
POST   /api/auth/verify-email           { token }
POST   /api/auth/resend-verification
POST   /api/auth/forgot-password        { email }
POST   /api/auth/reset-password         { token, password }

GET    /api/images                      enabled OS images that at least one online host has
GET    /api/options                     allowed RAM/CPU/disk sizes, the user's limits and current usage
GET    /api/events                      Server-Sent Events: live state of the user's VMs

GET    /api/vms
POST   /api/vms                         { name, image, ram_mb, vcpus, disk_gb, linux_username, linux_password }
GET    /api/vms/:id
PATCH  /api/vms/:id                     { name?, keep_alive_hours?, ram_mb?, vcpus?, disk_gb? }
DELETE /api/vms/:id
POST   /api/vms/:id/start
POST   /api/vms/:id/suspend
POST   /api/vms/:id/shutdown
POST   /api/vms/:id/force-stop
POST   /api/vms/:id/reboot
POST   /api/vms/:id/discard-saved-state
POST   /api/vms/:id/stream-token        -> { url, token, expires_at }
POST   /api/vms/:id/files-token         -> { base_url, token, expires_at }
GET    /api/vms/:id/events              audit history, newest first

GET    /api/vms/:id/snapshots
POST   /api/vms/:id/snapshots           { name }
POST   /api/vms/:id/snapshots/:sid/restore
DELETE /api/vms/:id/snapshots/:sid

GET    /api/admin/hosts
POST   /api/admin/hosts                 { name, agent_url, public_url } -> includes agent_token and stream_secret, shown once
PATCH  /api/admin/hosts/:id             { admin_state?, reserved_ram_mb?, cpu_overcommit? }
GET    /api/admin/vms                   filters: state, host, user email; cursor pagination
POST   /api/admin/vms/:id/suspend
POST   /api/admin/vms/:id/force-stop
GET    /api/admin/users                 cursor pagination
PATCH  /api/admin/users/:id             limits, disable
GET    /api/admin/usage                 hours per user and per size for a date range

POST   /internal/agent/heartbeat        bearer: the host's agent token
POST   /internal/agent/stream-event     { vm_id, event: "connected" | "disconnected" }
GET    /health                          checks Postgres and Redis
```

The VM object returned everywhere:

```json
{
  "id": "6f1c0b1e-...",
  "name": "My PC",
  "image": { "slug": "ubuntu-24.04-xfce", "display_name": "Ubuntu 24.04" },
  "ram_mb": 4096,
  "vcpus": 2,
  "disk_gb": 80,
  "disk_used_bytes": 9123456789,
  "linux_username": "alex",
  "state": "running",
  "state_detail": null,
  "has_saved_state": false,
  "viewer_connected": true,
  "keep_alive_until": null,
  "last_active_at": "2026-11-19T10:15:00Z",
  "created_at": "2026-11-18T09:00:00Z",
  "allowed_actions": ["connect", "suspend", "shutdown", "force_stop", "reboot", "snapshot", "delete"]
}
```

`allowed_actions` is computed by the state machine package. The UI enables buttons from this list and never re-implements the rules.

Input validation for `POST /api/vms`:

- `name`: 1 to 40 characters after trimming.
- `image`: slug of an enabled image.
- `ram_mb`, `vcpus`, `disk_gb`: must be in the allowed lists, at least the image's minimums, and within the user's limits.
- `linux_username`: matches `^[a-z][a-z0-9_-]{2,30}$` and is not a reserved name (`root`, `admin`, `daemon`, `sys`, `ubuntu`, `debian`, `lightdm`, `nobody`, `zynthera`, and any other system account present in the images).
- `linux_password`: 8 to 128 characters. Hash it immediately with SHA-512 crypt (the `$6$` format that Linux and cloud-init accept), store the hash in `pending_password_hash`, and never log, queue or persist the plain text.

### 10.4 VM state machine

Stable states: `stopped`, `running`, `suspended`, `error`. Everything else is transitional and means a job is working on the VM.

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

| Action | Allowed from | Transitional state | Job | Ends in |
|---|---|---|---|---|
| Create | (new) | `provisioning` then `starting` | `vm:provision` then `vm:start` | `running` |
| Start / resume | `stopped`, `suspended`, `error` | `starting` | `vm:start` | `running` |
| Suspend | `running` | `suspending` | `vm:suspend` | `suspended` |
| Shut down | `running` | `stopping` | `vm:shutdown` | `stopped`, or back to `running` with a message if the guest did not comply in time |
| Force stop | `running`, `error` | `stopping` | `vm:force_stop` | `stopped` |
| Reboot | `running` | `rebooting` | `vm:reboot` | `running` |
| Discard saved state | `suspended`, `error` (when `has_saved_state`) | `maintenance` | `vm:discard_saved_state` | `stopped` |
| Resize | `stopped` with no saved state | `maintenance` | `vm:resize` | `stopped` |
| Create snapshot | `running`, `suspended`, `stopped` | `maintenance` | `snapshot:create` | the state it started from |
| Restore snapshot | `suspended`, `stopped` | `maintenance` | `snapshot:restore` | `stopped` |
| Delete snapshot | `suspended`, `stopped` | `maintenance` | `snapshot:delete` | the state it started from |
| Delete | `stopped`, `suspended`, `running`, `error` | `deleting` | `vm:delete` | `deleted` |

How transitions are made safe:

- **The transitional state is the lock.** An API handler moves a VM out of a stable state with one atomic statement, for example `UPDATE vms SET state = 'starting', state_changed_at = now() WHERE id = $1 AND state IN ('stopped', 'suspended', 'error') RETURNING *`. If no row comes back, respond `vm_invalid_state`. Only after the update succeeds is the job queued. Two requests can never both win.
- **Jobs are idempotent.** A job first checks that the VM is still in the transitional state it expects; if not, it exits successfully. The agent operations are idempotent too: suspending an already suspended VM succeeds.
- **Jobs set the final state** in a transaction together with the event row and the usage interval change.
- **A job that runs out of retries** moves the VM to `error` with a readable `state_detail`. From `error` the user can start, force stop, discard the saved state, or delete.
- **Write the state machine as pure functions** in `internal/api/vms` (allowed actions for a state, target states for an action) with table-driven tests. Handlers, jobs and `allowed_actions` all use it.

Usage intervals: open one when a VM enters `running`, close it when the VM leaves `running` for anything other than `rebooting`.

### 10.5 Jobs

Queues: `critical` (start, suspend, force stop), `default` (everything else on demand), `low` (periodic tasks).

| Task | Payload | Timeout | Retries | Notes |
|---|---|---|---|---|
| `vm:provision` | `vm_id` | 10 min | 2 | Generates a fresh file bridge token per attempt. On success clears `pending_password_hash`, sets `starting`, queues `vm:start` |
| `vm:start` | `vm_id` | 5 min | 1 | Re-checks capacity. After a resume, syncs the guest clock. Sets `has_saved_state = false`, `last_active_at = now()` |
| `vm:suspend` | `vm_id`, `reason` | 10 min | 2 | Reason is `user`, `logout`, `idle` or `admin`. Sets `has_saved_state = true` |
| `vm:shutdown` | `vm_id` | 3 min | 0 | If the guest is still up at the timeout, return to `running` with a hint to use force stop |
| `vm:force_stop` | `vm_id` | 1 min | 3 | Also removes any saved state |
| `vm:reboot` | `vm_id` | 3 min | 1 | |
| `vm:discard_saved_state` | `vm_id` | 1 min | 3 | |
| `vm:resize` | `vm_id`, new sizes | 5 min | 1 | |
| `vm:delete` | `vm_id` | 5 min | 5 | Force stops if needed, removes everything on the host, sets `deleted_at` |
| `snapshot:create` | `vm_id`, `snapshot_id`, `return_to` | 10 min | 1 | If the VM was running: suspend, snapshot, start again |
| `snapshot:restore` | `vm_id`, `snapshot_id` | 10 min | 1 | Removes saved memory state first |
| `snapshot:delete` | `vm_id`, `snapshot_id`, `return_to` | 5 min | 3 | |

Periodic tasks:

| Task | Every | What it does |
|---|---|---|
| `cron:idle_sweep` | 1 min | Queues `vm:suspend` (reason `idle`) for VMs that are `running`, have no viewer connected, were last active more than `ZYN_IDLE_SUSPEND_MINUTES` ago, and have no keep-alive in the future |
| `cron:reconcile` | 30 s | Compares each online host's last heartbeat with the database and fixes drift (section 10.7) |
| `cron:stuck_vms` | 2 min | Finds VMs in a transitional state for longer than that job's timeout plus a margin, asks the agent for the real status and settles the VM into the matching stable state or `error` |
| `cron:session_cleanup` | 1 h | Deletes expired sessions and email tokens |

Every job logs the VM id, the task and the attempt number, and writes a `vm_events` row on success and on final failure.

### 10.6 Scheduling and capacity

Definitions for one host:

- VMs that hold RAM and CPU: those in `starting`, `running`, `suspending`, `stopping`, `rebooting`.
- `free_ram = total_ram_mb - reserved_ram_mb - sum(ram_mb of VMs holding RAM)`
- `free_vcpus = total_vcpus * cpu_overcommit - sum(vcpus of VMs holding RAM)`
- Disk committed by a VM: `disk_gb + ram_mb / 1024`. The second term reserves room for its saved memory image.
- `free_disk = total_disk_gb * (1 - headroom) - sum(disk committed by every VM not deleted)`

Rules:

- **Create:** choose an online host with `admin_state = 'active'` that has the image and enough `free_disk`. Among candidates pick the one with the most free disk.
- **Start or resume:** the VM's own host must be online and must have `free_ram >= ram_mb` and `free_vcpus >= vcpus`. RAM is never overcommitted.
- Do the capacity check and the state change in one transaction that first locks the host row with `SELECT ... FOR UPDATE`, so two simultaneous starts cannot both take the last free RAM.
- If the check fails, return `no_capacity` with a clear message. Do not queue the request silently.
- A `draining` host accepts no new VMs. Its existing VMs can still be started so users are not locked out.
- Per-user limits (number of VMs, RAM, vCPUs and disk summed over their VMs) come from the user row or the defaults in config.

### 10.7 Heartbeats and reconciliation

The agent posts a heartbeat every `ZYN_HEARTBEAT_SECONDS`:

```json
{
  "host_id": "…",
  "agent_version": "0.1.0",
  "capacity": { "total_ram_mb": 128000, "total_vcpus": 32, "total_disk_gb": 1800, "free_disk_gb": 1400 },
  "images": [ { "slug": "ubuntu-24.04-xfce", "version": "2026.11.1" } ],
  "vms": [ { "id": "…", "status": "running", "has_saved_state": false, "viewer_connected": true, "disk_used_bytes": 9123456789 } ]
}
```

The API stores capacity, images and the heartbeat time, updates `disk_used_bytes` and `viewer_connected`, and keeps the VM list for the reconciler. VMs in a transitional state are left alone; a job owns them.

| Database says | Host says | Action |
|---|---|---|
| `running` | `stopped` | Set `stopped`, close the usage interval, event "Shut down from inside the PC" |
| `running` | `suspended` | Set `suspended` and `has_saved_state` (this is what a host reboot looks like) |
| `suspended` or `stopped` | `running` | Set `running`, open a usage interval, log a warning event |
| `suspended` | `stopped` | Set `stopped`, clear `has_saved_state`, event "Saved session was lost" |
| any stable state | missing | Set `error` with detail "PC not found on its host" |
| not in the database | present | Log a warning. Never delete automatically |

A drift must be seen in two consecutive heartbeats before it is acted on, so a heartbeat that races with a finishing job does not cause a wrong correction.

### 10.8 Live updates

Workers publish every VM state change to a Redis channel named for the owning user. `GET /api/events` subscribes to that channel and forwards each change as a Server-Sent Event:

```text
event: vm
data: { ...the full VM object... }
```

Send the current list on connect, and a comment line every 20 seconds as a keep-alive. If events arrive late through the development rewrite, the proxy is buffering: disable compression for this route and set `X-Accel-Buffering: no` and `Cache-Control: no-cache`.

### 10.9 Tokens for the stream and for files

One format, implemented in `internal/shared/token` and used by both the API (signing) and the agent (verifying):

```text
base64url(JSON payload) + "." + base64url(HMAC-SHA256(payload bytes, host stream secret))
```

Payload:

```json
{ "v": 1, "jti": "random id", "vm": "<vm id>", "uid": "<user id>", "pur": "stream", "exp": 1795000000 }
```

- `pur` is `stream` or `files`. The agent rejects a token used for the wrong purpose.
- A `stream` token lives `ZYN_STREAM_TOKEN_TTL_SECONDS` and is single use: the agent remembers each `jti` until it expires. It only has to be valid at connection time.
- A `files` token lives `ZYN_FILES_TOKEN_TTL_SECONDS` and can be used many times for one VM. The browser asks for a new one when it expires.
- The API issues a stream token only for a `running` VM the caller owns, and a files token under the same condition.
- Compare signatures in constant time.

---

## 11. Host agent

### 11.1 What the agent is

A single Go binary running on each VM host. It has no database. Everything it knows comes from libvirt and from the per-VM directory under `ZYN_DATA_DIR` (section 5.2). It holds a mutex per VM so two operations on the same VM never overlap; a second request for a busy VM gets `409 busy` and the calling job retries.

It runs as root on a real host, because it manages disks, network reservations and libvirt. That is the reason it is small, listens on loopback only, and accepts just two kinds of caller: the control plane with the agent token, and browsers with a signed token.

### 11.2 Shared types

Defined in `internal/shared/vmtypes` and used by both the API and the agent.

```go
type VMSpec struct {
    ID           string // VM UUID
    ImageSlug    string
    ImageVersion string
    RAMMB        int
    VCPUs        int
    DiskGB       int
    Hostname     string
    Username     string
    PasswordHash string // SHA-512 crypt; never the plain password
    BridgeToken  string // secret for the guest file bridge
}

type VMStatus struct {
    ID              string
    Status          string // running | stopped | suspended | paused | crashed | missing
    HasSavedState   bool
    ViewerConnected bool
    DiskUsedBytes   int64
    PrivateIP       string
}

type DisplayEndpoint struct {
    Network string // "tcp" or "unix"
    Address string // "127.0.0.1:5903" or a socket path
}
```

### 11.3 Driver interface

All VM operations go through one interface so the mock and the real implementation are interchangeable. Select with `ZYN_VM_DRIVER`.

```go
type Driver interface {
    Create(ctx context.Context, spec VMSpec) error   // idempotent: finishes or redoes a partial create
    Start(ctx context.Context, id string) error      // boots, or resumes if a saved state exists
    Suspend(ctx context.Context, id string) error    // saves memory to disk and stops the VM
    Shutdown(ctx context.Context, id string, timeout time.Duration) error // clean guest shutdown
    ForceStop(ctx context.Context, id string) error  // power off now; discards saved state
    Reboot(ctx context.Context, id string) error
    DiscardSavedState(ctx context.Context, id string) error
    Resize(ctx context.Context, id string, ramMB, vcpus, diskGB int) error // VM must be off, disk grows only
    Delete(ctx context.Context, id string) error     // removes domain, disk, seed, saved state, snapshots, IP reservation

    Status(ctx context.Context, id string) (VMStatus, error)
    List(ctx context.Context) ([]VMStatus, error)
    Display(ctx context.Context, id string) (DisplayEndpoint, error)
    SyncClock(ctx context.Context, id string) error
    GuestAddress(ctx context.Context, id string) (string, error) // for the file bridge

    SnapshotCreate(ctx context.Context, id, snapshotID string) error
    SnapshotRestore(ctx context.Context, id, snapshotID string) error
    SnapshotDelete(ctx context.Context, id, snapshotID string) error
    SnapshotList(ctx context.Context, id string) ([]SnapshotInfo, error)

    Capacity(ctx context.Context) (Capacity, error)
    Images(ctx context.Context) ([]ImageRef, error)
}
```

Every method is idempotent: calling it when the VM is already in the target condition succeeds.

Write one **conformance test suite** in `internal/agent/driver` that takes any `Driver` and exercises the full lifecycle, including the idempotency rules. It runs against the mock driver in normal test runs, and against the libvirt driver when `ZYN_TEST_LIBVIRT=1` is set on a KVM host.

### 11.4 Mock driver

Purpose: develop and test the whole product on a machine without KVM.

- Keeps VM state in memory and in a JSON file under `ZYN_DATA_DIR`, so it survives agent restarts.
- Simulates realistic delays: create 5 s, start 3 s, suspend 2 s, delete 1 s.
- Fails the operations listed in `ZYN_MOCK_FAIL`, so error handling in the API and the UI can be tested.
- `Display` returns `ZYN_MOCK_VNC_ADDR`, the `dev-desktop` container. Every mock VM shows the same desktop. `Display` returns an error unless the mock VM is `running`, so the stream closes on suspend like it does for real.
- File operations work against a temp directory per mock VM.
- Reports a fixed fake capacity (for example 32 GB RAM, 16 vCPUs, 500 GB disk) and the seeded image list.

Everything above the driver (API, jobs, state machine, tokens, stream proxy, UI) runs unchanged against either driver.

### 11.5 Storage interface

```go
type Storage interface {
    CloneImage(ctx context.Context, image ImageRef, vmID string, sizeGB int) (DiskRef, error)
    Delete(ctx context.Context, vmID string) error
    Grow(ctx context.Context, vmID string, sizeGB int) error
    SnapshotCreate(ctx context.Context, vmID, snapshotID string) error
    SnapshotRestore(ctx context.Context, vmID, snapshotID string) error
    SnapshotDelete(ctx context.Context, vmID, snapshotID string) error
    SnapshotList(ctx context.Context, vmID string) ([]SnapshotInfo, error)
    UsedBytes(ctx context.Context, vmID string) (int64, error)
    HostDisk(ctx context.Context) (totalGB, freeGB int, err error)
    Images(ctx context.Context) ([]ImageRef, error)
}
```

`DiskRef` tells the domain XML builder whether the disk is a qcow2 file or a raw block device. Implement `qcow2` in milestone 5 and `zfs` in milestone 7. Select with `ZYN_STORAGE_DRIVER`.

### 11.6 Agent HTTP API

Called by the control plane. Every request carries `Authorization: Bearer <agent token>`. Compare in constant time.

```text
GET    /health
GET    /capacity
GET    /vms
POST   /vms                             body: VMSpec
GET    /vms/{id}
POST   /vms/{id}/start
POST   /vms/{id}/suspend
POST   /vms/{id}/shutdown               { timeout_seconds }
POST   /vms/{id}/force-stop
POST   /vms/{id}/reboot
POST   /vms/{id}/discard-saved-state
POST   /vms/{id}/resize                 { ram_mb, vcpus, disk_gb }
POST   /vms/{id}/sync-clock
DELETE /vms/{id}
GET    /vms/{id}/snapshots
POST   /vms/{id}/snapshots              { snapshot_id }
POST   /vms/{id}/snapshots/{sid}/restore
DELETE /vms/{id}/snapshots/{sid}
```

Calls are synchronous: the API's job worker calls and waits, with the job's timeout as the deadline. Validate every `{id}` and `{sid}` as a UUID before using it. Return errors in the same envelope as the API, with codes such as `busy`, `not_found`, `invalid_state` and `driver_error` (with the command's stderr in `details`, which the API logs but never shows to users verbatim).

Called by browsers, authenticated by a signed token (section 10.9) and restricted to the configured web origin:

```text
GET    /stream?token=...                WebSocket upgrade; token purpose "stream"
GET    /files/list?path=...             Authorization: Bearer <files token>
PUT    /files/upload?name=...           Authorization: Bearer <files token>; body is the file
GET    /files/download?path=...&token=... browser download; token purpose "files"
```

### 11.7 Desktop stream proxy

noVNC in the browser speaks the VNC protocol over a WebSocket. The agent bridges that WebSocket to the VM's VNC port.

On each connection the agent:

1. Rejects the request unless the `Origin` header equals `ZYN_AGENT_WEB_ORIGIN`.
2. Verifies the token: signature, expiry, purpose `stream`, and that the `jti` has not been used.
3. Asks the driver for the VM's display endpoint. If the VM is not running, closes with code 4404.
4. Accepts the WebSocket (accept the `binary` subprotocol if the client offers it) and dials the VNC endpoint.
5. Copies bytes in both directions as binary frames until either side closes. Sends a WebSocket ping every 20 seconds.
6. Allows one viewer per VM. A new valid connection closes the old one with code 4409, so opening the desktop in a second tab takes over.
7. Tells the API when a viewer connects and disconnects (`/internal/agent/stream-event`), and also reports `viewer_connected` in every heartbeat so a lost event corrects itself.

Close codes: 4401 invalid or expired token, 4403 wrong origin, 4404 VM not running, 4409 replaced by a newer connection.

Never log the query string of `/stream` or `/files/download`. It contains a token.

Use a maintained WebSocket library (for example `coder/websocket` or `gorilla/websocket`). Check its current API when you add it.

### 11.8 File proxy

The browser never talks to a VM directly. The agent forwards file requests to the file bridge inside the guest (section 14) at `http://<guest ip>:<ZYN_FILE_BRIDGE_PORT>`, adding the bridge token from the VM's `meta.json`.

- Verify the `files` token and that its VM matches a running VM on this host.
- Stream request and response bodies. Never buffer a file in memory.
- Enforce the maximum upload size while streaming and abort when it is exceeded.
- Send CORS headers that allow only `ZYN_AGENT_WEB_ORIGIN`, and answer preflight requests.

---

## 12. The libvirt driver in detail

The commands and XML in this section are a reference. Check them against the versions installed on the host (`virsh --version`, `qemu-system-x86_64 --version`) and the libvirt documentation, and adjust anything that has changed. Record what you changed in `docs/DECISIONS.md`.

Run every command with `exec.CommandContext` and an argument slice. Capture stdout and stderr, and include stderr in the returned error.

### 12.1 Creating a VM

1. If a directory for this VM already exists from an earlier attempt, remove whatever it left behind (domain, disk, reservation) and start clean.
2. Reserve a MAC address (`52:54:00` plus three random bytes) and the lowest free IP in the VM subnet, under a host-wide lock. Add a static DHCP entry so the VM always gets that IP:
   `virsh net-update zynthera add ip-dhcp-host "<host mac='…' ip='…'/>" --live --config`
   A fixed IP matters because a suspended VM keeps its address while asleep. With ordinary leases, the address could be handed to another VM in the meantime.
3. Clone the disk: `qemu-img create -f qcow2 -F qcow2 -b <golden image> <vm dir>/disk.qcow2 <disk_gb>G`.
4. Render the cloud-init files (section 12.6) and build the seed: `cloud-localds <vm dir>/seed.iso user-data meta-data`. Delete the rendered `user-data` afterwards.
5. Write `meta.json` with mode 0600.
6. Build the domain XML, validate it, and run `virsh define`.

### 12.2 Domain XML

Build the XML from Go structs with `encoding/xml`, not by string concatenation. Keep golden-file tests for it and validate the output with `virt-xml-validate <file> domain` in tests when the tool is available.

```xml
<domain type='kvm'>
  <name>zyn-VM_ID</name>
  <uuid>VM_ID</uuid>
  <memory unit='MiB'>RAM_MB</memory>
  <currentMemory unit='MiB'>RAM_MB</currentMemory>
  <vcpu placement='static'>VCPUS</vcpu>
  <os>
    <type arch='x86_64' machine='q35'>hvm</type>
    <boot dev='hd'/>
  </os>
  <features>
    <acpi/>
    <apic/>
  </features>
  <cpu mode='host-model'/>
  <clock offset='utc'>
    <timer name='rtc' tickpolicy='catchup'/>
    <timer name='pit' tickpolicy='delay'/>
    <timer name='hpet' present='no'/>
  </clock>
  <pm>
    <suspend-to-mem enabled='no'/>
    <suspend-to-disk enabled='no'/>
  </pm>
  <on_poweroff>destroy</on_poweroff>
  <on_reboot>restart</on_reboot>
  <on_crash>destroy</on_crash>
  <devices>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2' cache='none' discard='unmap'/>
      <source file='/var/lib/zynthera/vms/VM_ID/disk.qcow2'/>
      <target dev='vda' bus='virtio'/>
    </disk>
    <disk type='file' device='cdrom'>
      <driver name='qemu' type='raw'/>
      <source file='/var/lib/zynthera/vms/VM_ID/seed.iso'/>
      <target dev='sda' bus='sata'/>
      <readonly/>
    </disk>
    <interface type='network'>
      <mac address='52:54:00:XX:XX:XX'/>
      <source network='zynthera'/>
      <model type='virtio'/>
      <port isolated='yes'/>
      <filterref filter='clean-traffic'>
        <parameter name='IP' value='VM_IP'/>
      </filterref>
      <bandwidth>
        <inbound average='12500'/>
        <outbound average='12500'/>
      </bandwidth>
    </interface>
    <channel type='unix'>
      <target type='virtio' name='org.qemu.guest_agent.0'/>
    </channel>
    <input type='tablet' bus='usb'/>
    <graphics type='vnc' port='-1' autoport='yes' listen='127.0.0.1'/>
    <video>
      <model type='virtio' heads='1' primary='yes'/>
    </video>
    <rng model='virtio'>
      <backend model='random'>/dev/urandom</backend>
    </rng>
    <memballoon model='none'/>
  </devices>
</domain>
```

Why these choices:

- **VNC on `127.0.0.1` with an automatic port.** Guests cannot reach the host's loopback, and nothing outside the host can either. The agent's proxy is the only way in. A per-VM Unix socket is a possible hardening step later.
- **USB tablet input.** Without it the mouse pointer in a VNC session drifts away from the real pointer.
- **`host-model` CPU.** Fine for one host. A saved memory image can only be restored on a host with a compatible CPU, so with several different hosts a fixed named model is needed. That is what `ZYN_VM_CPU_MODE` is for.
- **No memory balloon and no guest sleep states.** The host does the suspending. Guest-level sleep would fight with it.
- **`<port isolated='yes'/>` and `clean-traffic`.** VMs cannot talk to each other and cannot fake another VM's address. Port isolation needs a reasonably recent libvirt; if the host's version lacks it, use an nwfilter that drops traffic to the rest of the VM subnet.
- **The seed ISO stays attached for the life of the VM.** A saved memory image can only be restored into the same virtual hardware, so the device list must never change while a saved state exists.
- Set the guest's default screen resolution to 1920x1080, either in the image or with a `<resolution>` element on the video model if the installed libvirt supports it.

### 12.3 Operations

| Operation | Command | Notes |
|---|---|---|
| Start or resume | `virsh start zyn-<id>` | libvirt restores the saved state automatically if there is one |
| Suspend | `virsh managedsave zyn-<id>` | Memory goes to disk and the QEMU process exits. Add `--bypass-cache` if it keeps the host responsive during large saves |
| Shut down | `virsh shutdown zyn-<id> --mode agent`, falling back to `--mode acpi` | Poll `virsh domstate` until `shut off` or the timeout |
| Force stop | `virsh destroy zyn-<id>` then `virsh managedsave-remove zyn-<id>` if a saved state exists | |
| Reboot | `virsh reboot zyn-<id>` | |
| Discard saved state | `virsh managedsave-remove zyn-<id>` | |
| Sync clock | `virsh domtime zyn-<id> --now` | Needs the guest agent. Retry a few times after a resume; treat failure as a warning |
| Display | `virsh vncdisplay zyn-<id>` | Returns `127.0.0.1:N`; the TCP port is 5900 + N |
| Guest address | Read `meta.json` | The IP was reserved at creation |
| Resize | `virsh setmaxmem` and `virsh setvcpus` with `--config`, or redefine the domain; `qemu-img resize` for the disk | Only when shut off with no saved state |
| Delete | `virsh destroy` if running, `virsh undefine zyn-<id> --managed-save --snapshots-metadata`, remove the DHCP entry with `virsh net-update zynthera delete ip-dhcp-host …`, delete the VM directory | Each step tolerates "already gone" |
| List | `virsh list --all --name` filtered to the `zyn-` prefix | |

Status mapping, from `virsh domstate` and the "Managed save" line of `virsh dominfo`:

| domstate | Managed save | Agent reports |
|---|---|---|
| `running` | any | `running` |
| `shut off` | yes | `suspended` |
| `shut off` | no | `stopped` |
| `paused` | any | `paused` |
| `crashed` | any | `crashed` |
| domain not defined | | `missing` |

### 12.4 Suspend and resume rules

This is the core feature of the product, so the rules are strict.

- After a resume, the guest clock is behind by the time it spent asleep. Sync it through the guest agent, and run `chrony` in the image as a second line of defense.
- If a restore fails, do not delete the saved state automatically. Report the libvirt error, leave the VM `shut off` with its saved state, and let the control plane mark it `error`. The user gets a "discard saved session and start fresh" action. Their disk is unaffected.
- Never change RAM, CPU count or devices while a saved state exists.
- Restoring a disk snapshot removes the saved memory state first. Memory from now must never be combined with a disk from earlier.
- Saved images use disk space roughly equal to the RAM the guest was using. Section 12.8 covers compressing them.

### 12.5 Snapshots

- V1 snapshots are disk only and are taken while no QEMU process has the disk open, meaning the VM is suspended or shut off. For a running VM, the control plane suspends it, takes the snapshot and starts it again.
- qcow2 driver: `qemu-img snapshot -c <snapshot id> disk.qcow2` to create, `-a` to restore, `-d` to delete, `qemu-img info --output=json` to list.
- A snapshot taken while the VM is suspended captures the disk as it was mid-session. Booting from it later is like booting after a power cut: the filesystem journal recovers it. The UI should say that shutting down first gives the cleanest snapshot.
- Check whether `qemu-img resize` works on an image that has internal snapshots with the installed QEMU. If it does not, the resize action must require deleting snapshots first and say so.
- ZFS driver: `zfs snapshot`, `zfs rollback`, `zfs destroy`. Rolling back to anything but the newest snapshot destroys the newer ones. The UI must warn about that before restoring when the storage driver is ZFS.

### 12.6 cloud-init seed

`meta-data`:

```yaml
instance-id: VM_ID
local-hostname: zynthera-ab12cd
```

`user-data`:

```yaml
#cloud-config
hostname: zynthera-ab12cd
manage_etc_hosts: true
ssh_pwauth: false

users:
  - name: USERNAME
    gecos: USERNAME
    shell: /bin/bash
    groups: [sudo, adm, audio, video, plugdev]
    sudo: "ALL=(ALL:ALL) ALL"
    lock_passwd: false
    passwd: "PASSWORD_HASH"

growpart:
  mode: auto
  devices: ["/"]

write_files:
  - path: /etc/zynthera/file-bridge.env
    permissions: "0600"
    content: |
      ZYN_BRIDGE_TOKEN=BRIDGE_TOKEN
      ZYN_BRIDGE_PORT=7070
  - path: /etc/systemd/system/zynthera-file-bridge.service.d/10-user.conf
    content: |
      [Service]
      User=USERNAME
  - path: /etc/lightdm/lightdm.conf.d/50-zynthera-autologin.conf
    content: |
      [Seat:*]
      autologin-user=USERNAME
      autologin-user-timeout=0

runcmd:
  - systemctl daemon-reload
  - systemctl enable --now zynthera-file-bridge.service
  - systemctl restart lightdm.service
```

- Render this with a YAML library from a struct, not with string templates, so no value can break out of its field.
- The password arrives already hashed. The plain text never reaches the host.
- The file bridge env file must be readable by the service. If the service runs as the user, either let systemd load it as root through `EnvironmentFile=` (systemd reads it before dropping privileges) or adjust ownership. Verify that it works.
- Autologin details differ between distributions and display managers. Verify them for each image while building it.

### 12.7 Network and isolation

`infra/host-setup/network.xml`:

```xml
<network>
  <name>zynthera</name>
  <forward mode='nat'/>
  <bridge name='zynbr0' stp='on' delay='0'/>
  <ip address='10.210.0.1' netmask='255.255.0.0'>
    <dhcp>
      <range start='10.210.0.10' end='10.210.255.250'/>
    </dhcp>
  </ip>
</network>
```

Host firewall rules, in `infra/host-setup/firewall.nft`. Put them in their own nftables table so libvirt's NAT rules and ours do not overwrite each other, and check which firewall backend the installed libvirt uses.

- From VMs to the host itself: allow DHCP (UDP 67) and DNS (UDP and TCP 53) to `10.210.0.1`. Drop everything else. In particular VMs must not reach the agent, SSH or anything else on the host.
- From VMs outward: drop TCP port 25 so VMs cannot send spam. Drop forwarded traffic to private and link-local ranges (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `169.254.0.0/16`) so VMs cannot probe the hosting provider's internal network or a metadata service. Allow the rest; users need the internet to install software.
- No IPv6 for guests in V1.
- The bandwidth cap in the domain XML limits each VM.

### 12.8 Host configuration

All of this goes into `docs/DEPLOY_HOST.md` and, where possible, into idempotent scripts under `infra/host-setup/`. Recommended host OS: the current Ubuntu Server LTS.

1. Install packages: QEMU/KVM, libvirt daemon and clients, `qemu-utils`, `cloud-image-utils` (for `cloud-localds`), `libguestfs-tools`, `nftables`, Caddy. Verify the package names for the host's distribution.
2. Create `/var/lib/zynthera/{images,vms}` and `/etc/zynthera`.
3. Define, start and autostart the `zynthera` network. Load the firewall rules and make them persistent.
4. Configure `libvirt-guests` so that a host shutdown suspends running VMs and a host boot does not start them. On Debian and Ubuntu that is `ON_SHUTDOWN=suspend` and `ON_BOOT=ignore` in `/etc/default/libvirt-guests`. After a reboot the control plane sees those VMs as suspended, and they resume when their owners come back.
5. In `/etc/libvirt/qemu.conf`, set `save_image_format` to a fast compressor that the installed libvirt lists as supported (`zstd` or `lzop` are good choices) and install that compressor. This cuts the size and time of every suspend.
6. Watch for AppArmor or SELinux denials. Disk images and backing files outside libvirt's default directories are a common cause of "permission denied" when starting a VM. Golden images must be readable by the user QEMU runs as.
7. Install the agent binary, `/etc/zynthera/agent.env` and `infra/systemd/zynthera-agent.service` (restart on failure, start after `libvirtd`).
8. Put Caddy in front of the agent: `https://<host name>.<domain>` proxies to `127.0.0.1:9090`, WebSockets included, with no request body limit on `/files/upload` beyond the agent's own.
9. Register the host in the admin area, copy the two secrets it shows into `agent.env`, start the agent and confirm the host shows as online.
10. Build or copy the golden images into `/var/lib/zynthera/images`.

---

## 13. Guest images

V1 images:

| Slug | Contents | Priority |
|---|---|---|
| `ubuntu-24.04-xfce` | Ubuntu 24.04 LTS with XFCE | Build first. The default |
| `debian-12-xfce` | Debian 12 with XFCE | Milestone 7 |
| A Fedora image with a lightweight desktop | | Only if time allows |

Seed all rows in `os_images`, with only the Ubuntu image enabled until the others exist. Suggested minimums: 2048 MB RAM and 40 GB disk.

Build with Packer's QEMU plugin, starting from the distribution's official cloud image (`disk_image = true`), feeding build-time cloud-init through a `cidata` CD, then running the scripts in `images/scripts/`. If Packer gets in the way, a script based on `virt-customize` and `virt-sysprep` is an acceptable replacement; note the choice in `docs/DECISIONS.md`.

Each image must contain:

- The full generic kernel. Cloud images often ship a trimmed kernel that lacks some graphics and input modules. Confirm the desktop renders on the virtio display.
- XFCE, LightDM, and a default resolution of 1920x1080
- `cloud-init`, left enabled so the per-VM seed is applied at first boot
- `qemu-guest-agent`, enabled
- `chrony`
- A web browser, file manager, terminal, text editor and archive tool
- Flatpak with the Flathub remote, so users have an app store
- `zynthera-file-bridge` in `/usr/local/bin` with its systemd unit installed but not enabled (cloud-init enables it once the user exists)
- Screen locking, screen blanking and all guest sleep and hibernate targets disabled. The web login already protects the session, and a locked or sleeping screen after a resume confuses people
- No leftover build user, no SSH server listening

Before sealing: clear the machine ID, SSH host keys, cloud-init state, shell history, package caches and logs, so every clone is unique. Convert the result to a compressed qcow2 with a small virtual size (around 16 GB); clones can grow but never shrink.

Versioning: images are named `<slug>/<version>.qcow2` with a date-based version such as `2026.11.1`. An image file is never modified or deleted while any VM was cloned from it, because qcow2 clones read from it for their whole life. A rebuilt image is a new version; `os_images.current_version` points new VMs at it.

`images/build.sh <slug>` builds one image and prints the path and version. Building needs KVM. In mock mode, write the templates and scripts and mark them untested.

---

## 14. File bridge

QEMU's VNC display has no file transfer, so each guest runs a small service.

`zynthera-file-bridge` is a static Go binary with no dependencies outside the standard library. It runs as the user's account under systemd, listens on `ZYN_BRIDGE_PORT`, and requires `Authorization: Bearer <bridge token>` on every request.

```text
GET  /v1/health
GET  /v1/list?path=<relative dir>        -> [{ name, type: "file" | "dir", size, modified }]
GET  /v1/download?path=<relative file>   -> the file, streamed, with Content-Disposition: attachment
PUT  /v1/upload?name=<file name>         -> saves the body to ~/Uploads/<name>
```

Rules:

- All paths are relative to the user's home directory. Clean the path, join it to the home directory, resolve symlinks, and reject the request unless the result is still inside the home directory. Cover this with tests for `..`, absolute paths, symlinks that point outside, and encoded separators.
- Upload names are a single path component. Strip directories, reject empty names and names starting with a dot, and when a file with that name exists save as `name (1).ext`.
- Uploads are written to a temporary file in the same directory and renamed when complete, so a failed upload leaves nothing half-written.
- Hidden files are listed only when the request asks for them.
- Other VMs cannot reach this port (port isolation), the internet cannot (NAT), and the token is required regardless.

---

## 15. Web app

### 15.1 Structure

Next.js App Router with TypeScript and Tailwind. `next.config` rewrites `/api/:path*` to `API_INTERNAL_URL`. Server state goes through TanStack Query. The event stream from `GET /api/events` updates the query cache so every page reflects VM state changes immediately.

Visual direction: dark by default with a light theme available, calm and technical, one accent color, generous spacing. Define colors, radii and spacing as tokens in the Tailwind config and use only those. The wordmark is the text "Zynthera"; no logo asset is needed.

### 15.2 Pages

| Route | Purpose |
|---|---|
| `/` | Landing page: what Zynthera is in a few lines, and a sign-up button |
| `/signup`, `/login` | Auth forms |
| `/verify-email` | Handles the emailed link, and offers to resend |
| `/forgot-password`, `/reset-password` | Password reset |
| `/dashboard` | The user's PCs as cards. With none, a prompt to create the first one |
| `/vms/new` | Creation wizard |
| `/vms/[id]` | Settings: rename, keep-alive, resize, snapshots, history, discard saved session, delete |
| `/vms/[id]/desktop` | The desktop viewer |
| `/admin`, `/admin/hosts`, `/admin/vms`, `/admin/users`, `/admin/usage` | Admin area, for the admin role only |

### 15.3 Dashboard and wizard

- A VM card shows name, OS, RAM, CPU, disk used out of disk size, a state badge, and the buttons from `allowed_actions`. While the state is transitional, buttons are disabled and the badge shows what is happening ("Resuming…", "Suspending…").
- For a VM in `error`, show `state_detail` in plain words with the recovery actions.
- Wizard steps: (1) choose the OS, (2) choose RAM, CPU and disk from the allowed values, with the user's remaining limits visible, (3) name the PC and set the Linux username and password, with a note that the password is needed for `sudo`, (4) review and create. After creating, go to the desktop viewer, which shows progress and then the first boot.
- If creation fails with `no_capacity`, say so plainly and keep the user's choices.

### 15.4 Desktop viewer

A full-window noVNC canvas with a small toolbar that hides itself.

State handling when the page opens and on every state event:

| VM state | Viewer shows |
|---|---|
| `running` | Requests a stream token and connects |
| `stopped` or `suspended` | Calls start, then shows "Starting your PC…" or "Resuming your PC…" |
| Transitional | Progress screen naming what is happening |
| `error` | The message and recovery buttons |
| Stream dropped while `running` | Reconnects with a fresh token: after 1 s, 2 s, 5 s, then every 10 s, up to 5 tries, then a "Reconnect" button |
| Closed with 4409 | "This PC was opened in another tab" with a "Use here" button |

Toolbar: fullscreen, send Ctrl+Alt+Del, clipboard text box, upload, download, suspend, shut down, connection indicator.

Details that matter:

- Load noVNC on the client only (dynamic import, no server rendering). The package has caused bundler trouble in some versions; if it does, vendor its ESM `core/` directory into the web app and note it in `docs/DECISIONS.md`.
- Scale the remote screen to fit the window (`scaleViewport`). Then try `resizeSession`, which asks the guest to change its resolution to match the window. Recent QEMU with a virtio display supports that. Keep it only if it works reliably.
- When the canvas has focus, keys go to the VM. In fullscreen, use the Keyboard Lock API where the browser supports it so Esc, Alt+Tab and similar keys reach the VM.
- Input idle check: after `ZYN_IDLE_INPUT_MINUTES` without mouse or keyboard input, show "Still there?" with a 60-second countdown, then disconnect. The normal idle rule then suspends the VM. Skip this when keep-alive is active.
- Uploads: a button and drag-and-drop onto the viewer, with a progress list. Downloads: a panel that browses the home directory through the files endpoints.
- Warn before closing the tab while an upload is in progress.

### 15.5 Settings page

- Rename.
- Keep-alive: "Keep running when I disconnect" for 1, 4, 8 or 12 hours (capped by `ZYN_MAX_KEEP_ALIVE_HOURS`), with the end time shown.
- Resize: only enabled when the PC is shut down with no saved session. Explain why when disabled. Disk can only grow.
- Snapshots: list, create, restore, delete. Restoring asks for confirmation and says that the current session will end and that everything changed since the snapshot will be lost.
- History: the event log.
- Discard saved session: shown when there is one, with a plain explanation of what is lost (open windows and unsaved work, not files).
- Delete: requires typing the PC's name.

### 15.6 Admin area

- Hosts: online status, last heartbeat, RAM, vCPU and disk bars (used against total), images present, admin state with a control to drain.
- Add host: a form, then a one-time display of the agent token and stream secret with copy buttons and the lines to paste into `agent.env`.
- VMs: a table with filters by state, host and user email, and suspend and force stop actions.
- Users: list, limits editor, disable.
- Usage: running hours per user and per size over a date range, with CSV export.

---

## 16. Security requirements

These are requirements. A platform that gives strangers root on a VM will be attacked.

1. **No shell injection.** Build commands as argument slices and never pass a string to a shell. Anything derived from user input (PC name, username, snapshot label) is validated against a strict pattern. Names on disk and in libvirt are always our UUIDs.
2. **Structured rendering.** cloud-init YAML and libvirt XML are produced by serializers from structs, never by pasting values into text.
3. **Nothing in a VM is reachable from outside the host.** The display, the guest agent and the file bridge are reached only through the agent, with a signed, short-lived, purpose-bound token.
4. **The agent trusts two callers.** The control plane with the agent token, and browsers with a signed token from the allowed origin. The agent listens on loopback, behind Caddy. In production, also firewall the control-plane routes to the control plane's address.
5. **Ownership checks on every VM route,** returning 404 for VMs that belong to someone else.
6. **Secrets.** They live in `.env` or `/etc/zynthera/agent.env`, never in the repository. Host credentials in the database are encrypted with `ZYN_SECRETS_KEY`. Never log passwords, tokens, cookies, cloud-init contents or URLs that contain tokens. In production, refuse to start with placeholder secrets.
7. **Passwords.** Account passwords use argon2id. Linux passwords are hashed in the request handler and the plain text is discarded; the hash is cleared from the database after provisioning.
8. **Sessions and CSRF.** `HttpOnly`, `Secure`, `SameSite=Lax` cookies, Origin checks on unsafe methods, session rotation on login, all sessions revoked on password reset.
9. **Rate limits** as listed in section 10.2.
10. **Network isolation** as described in section 12.7: no VM to VM traffic, no VM to host traffic beyond DHCP and DNS, no outbound port 25, no access to private ranges, a bandwidth cap per VM.
11. **Resource limits.** RAM and vCPUs are fixed by KVM. Per-user limits are enforced by the API. Request bodies are size-limited everywhere except the upload route, which has its own limit.
12. **Web hardening.** A Content Security Policy, `X-Content-Type-Options`, `Referrer-Policy`, frame-ancestors denied. User-supplied text is only ever rendered as text.
13. **Deletion is real.** Deleting a VM removes its disk, saved memory image, seed, snapshots and network reservation from the host.
14. **Audit trail.** Every state change, admin action and login is recorded.

---

## 17. Testing strategy

| Layer | What is tested | How |
|---|---|---|
| Pure logic | State machine, scheduler arithmetic, validation, token signing, path safety, IP allocation, XML and cloud-init rendering | Table-driven Go unit tests and golden files |
| API | Auth flows, VM endpoints, ownership, races, limits, rate limits | Go integration tests against the `zynthera_test` database and Redis from Compose, with the mock driver and a fake mailer |
| Driver | Full lifecycle and idempotency | The conformance suite: mock always, libvirt when `ZYN_TEST_LIBVIRT=1` |
| Stream proxy | Token checks, origin check, single viewer, byte piping | Go tests with a fake VNC TCP server |
| Web | Forms, state-driven UI logic | Unit tests for logic; Playwright end-to-end tests in mock mode |
| Whole system on a real host | Section 19 | `scripts/acceptance.sh` plus the manual steps it cannot automate |

Rules:

- New logic comes with tests in the same commit.
- Tests never need the internet and never need KVM unless explicitly gated.
- A bug that reaches the user gets a regression test with its fix.

---

## 18. Implementation plan

Copy this checklist into `docs/PROGRESS.md` and tick tasks as they are finished. Each milestone ends with a "Done when" check that you must actually run.

Every task is finished only when: the code builds, `make lint` and `make test` pass, new logic has tests, and any document it affects is updated.

### Milestone 0: Foundations

Goal: an empty but fully wired project that anyone can start with five commands.

- [ ] **M0.1** Run the environment check from section 2. Record the result and the chosen mode at the top of `docs/PROGRESS.md`.
- [ ] **M0.2** `git init`, `.gitignore` (`.env`, `bin/`, `.data/`, `node_modules/`, `.next/`), `.editorconfig`, `README.md` with the quick start, and the directory layout from section 6.
- [ ] **M0.3** `.env.example` with every variable from section 8, each with a comment.
- [ ] **M0.4** `infra/docker-compose.yml` as in section 7.1, including the script that creates `zynthera_test`. `make up`, `make down`, `make nuke`.
- [ ] **M0.5** Go module, `internal/shared/config` and `internal/shared/logging`, and `cmd/api` with the `serve`, `migrate` and `seed` subcommands (seed does nothing yet).
- [ ] **M0.6** API HTTP skeleton: chi router, the middleware stack from section 10.1, error envelope helpers, `GET /health` checking Postgres and Redis, graceful shutdown.
- [ ] **M0.7** Database plumbing: pgx pool, golang-migrate behind `zynthera-api migrate`, a first migration that enables `citext`, `db/sqlc.yaml` and `make generate`.
- [ ] **M0.8** `cmd/agent` skeleton: config, logging, `GET /health`, graceful shutdown.
- [ ] **M0.9** Web skeleton: Next.js with strict TypeScript, Tailwind with the design tokens, base layout with the Zynthera wordmark, the `/api` rewrite, and a placeholder landing page that shows whether the API is healthy.
- [ ] **M0.10** `make dev` (runs API, agent and web with prefixed logs), `make test`, `make lint`, `make build`.
- [ ] **M0.11** CI workflow that runs lint and tests with Postgres and Redis service containers.

**Done when:** on a fresh clone, `cp .env.example .env && make up && make migrate && make seed && make dev` gives a page at `http://localhost:3000` that reports the API as healthy, and `make test` and `make lint` pass.

### Milestone 1: Accounts

Goal: people can sign up, verify their email, log in and reset a password.

- [ ] **M1.1** Migration for `users`, `sessions`, `email_tokens`, with sqlc queries.
- [ ] **M1.2** Password hashing package (argon2id, PHC string format) with tests.
- [ ] **M1.3** Sessions: issue, look up, rotate, revoke. Session middleware, cookie settings by environment, Origin check middleware.
- [ ] **M1.4** Redis rate limiter middleware with the limits from section 10.2.
- [ ] **M1.5** Mail package: SMTP sender, verification and reset emails in text and HTML, links built from `ZYN_WEB_ORIGIN`, and a fake sender for tests.
- [ ] **M1.6** Handlers: signup, login, logout, me, verify-email, resend-verification, forgot-password, reset-password.
- [ ] **M1.7** Seed the admin user.
- [ ] **M1.8** Web: API client (a fetch wrapper that parses the error envelope), auth pages with zod validation, session-aware layout, redirects (logged out goes to `/login`, logged in is kept away from auth pages), and a "verify your email" banner.
- [ ] **M1.9** Web: an empty dashboard and a header with the user menu and logout.
- [ ] **M1.10** Integration tests: signup to verify to login to logout; the reset flow; rate limiting; identical responses for known and unknown emails; rejected requests with a wrong Origin.

**Done when:** a new user can sign up, open the email in Mailpit at `http://localhost:8025`, verify, log in, see an empty dashboard and log out, and the seeded admin can log in.

### Milestone 2: Host agent with the mock driver

Goal: the control plane knows about a host and can drive fake VMs on it.

- [ ] **M2.1** `internal/shared/vmtypes` from section 11.2.
- [ ] **M2.2** The `Driver` and `Storage` interfaces and the driver conformance test suite.
- [ ] **M2.3** Mock driver with persistence, delays and failure injection.
- [ ] **M2.4** `infra/dev-desktop` Dockerfile and its Compose service. Confirm a VNC client can connect to `127.0.0.1:5901` and sees an XFCE desktop.
- [ ] **M2.5** Agent HTTP API for the control plane: bearer auth, UUID validation, per-VM mutex, error envelope.
- [ ] **M2.6** Migration for `hosts`. `internal/api/secrets` (AES-GCM) with tests.
- [ ] **M2.7** `internal/api/agentclient`: a typed client with timeouts and error mapping.
- [ ] **M2.8** Heartbeats: the agent loop, the `/internal/agent/heartbeat` handler, and the "online" calculation.
- [ ] **M2.9** Admin middleware and the host endpoints: list, create (secrets shown once), patch.
- [ ] **M2.10** Seed the development host `local` from `.env`.
- [ ] **M2.11** Tests: the conformance suite against the mock driver, agent auth, and a heartbeat integration test.

**Done when:** with `make dev` running, `GET /api/admin/hosts` shows `local` as online with capacity; `curl` with the agent token can create, start, suspend and delete a mock VM; the same calls without the token get 401.

### Milestone 3: VM lifecycle in the control plane

Goal: the full lifecycle works through the API, with correct state handling under failure and concurrency.

- [ ] **M3.1** Migrations for `os_images`, `vms`, `vm_events`, `snapshots`, `usage_intervals`. Seed the image rows.
- [ ] **M3.2** State machine package with table-driven tests covering every action in every state.
- [ ] **M3.3** asynq wiring: client, server, the three queues, task constructors, logging and panic-recovery middleware, periodic task registration.
- [ ] **M3.4** Scheduler package: host choice and capacity check under a host row lock, with a test that fires concurrent starts at a nearly full host.
- [ ] **M3.5** Validation and limits. `GET /api/images` and `GET /api/options`.
- [ ] **M3.6** VM handlers: list, get, create, patch (rename and keep-alive), delete, and the action endpoints, all using the compare-and-set update.
- [ ] **M3.7** Job handlers: provision, start, suspend, shutdown, force stop, reboot, discard saved state, resize, delete.
- [ ] **M3.8** Events and usage intervals written in the same transaction as each state change.
- [ ] **M3.9** Live updates: publish to Redis on every change, `GET /api/events` as Server-Sent Events.
- [ ] **M3.10** Snapshots: endpoints, jobs and the per-VM limit, against the mock driver.
- [ ] **M3.11** Periodic tasks: reconcile, stuck VMs, idle sweep (using `last_active_at` only until milestone 4 adds viewer tracking), session cleanup.
- [ ] **M3.12** An integration test that runs the whole lifecycle against the mock driver: create, start, suspend, resume, snapshot, restore, resize, delete; a forced failure that lands in `error` and recovers; two simultaneous start requests where exactly one wins; a start refused for lack of capacity.

**Done when:** the lifecycle test passes, and the database shows the expected events and usage intervals afterwards.

### Milestone 4: Desktop in the browser

Goal: the complete user journey works in mock mode.

- [ ] **M4.1** `internal/shared/token`: sign and verify, with tests for expiry, tampering, wrong purpose and replay.
- [ ] **M4.2** `POST /api/vms/:id/stream-token`.
- [ ] **M4.3** Agent stream proxy as in section 11.7, tested with a fake VNC TCP server.
- [ ] **M4.4** Stream events to the API, `viewer_connected` and `last_active_at`, and the idle sweep using them.
- [ ] **M4.5** Web data layer for VMs: queries, mutations, and the event stream feeding the query cache.
- [ ] **M4.6** Web: dashboard with VM cards and actions.
- [ ] **M4.7** Web: creation wizard.
- [ ] **M4.8** Web: desktop viewer with noVNC, the state table from section 15.4, toolbar (file buttons arrive in milestone 6), reconnect, fullscreen with keyboard lock, and the input idle prompt.
- [ ] **M4.9** Web: settings page with rename, keep-alive, history and delete.
- [ ] **M4.10** Logout suspends the user's VMs, and the logout confirmation says so.
- [ ] **M4.11** Playwright end-to-end test in mock mode: sign up, verify (read the link from Mailpit's API), create a PC, see the desktop canvas receive frames, log out, confirm the VM is suspended, log in, resume, connected again.

**Done when:** in mock mode, a new user can go from sign-up to moving the mouse on a desktop in the browser, log out, log in again, see the PC as suspended, and resume it.

### Milestone 5: Real virtual machines

Goal: the same journey on real KVM VMs, with real suspend and resume. Needs a KVM host. Without one, write and unit-test everything, mark the host-dependent tasks "written, not tested on real hardware", and tell the user exactly which commands to run on a server.

- [ ] **M5.1** `infra/host-setup/`: idempotent scripts for packages, directories, the network, the firewall, `libvirt-guests` and `qemu.conf`. `docs/DEPLOY_HOST.md`.
- [ ] **M5.2** `internal/agent/netalloc`: MAC and IP reservation under a lock, with tests.
- [ ] **M5.3** qcow2 storage driver, tested with the real `qemu-img` when it is installed.
- [ ] **M5.4** `internal/agent/cloudinit`: rendering and seed building, with golden-file tests.
- [ ] **M5.5** `internal/agent/domainxml`: the builder, golden files, and `virt-xml-validate` in tests when available.
- [ ] **M5.6** libvirt driver: create, start, suspend, shutdown, force stop, reboot, discard, delete, status, list, display, clock sync, capacity, images.
- [ ] **M5.7** libvirt driver: resize and snapshots.
- [ ] **M5.8** The Ubuntu image: Packer template, scripts, `images/build.sh`. Build it and boot it by hand with `virsh` before wiring it in.
- [ ] **M5.9** `infra/systemd/zynthera-agent.service` and `infra/caddy/host.Caddyfile`.
- [ ] **M5.10** Run the conformance suite with `ZYN_TEST_LIBVIRT=1`.
- [ ] **M5.11** `scripts/acceptance.sh`: automates every step of section 19 that can be automated, including host-side checks such as "the QEMU process is gone".
- [ ] **M5.12** Measure and record in `docs/PROGRESS.md`: creation time, suspend and resume times for a 4 GB and a 16 GB VM, and saved image sizes.

**Done when:** steps 1 to 7 and 10 to 12 of section 19 pass on a KVM host.

### Milestone 6: Files and snapshots

Goal: users can move files in and out, and manage snapshots from the UI.

- [ ] **M6.1** `cmd/file-bridge` and `internal/bridge`, with the path safety tests from section 14.
- [ ] **M6.2** Bake the bridge into the image (binary and unit file) and wire it through cloud-init. Rebuild the image as a new version.
- [ ] **M6.3** `POST /api/vms/:id/files-token`.
- [ ] **M6.4** Agent file proxy: list, upload, download, CORS, size limit, streaming. The mock driver's equivalent against a temp directory.
- [ ] **M6.5** Web: upload button, drag-and-drop, progress and errors; the download panel.
- [ ] **M6.6** Web: snapshots, resize and "discard saved session" in the settings page.
- [ ] **M6.7** Tests: traversal attempts through the whole chain, an upload over the limit, and an end-to-end upload and download in mock mode.

**Done when:** steps 8 and 9 of section 19 pass.

### Milestone 7: Admin, hardening and production readiness

Goal: something that can be run for real users.

- [ ] **M7.1** Remaining admin endpoints: VMs, users, usage.
- [ ] **M7.2** Admin UI from section 15.6.
- [ ] **M7.3** Host draining: no new VMs on a draining host, existing ones still start.
- [ ] **M7.4** ZFS storage driver, passing the conformance suite.
- [ ] **M7.5** The Debian image.
- [ ] **M7.6** Production packaging: Dockerfiles for the API and the web app, a production Compose file, `infra/caddy/control-plane.Caddyfile`, `docs/DEPLOY_CONTROL_PLANE.md`.
- [ ] **M7.7** Backups, in `docs/OPERATIONS.md` with scripts: a nightly `pg_dump`, and VM disk backups of suspended and stopped VMs to S3-compatible storage with a tested restore procedure. The storage bucket comes from the user, so keep it configurable.
- [ ] **M7.8** Metrics endpoints in Prometheus format on the API and the agent: job counts and durations, VMs by state, host capacity, open streams.
- [ ] **M7.9** Review the code against every item in section 16. Fix what you find and write the result to `docs/SECURITY_REVIEW.md`.
- [ ] **M7.10** Load test script: create, suspend and resume many VMs in parallel. Record timings and failures.
- [ ] **M7.11** Account deletion: a user can delete their account, which deletes their VMs and data.

**Done when:** all of section 19 passes, and a second host (a second mock agent is enough) can be registered and receives new VMs while the first is draining.

### Milestone 8 (optional): smoother streaming with sound

Goal: a WebRTC stream with audio, keeping VNC as the fallback.

- [ ] **M8.1** Read the current Selkies documentation. Write a short design note in `docs/DECISIONS.md` covering what runs in the guest, how signaling reaches it through the agent, and how TURN is used. Do not rely on memory for how Selkies is set up.
- [ ] **M8.2** A TURN server (coturn) in Compose and on the host, with short-lived credentials issued by the API.
- [ ] **M8.3** An image variant with the streaming server installed, and the agent's signaling proxy.
- [ ] **M8.4** The viewer uses WebRTC when the image supports it and falls back to VNC otherwise.

---

## 19. V1 acceptance test

V1 is finished when all of this works on a real KVM host.

1. A new user signs up, verifies their email and logs in.
2. They create an Ubuntu PC with 4 GB RAM, 2 vCPUs and an 80 GB disk. Within about two minutes they are looking at a desktop in the browser.
3. Inside the VM, `free -h`, `nproc` and `df -h /` report the chosen sizes.
4. They install a package with `sudo apt install`, download a file with the browser, open a text editor and type a note without saving it.
5. They log out of the website. Within a minute the VM is `suspended` and its QEMU process is gone from the host.
6. They log in and open the desktop. Within about 30 seconds the same screen is back, the unsaved note is still open, and `date` in the VM shows the correct time.
7. The host is rebooted. Afterwards the VM shows as suspended, and resuming it brings back the same session.
8. They upload a file from their computer and find it in `~/Uploads`. They download a file out of the VM.
9. They take a snapshot, delete a file, restore the snapshot, start the PC, and the file is back.
10. A second user cannot see the first user's VM, gets 404 for its ID on every route, and cannot obtain a stream token for it. From inside their own VM they cannot reach the first user's VM, the host's SSH port or the agent.
11. With the host's RAM fully allocated, another start request fails with a clear capacity message.
12. They delete the PC. Its directory, saved memory image, libvirt domain and DHCP reservation are gone from the host.

---

## 20. Production layout

The first real deployment, described fully in `docs/DEPLOY_CONTROL_PLANE.md` and `docs/DEPLOY_HOST.md`:

- **Control plane machine** (a small VPS is enough; it can also be the VM host at the very start): Caddy, the web container, the API container, Postgres and Redis from the production Compose file. Postgres and Redis are not exposed to the internet.
- **Routing on `app.<domain>`:** `/api/*` and `/internal/*` go to the API, everything else to the web app. `/internal/*` is additionally limited to the VM hosts' addresses.
- **Each VM host:** `<host name>.<domain>` terminates TLS in Caddy and proxies to the agent on loopback. Browsers connect here for the stream and for file transfer. The control plane calls the same address for agent operations.
- **Agent configuration:** `ZYN_AGENT_API_URL=https://app.<domain>` and `ZYN_AGENT_WEB_ORIGIN=https://app.<domain>`.
- **Email:** a real SMTP provider in place of Mailpit.
- **Secrets:** generated per environment. `ZYN_SECRETS_KEY` is backed up separately from the database, since losing it makes stored host credentials unreadable.
- **Latency:** the VM host should be in a region close to the users. The stream goes straight from the browser to the host, so the control plane's location matters less.

---

## 21. How to work

- Keep `docs/PROGRESS.md` current. It is how the user, and you in a later session, know where things stand. At the start of every session, read it and this spec before doing anything else.
- Commit at the end of every task with a message that names the task ID, for example `M3.4 scheduler: capacity check under host lock`.
- Record every choice that departs from this spec in `docs/DECISIONS.md` with one or two sentences of reasoning. If you find a better approach than what is written here, use it and say why. The goals in sections 1 and 19 matter more than the specific tools.
- When a command needs root or changes host networking or firewall rules, tell the user what it does before running it.
- If you cannot verify something, for example anything that needs KVM on a machine without it, say so plainly in the summary and in `docs/PROGRESS.md`. Never report something as working that you have not run.
- When a tool behaves differently from what this document describes, trust the tool's current documentation over this document, fix the approach and note it in `docs/DECISIONS.md`.
- Ask the user when a decision is theirs to make: spending money, choosing a hosting provider or domain, anything that affects real user data. Otherwise keep going.
