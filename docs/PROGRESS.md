# Zynthera build progress

Read this file and `PROJECT_SPEC.md` at the start of every session.

## Current status

- **Done:** Milestone 0 (foundations), Milestone 1 (accounts), Milestone 2 (host agent with
  the mock driver).
- **Also done:** Milestone 3 (VM lifecycle in the control plane) and Milestone 4 (desktop in
  the browser). The whole product flow works in mock mode.
- **In progress:** Milestone 5 (real VMs) and Milestone 6 (files and snapshots), worked on
  together.
  - Done and tested on this machine's KVM: netalloc, qcow2 storage, cloud-init seed, domain
    XML, the libvirt driver (it passes the driver conformance suite against real VMs), the
    `zynthera` libvirt network.
  - Done and tested in mock mode: file bridge, files-token, agent file proxy, upload/download
    panel in the viewer, snapshots and resize on the settings page.
  - Running: the first build of the Ubuntu image (`images/build.sh ubuntu-24.04-xfce 2026.11.1`
    into `/var/tmp/zynthera-dev/images`).
- **Next:** when the image exists: boot it by hand, then run the product with
  `ZYN_VM_DRIVER=libvirt` and `ZYN_DATA_DIR=/var/tmp/zynthera-dev`, run the conformance suite
  with `ZYN_TEST_LIBVIRT_IMAGE`, measure timings (M5.12), write the host setup scripts
  (M5.1), systemd unit and Caddyfile (M5.9), `scripts/acceptance.sh` (M5.11).
- **How to reach libvirt in this session:** the user is in the `libvirt` group but this login
  session predates that, so commands are run as `sg libvirt -c "..."`. After a logout/login
  plain `virsh` works.

## Environment check (M0.1, 2026-11-19)

| Check | Result |
|---|---|
| OS | Linux Mint 22.2 (Ubuntu 24.04 base), kernel 7.0, x86_64 |
| KVM | `/dev/kvm` present and usable, 16 CPU threads with virtualization |
| Docker / Compose | 29.10.0 / v5.8.0, running |
| Go | 1.27.4 (installed to `~/.local/go`) |
| Node.js | 24.11.1 |
| libvirt | 10.0.0, service active. User added to `libvirt` and `kvm` groups; needs one logout/login before `virsh` works without sudo |
| Other tools | qemu-img, cloud-localds, virt-customize, virt-xml-validate, Packer 1.16.2 + QEMU plugin, sqlc 1.31.1, golangci-lint 2.15.0 |
| RAM | 15 GB total, about 5 GB free: enough for 1 or 2 small test VMs |
| Disk | 416 GB free |
| Not installed | ZFS (needed only for M7.4), Caddy (production only) |

Local quirk: this machine already runs its own Postgres on 5432 and Redis on 6379, so the
local `.env` uses `POSTGRES_PORT=5433` and `REDIS_PORT=6380`. `.env.example` keeps the
standard ports.

## What works now and how to try it

```text
cp .env.example .env     # on this machine: also switch the two ports as noted above
make up && make migrate && make seed && make dev
```

Open http://localhost:3000. The landing page shows "API is healthy".
Sign up, open the confirmation email at http://localhost:8025 (Mailpit), confirm, log in.
The seeded admin is `admin@zynthera.local` with the password from `ZYN_SEED_ADMIN_PASSWORD`.
`make test`, `make lint`, `make build` and `make test-e2e` pass.

## Verified at the end of milestone 0

- `make up` starts Postgres 18, Redis 8, Adminer and Mailpit; `zynthera_test` is created.
- `make migrate` reaches schema version 1 (citext enabled). `make seed` runs (nothing to seed yet).
- `make dev` serves the web app on 3000, the API on 8080 and the agent on 9090; stopping it leaves no process behind.
- `/api/health` through the web rewrite reports Postgres and Redis as ok.
- A POST with a foreign `Origin` is refused with 403 and the error envelope.
- Not yet verified: the CI workflow (there is no Git remote to run it on), and `make up`
  with the mock desktop image (built in M2.4).

## Verified at the end of milestone 1

- 20 Go integration tests against the real test database and Redis: sign-up, verify, login,
  logout, password reset, single-use and expired tokens, session rotation, disabled users,
  identical answers for known and unknown emails, wrong-Origin rejection, rate limits.
- 4 Playwright browser tests against the running stack: the full sign-up journey with the
  email read from Mailpit, password reset by email, the generic wrong-password message, and
  the seeded admin logging in.
- Every page loaded logged-out and logged-in with no browser console errors.
- Not yet done from spec section 16.14 (audit trail): logins are recorded in the structured
  log and in the `sessions` table (IP, user agent), not yet in a dedicated audit table.
  Planned with the security review in M7.9.

## Verified at the end of milestone 2

- Driver conformance suite (14 groups) passes against the mock driver.
- Agent API tests: token required on every control-plane route, UUID and body validation,
  error mapping, and "409 busy" for a second operation on a VM that is being worked on.
- API tests: admin-only routes, host secrets shown once and stored encrypted, heartbeat
  authentication per host, online/offline calculation, host patch.
- One test runs a real agent (mock driver) against the real API: heartbeat, then
  create, start, suspend, status, delete through the control plane's agent client.
- By hand with `make dev`: `GET /api/admin/hosts` shows `local` online with 32768 MB RAM,
  16 vCPUs, 500 GB disk and two images; `curl` with the agent token creates, starts,
  suspends and deletes a mock VM; the same calls without the token get 401.
- The mock desktop container answers a VNC handshake on 127.0.0.1:5901 at 1920x1080 with an
  XFCE session running. Seeing the picture in a viewer comes with the browser viewer in M4.
- Not built yet: the heartbeat's VM list is received but not stored; the reconciler that
  needs it arrives in M3.11, together with `disk_used_bytes` and `viewer_connected`.

## Verified at the end of milestone 3

- State machine: every action in every state, with and without a saved session.
- Scheduler arithmetic: RAM never overcommitted, vCPU overcommit factor, disk headroom,
  placement rules, per-user limits.
- 15 API integration tests with real Postgres, Redis, job workers and a real agent on the
  mock driver. `TestVMLifecycle` runs create, suspend, resume, reboot, snapshot (of a running
  PC), shutdown, restore, resize, rename, keep-alive, start and delete, and checks the events
  and usage intervals in the database after each step.
- Failure paths: a failing suspend lands in `error` with a readable message and recovers by
  force stop and start; a failing provision can be deleted; a guest that ignores shutdown
  returns to `running` with a "use Force stop" hint.
- Concurrency: 8 simultaneous starts of one PC, exactly one accepted; 4 simultaneous starts
  on a host with room for one, exactly one accepted and three `no_capacity`.
- Isolation: a second user gets 404 on all 14 VM and snapshot routes, identical to an id
  that does not exist.
- Periodic tasks run directly in tests: reconcile (needs two agreeing heartbeats), idle
  sweep (respects keep-alive), plus logout suspend.
- Live updates: the event stream delivers the current list, then each state change, and
  never another user's PCs.
- By hand with `make dev`: a PC went from create to `running` in 8.3 s (the mock driver's
  5 s + 3 s delays), and the event stream through the web app's `/api` rewrite showed
  provisioning, starting, running, suspending, suspended as they happened.
- A deadlock found by the concurrency test was fixed (docs/DECISIONS.md D-021).
- Not run yet: the hourly session cleanup task and the stuck-VM check have no test of
  their own (the stuck-VM check shares `settleFromHost` with the tested failure paths).

## Verified at the end of milestone 4

- Token package: expiry, tampering in ten variants, wrong purpose, replay, forgetting of
  expired ids.
- Stream proxy against a fake VNC server (with the race detector): bytes both ways, every
  token and origin failure, single use, second viewer takes over with code 4409, stream
  closes with 4404 when the PC stops or its display goes away.
- API: stream token only for the owner and only while running; stream events authenticated
  per host; a PC with a viewer is never suspended as idle; suspending closes the stream.
- One Playwright test walks the whole journey in a real browser against `make dev` with the
  mock desktop: sign up, verify by email, wizard (with validation), progress screen, a real
  XFCE desktop on the canvas (checked by counting colours), mouse and keyboard input, the
  dashboard card, the logout warning, the PC suspended after logout, resume, a second tab
  taking over and "Use here", rename, history and delete. It takes about 25 seconds.
- Two bugs found by that test and fixed: live updates did not start after logging in without
  a page reload, and the viewer toolbar could not be opened without a mouse.
- Looked at by hand: dashboard and settings on desktop and phone widths; no browser console
  errors.
- Not exercised by any test yet: fullscreen with keyboard lock, the clipboard panel,
  Ctrl+Alt+Del, the "Still there?" idle prompt (its timing logic runs, but no test waits an
  hour), and the automatic reconnect after a network drop.
- `resizeSession` (the PC changes its resolution to fit the window) works with the mock
  desktop's VNC server. Whether QEMU's display supports it is to be checked in milestone 5.

## Verified so far in milestones 5 and 6

- `sg libvirt -c "ZYN_TEST_LIBVIRT=1 go test ./internal/agent/driver/libvirt"`: the
  conformance suite passes against real KVM domains (start, managed save, resume, force stop,
  reboot, resize, internal snapshots, delete), using a blank disk image. The clean-shutdown
  check is skipped until a bootable image exists.
- `TestDeleteLeavesNothingBehind`: after delete, no VM directory, no libvirt domain, no
  DHCP entry, no address reservation, no saved memory image; while suspended there is no
  QEMU process.
- Finding (spec 12.5 asked): with QEMU 8.2.2, `qemu-img resize` works on a disk that has
  internal snapshots, so resize does not require deleting snapshots first.
- Finding: libvirt changes the owner of a golden image file once a VM cloned from it has
  run. The image stays readable. In production the agent runs as root, so this has no effect.
- File transfer in mock mode, API to agent to bridge: upload, list, download, seven
  traversal attempts, upload over the limit, wrong token purpose, wrong origin, another
  user's token, CORS preflight, refused when the PC is not running.
- Browser tests (7 in total now): upload twice (no overwrite), browse, download and compare
  content; snapshot of a running PC, restore after shutdown, resize.
- Two more bugs found by browser tests and fixed: the history and snapshot lists did not
  refresh after an operation that ends in the state it started in, and the live update
  after a resize carried the old size.
- Not yet verified on a real VM: cloud-init applying the seed, the desktop over QEMU's VNC,
  the file bridge inside a guest, the guest agent (clean shutdown, clock sync), network
  isolation. All of these need the image.

## Deployment milestone (added to the spec, see docs/DECISIONS.md D-001)

Goal: a fresh Ubuntu server becomes a working Zynthera with one command.

- [ ] **D.1** Production Dockerfiles for the API and the web app, and `infra/docker-compose.prod.yml` with Caddy and automatic HTTPS. Build after milestone 4. (Replaces M7.6.)
- [ ] **D.2** `scripts/install.sh --domain <domain> --email <email>`: installs Docker, KVM, libvirt and Caddy, generates every secret, starts the control plane, migrates and seeds, installs the agent as a systemd service, registers the host by itself (no copying of secrets) and fetches the OS image. Safe to run again. Build after milestone 5.
- [ ] **D.3** `scripts/add-host.sh`: joins a second VM host using a one-time join token from the admin area.
- [ ] **D.4** `scripts/doctor.sh`: checks every part (services, KVM, network, firewall, image, TLS, heartbeat) and prints what is wrong and how to fix it.
- [ ] **D.5** `scripts/update.sh`: pulls a new version, migrates and restarts the control plane and the agent without touching running VMs.
- [ ] **D.6** `docs/DEPLOY_QUICKSTART.md`: the whole deployment on one page.

**Done when:** on a fresh Ubuntu Server LTS machine with a domain pointed at it, `install.sh` alone leads to a working sign-up and a real desktop in the browser, and `doctor.sh` reports all green.

## Task checklist (from PROJECT_SPEC.md section 18)

### Milestone 0: Foundations

Goal: an empty but fully wired project that anyone can start with five commands.

- [x] **M0.1** Run the environment check from section 2. Record the result and the chosen mode at the top of `docs/PROGRESS.md`.
- [x] **M0.2** `git init`, `.gitignore` (`.env`, `bin/`, `.data/`, `node_modules/`, `.next/`), `.editorconfig`, `README.md` with the quick start, and the directory layout from section 6.
- [x] **M0.3** `.env.example` with every variable from section 8, each with a comment.
- [x] **M0.4** `infra/docker-compose.yml` as in section 7.1, including the script that creates `zynthera_test`. `make up`, `make down`, `make nuke`.
- [x] **M0.5** Go module, `internal/shared/config` and `internal/shared/logging`, and `cmd/api` with the `serve`, `migrate` and `seed` subcommands (seed does nothing yet).
- [x] **M0.6** API HTTP skeleton: chi router, the middleware stack from section 10.1, error envelope helpers, `GET /health` checking Postgres and Redis, graceful shutdown.
- [x] **M0.7** Database plumbing: pgx pool, golang-migrate behind `zynthera-api migrate`, a first migration that enables `citext`, `db/sqlc.yaml` and `make generate`.
- [x] **M0.8** `cmd/agent` skeleton: config, logging, `GET /health`, graceful shutdown.
- [x] **M0.9** Web skeleton: Next.js with strict TypeScript, Tailwind with the design tokens, base layout with the Zynthera wordmark, the `/api` rewrite, and a placeholder landing page that shows whether the API is healthy.
- [x] **M0.10** `make dev` (runs API, agent and web with prefixed logs), `make test`, `make lint`, `make build`.
- [x] **M0.11** CI workflow that runs lint and tests with Postgres and Redis service containers.

**Done when:** on a fresh clone, `cp .env.example .env && make up && make migrate && make seed && make dev` gives a page at `http://localhost:3000` that reports the API as healthy, and `make test` and `make lint` pass.

### Milestone 1: Accounts

Goal: people can sign up, verify their email, log in and reset a password.

- [x] **M1.1** Migration for `users`, `sessions`, `email_tokens`, with sqlc queries.
- [x] **M1.2** Password hashing package (argon2id, PHC string format) with tests.
- [x] **M1.3** Sessions: issue, look up, rotate, revoke. Session middleware, cookie settings by environment, Origin check middleware.
- [x] **M1.4** Redis rate limiter middleware with the limits from section 10.2.
- [x] **M1.5** Mail package: SMTP sender, verification and reset emails in text and HTML, links built from `ZYN_WEB_ORIGIN`, and a fake sender for tests.
- [x] **M1.6** Handlers: signup, login, logout, me, verify-email, resend-verification, forgot-password, reset-password.
- [x] **M1.7** Seed the admin user.
- [x] **M1.8** Web: API client (a fetch wrapper that parses the error envelope), auth pages with zod validation, session-aware layout, redirects (logged out goes to `/login`, logged in is kept away from auth pages), and a "verify your email" banner.
- [x] **M1.9** Web: an empty dashboard and a header with the user menu and logout.
- [x] **M1.10** Integration tests: signup to verify to login to logout; the reset flow; rate limiting; identical responses for known and unknown emails; rejected requests with a wrong Origin.

**Done when:** a new user can sign up, open the email in Mailpit at `http://localhost:8025`, verify, log in, see an empty dashboard and log out, and the seeded admin can log in.

### Milestone 2: Host agent with the mock driver

Goal: the control plane knows about a host and can drive fake VMs on it.

- [x] **M2.1** `internal/shared/vmtypes` from section 11.2.
- [x] **M2.2** The `Driver` and `Storage` interfaces and the driver conformance test suite.
- [x] **M2.3** Mock driver with persistence, delays and failure injection.
- [x] **M2.4** `infra/dev-desktop` Dockerfile and its Compose service. Confirm a VNC client can connect to `127.0.0.1:5901` and sees an XFCE desktop.
- [x] **M2.5** Agent HTTP API for the control plane: bearer auth, UUID validation, per-VM mutex, error envelope.
- [x] **M2.6** Migration for `hosts`. `internal/api/secrets` (AES-GCM) with tests.
- [x] **M2.7** `internal/api/agentclient`: a typed client with timeouts and error mapping.
- [x] **M2.8** Heartbeats: the agent loop, the `/internal/agent/heartbeat` handler, and the "online" calculation.
- [x] **M2.9** Admin middleware and the host endpoints: list, create (secrets shown once), patch.
- [x] **M2.10** Seed the development host `local` from `.env`.
- [x] **M2.11** Tests: the conformance suite against the mock driver, agent auth, and a heartbeat integration test.

**Done when:** with `make dev` running, `GET /api/admin/hosts` shows `local` as online with capacity; `curl` with the agent token can create, start, suspend and delete a mock VM; the same calls without the token get 401.

### Milestone 3: VM lifecycle in the control plane

Goal: the full lifecycle works through the API, with correct state handling under failure and concurrency.

- [x] **M3.1** Migrations for `os_images`, `vms`, `vm_events`, `snapshots`, `usage_intervals`. Seed the image rows.
- [x] **M3.2** State machine package with table-driven tests covering every action in every state.
- [x] **M3.3** asynq wiring: client, server, the three queues, task constructors, logging and panic-recovery middleware, periodic task registration.
- [x] **M3.4** Scheduler package: host choice and capacity check under a host row lock, with a test that fires concurrent starts at a nearly full host.
- [x] **M3.5** Validation and limits. `GET /api/images` and `GET /api/options`.
- [x] **M3.6** VM handlers: list, get, create, patch (rename and keep-alive), delete, and the action endpoints, all using the compare-and-set update.
- [x] **M3.7** Job handlers: provision, start, suspend, shutdown, force stop, reboot, discard saved state, resize, delete.
- [x] **M3.8** Events and usage intervals written in the same transaction as each state change.
- [x] **M3.9** Live updates: publish to Redis on every change, `GET /api/events` as Server-Sent Events.
- [x] **M3.10** Snapshots: endpoints, jobs and the per-VM limit, against the mock driver.
- [x] **M3.11** Periodic tasks: reconcile, stuck VMs, idle sweep (using `last_active_at` only until milestone 4 adds viewer tracking), session cleanup.
- [x] **M3.12** An integration test that runs the whole lifecycle against the mock driver: create, start, suspend, resume, snapshot, restore, resize, delete; a forced failure that lands in `error` and recovers; two simultaneous start requests where exactly one wins; a start refused for lack of capacity.

**Done when:** the lifecycle test passes, and the database shows the expected events and usage intervals afterwards.

### Milestone 4: Desktop in the browser

Goal: the complete user journey works in mock mode.

- [x] **M4.1** `internal/shared/token`: sign and verify, with tests for expiry, tampering, wrong purpose and replay.
- [x] **M4.2** `POST /api/vms/:id/stream-token`.
- [x] **M4.3** Agent stream proxy as in section 11.7, tested with a fake VNC TCP server.
- [x] **M4.4** Stream events to the API, `viewer_connected` and `last_active_at`, and the idle sweep using them.
- [x] **M4.5** Web data layer for VMs: queries, mutations, and the event stream feeding the query cache.
- [x] **M4.6** Web: dashboard with VM cards and actions.
- [x] **M4.7** Web: creation wizard.
- [x] **M4.8** Web: desktop viewer with noVNC, the state table from section 15.4, toolbar (file buttons arrive in milestone 6), reconnect, fullscreen with keyboard lock, and the input idle prompt.
- [x] **M4.9** Web: settings page with rename, keep-alive, history and delete.
- [x] **M4.10** Logout suspends the user's VMs, and the logout confirmation says so.
- [x] **M4.11** Playwright end-to-end test in mock mode: sign up, verify (read the link from Mailpit's API), create a PC, see the desktop canvas receive frames, log out, confirm the VM is suspended, log in, resume, connected again.

**Done when:** in mock mode, a new user can go from sign-up to moving the mouse on a desktop in the browser, log out, log in again, see the PC as suspended, and resume it.

### Milestone 5: Real virtual machines

Goal: the same journey on real KVM VMs, with real suspend and resume. Needs a KVM host. Without one, write and unit-test everything, mark the host-dependent tasks "written, not tested on real hardware", and tell the user exactly which commands to run on a server.

- [ ] **M5.1** `infra/host-setup/`: idempotent scripts for packages, directories, the network, the firewall, `libvirt-guests` and `qemu.conf`. `docs/DEPLOY_HOST.md`.
- [x] **M5.2** `internal/agent/netalloc`: MAC and IP reservation under a lock, with tests.
- [x] **M5.3** qcow2 storage driver, tested with the real `qemu-img` when it is installed.
- [x] **M5.4** `internal/agent/cloudinit`: rendering and seed building, with golden-file tests.
- [x] **M5.5** `internal/agent/domainxml`: the builder, golden files, and `virt-xml-validate` in tests when available.
- [x] **M5.6** libvirt driver: create, start, suspend, shutdown, force stop, reboot, discard, delete, status, list, display, clock sync, capacity, images.
- [x] **M5.7** libvirt driver: resize and snapshots.
- [ ] **M5.8** The Ubuntu image: Packer template, scripts, `images/build.sh`. Build it and boot it by hand with `virsh` before wiring it in.
- [ ] **M5.9** `infra/systemd/zynthera-agent.service` and `infra/caddy/host.Caddyfile`.
- [ ] **M5.10** Run the conformance suite with `ZYN_TEST_LIBVIRT=1`.
- [ ] **M5.11** `scripts/acceptance.sh`: automates every step of section 19 that can be automated, including host-side checks such as "the QEMU process is gone".
- [ ] **M5.12** Measure and record in `docs/PROGRESS.md`: creation time, suspend and resume times for a 4 GB and a 16 GB VM, and saved image sizes.

**Done when:** steps 1 to 7 and 10 to 12 of section 19 pass on a KVM host.

### Milestone 6: Files and snapshots

Goal: users can move files in and out, and manage snapshots from the UI.

- [x] **M6.1** `cmd/file-bridge` and `internal/bridge`, with the path safety tests from section 14.
- [ ] **M6.2** Bake the bridge into the image (binary and unit file) and wire it through cloud-init. Rebuild the image as a new version.
- [x] **M6.3** `POST /api/vms/:id/files-token`.
- [x] **M6.4** Agent file proxy: list, upload, download, CORS, size limit, streaming. The mock driver's equivalent against a temp directory.
- [x] **M6.5** Web: upload button, drag-and-drop, progress and errors; the download panel.
- [x] **M6.6** Web: snapshots, resize and "discard saved session" in the settings page.
- [x] **M6.7** Tests: traversal attempts through the whole chain, an upload over the limit, and an end-to-end upload and download in mock mode.

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

