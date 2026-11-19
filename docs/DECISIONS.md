# Decisions

Choices that depart from `PROJECT_SPEC.md` or fill a gap in it, newest last.

## D-001 One-command deployment is a goal of its own

The owner asked for deployment to be "super easy". The spec describes deployment as a
ten-step manual procedure (section 12.8) plus a separate control-plane guide. We add a
deployment milestone (see `docs/PROGRESS.md`): one `install.sh` for a single server that
runs both the control plane and the VMs, with secrets generated and the host registered
automatically. The manual documents are still written, as the reference for what the
script does.

## D-002 Design tokens live in CSS, not in a Tailwind config file

Spec 15.1 says to define tokens "in the Tailwind config". The current Tailwind (v4) has no
JavaScript config by default; tokens are declared with `@theme` in CSS. They are in
`web/src/app/globals.css`, which is the single place for colors and radii.

## D-003 Binaries read `.env` themselves in development

The API and the agent load `.env` from the working directory if it exists; real
environment variables always win. This avoids shell or Make quoting problems with values
such as `ZYN_MAIL_FROM=Zynthera <no-reply@zynthera.local>`. In production there is no
`.env` in the working directory, so nothing changes there.

## D-004 `internal/shared/httpx` and per-binary config packages

Not in the spec's layout: `internal/shared/httpx` (error envelope, request ID, recovery,
access log, graceful shutdown) is shared by the API and the agent so both behave the same.
The typed config structs are `internal/api/appconfig` and `internal/agent/agentconfig`;
`internal/shared/config` keeps only the env-reading helpers.

## D-005 `web/go.mod` exists only to hide `node_modules` from Go

Some npm packages ship Go files. Without a module boundary, `go test ./...` at the root
would scan them. The file contains no code.

## D-006 `GET /api/health` mirrors `GET /health`

The web app can only reach the API under `/api`, so the landing page's health indicator
needs the check there too.

## D-007 Postgres 18 data path

The Compose volume is mounted at `/var/lib/postgresql` (not `.../data`), as the Postgres 18
image requires.

## D-008 `make dev` rebuilds Go once per start

There is no file watcher for the Go binaries; restart `make dev` after changing Go code.
The web app hot-reloads as usual. A watcher can be added later if it becomes annoying.

## D-009 Sign-up does not log the user in

Spec 10.2 requires sign-up to answer the same way whether or not the email already has an
account. Logging the new user in would reveal the difference, so sign-up always answers
"check your email" (HTTP 202) and the person logs in afterwards. Unverified users can still
log in, as the spec says.

## D-010 A password reset also confirms the email address

Using a reset link proves the person controls the mailbox, which is exactly what
verification checks. Without this, someone who lost the verification email and reset their
password would still be asked to verify.

## D-011 Rate limits are applied inside handlers, not as one middleware

The limit keys depend on the request body (the email at login) or on the logged-in user, so
each handler calls the limiter after parsing. If Redis is down the limiter fails closed
(HTTP 500): otherwise an outage would open login to unlimited password guessing.

## D-012 Client address comes from the last X-Forwarded-For entry

The API is only reachable through our own proxy (Caddy, or the Next.js dev server). The
proxy appends the real client address, so the last entry is trustworthy while earlier ones
can be forged by the client. It is used only when the direct peer is a local or private
address.

## D-013 Login state is resolved in the browser

Pages are static and the "who is logged in" check runs in the browser (`useMe`), with
`RequireAuth` / `GuestOnly` wrappers doing the redirects. This keeps every page cacheable and
the API the only place that reads the session cookie. The API enforces access; the redirects
are a convenience.

## D-014 Go integration tests run one package at a time

All packages share the `zynthera_test` database, so `make test` uses `go test -p 1`.

## D-015 The conformance suite lives in `internal/agent/driver/drivertest`

The spec places it "in `internal/agent/driver`". A sub-package keeps the `testing` package
out of the agent binary while still letting every driver's tests call the same suite.

## D-016 The agent validates every VMSpec itself

The agent runs as root, so it does not rely on the control plane's validation. Ids must be
lower-case UUIDs, and image slug, version, hostname, username, password hash and bridge
token must each match a strict pattern before any driver sees them.

## D-017 Agent operations outlive a dropped connection

A VM operation keeps running to its own 15-minute limit even if the calling job
disconnects. A suspend or delete that stops halfway is worse than one whose result nobody
reads, and the control plane's stuck-VM check settles the state afterwards.

## D-018 Driver error semantics

`Status` of an unknown VM reports `missing` without an error and `Delete` of an unknown VM
succeeds; every other operation on an unknown VM returns "not found". Suspending a VM that
is not running succeeds and changes nothing. These rules are pinned by the conformance
suite, so the libvirt driver must match them.

## D-019 `internal/api/vmsvc` is the only code that changes a VM's state

Not in the spec's layout. Handlers call `Begin` (into a transitional state), jobs call
`Settle` (out of it). Both keep the event row, the usage interval and the live update in one
place and one transaction, so they cannot drift apart. `internal/api/vms` stays pure rules.

## D-020 Events are written when a job finishes, not when it is requested

The history shows what happened ("PC suspended because you logged out"), with the actor
carried in the job payload. Requests that were refused leave no event.

## D-021 Row locks use FOR NO KEY UPDATE

The spec says to lock the host row with `SELECT ... FOR UPDATE`. That deadlocked: a job
holding a VM row inserts a usage interval, which takes a foreign-key lock on the host row,
while a start request holds `FOR UPDATE` on the host and waits for the VM row. `FOR NO KEY
UPDATE` still serialises capacity decisions but does not block foreign-key checks. The same
applies to the user row (limits) and the VM row. Lock order is always host, then VM.

## D-022 A new PC's start is capacity-checked in the job

Creation only needs disk. A PC coming from provisioning has not been through the start
check, so `vm:start` re-checks RAM and CPU; if the host is full the PC settles as `stopped`
with an explanation instead of failing.

## D-023 Reboot failure and usage

A reboot keeps the usage interval open. If the reboot job ends in `error`, the interval is
closed at that point.

## D-024 The job queue polls every 200 ms

asynq's default is one second, which a user feels as a pause after every click.

## D-025 The event stream sends a `ready` event after the initial list

So the browser knows the list is complete and can drop PCs it no longer received.

## D-026 Heartbeat VM lists live in Redis

The last two per host, with their arrival time, expiring after two minutes. The reconciler
acts only when both agree and both are newer than the VM's last state change.

## D-027 SHA-512 crypt comes from `github.com/GehirnInc/crypt`

Go's standard library has no crypt(3). The output is checked against `openssl passwd -6`
in a test.

## D-028 The stream proxy always accepts the WebSocket handshake

Wrong origin, bad token and "PC not running" are reported as WebSocket close codes (4403,
4401, 4404) after accepting the handshake, as spec 11.7 lists them. A browser cannot read
the HTTP status of a refused upgrade, so refusing it would leave the viewer unable to tell
the cases apart. Nothing is forwarded before all checks pass.

## D-029 The agent closes streams itself when a PC stops

After a successful suspend, shutdown, force stop or delete the agent closes the PC's stream
with 4404. A real VM's VNC socket closes anyway when QEMU exits; doing it explicitly makes
the mock driver behave the same.

## D-030 Stream events carry the host id

`POST /internal/agent/stream-event` takes `{ host_id, vm_id, event }` (the spec lists only
`vm_id` and `event`). The host id is needed to check the caller's token, and the update is
restricted to VMs that live on that host.

## D-031 The viewer opens the WebSocket itself

noVNC is given an open WebSocket instead of a URL, so the viewer can read the close code and
tell "opened in another tab" from "PC stopped" from "connection dropped".

## D-032 The viewer hides the site header

The desktop page uses the whole window. Its own toolbar (opened by hovering or by clicking
the small tab at the top) leads back to the dashboard.

## D-033 Suspending from the viewer returns to the dashboard

Opening the desktop page starts or resumes the PC automatically, once. After a suspend or
shutdown from the toolbar the viewer navigates away, so the PC is not started again by the
page it was just stopped from.

## D-034 Next.js 16 specifics

The header is wrapped in `<Suspense>` because it reads the current path, which blocks
prerendering of dynamic routes otherwise. Components that depend on who is logged in render
a neutral state until hydration is finished (`useHydrated`), which avoids hydration errors
when the login check finishes before a late part of the page hydrates.

## D-035 Mock VMs have real file bridges

In mock mode the agent runs the real file bridge code in-process, one per mock VM, on a
loopback port, each confined to that VM's directory under the data directory. The agent's
file proxy talks HTTP to it exactly as it would to a bridge inside a guest, so the whole
file path is exercised without KVM.

## D-036 File bridge paths behave like a chroot

A path is re-rooted at the home directory before it is resolved: `../..` from the top stays
at the top, and `/etc/passwd` means `~/etc/passwd`. After resolving symlinks the result must
still be inside the home directory, or the request is refused.

## D-037 The agent reads `ZYN_MAX_UPLOAD_MB` too

Spec 8.2 lists it under the API, but the agent is what enforces it while a file streams
through (spec 11.8). In development both read the same `.env`; on a real host the value goes
into `agent.env` as well.

## D-038 Resize rewrites the domain definition

Instead of `virsh setmaxmem` / `setvcpus`, the driver rebuilds the domain XML from the VM's
metadata with the new sizes and defines it again. One code path builds every definition, so
a resized VM cannot drift from a freshly created one.

## D-039 Firefox comes from Mozilla's APT repository in the image

Ubuntu's own `firefox` package is a snap, which starts slowly in a VM.

## D-040 The build user is removed by a one-shot unit at shutdown

Packer logs in over SSH as a temporary user. That user cannot delete itself while logged in,
so the sealing script installs a unit that removes it at the very end of the shutdown, along
with the unit itself. SSH is disabled and masked in the finished image.

## D-041 One domain for a single server

On a single server the agent's browser endpoints (`/stream`, `/files/*`) are served under
the same domain as the web app, by Caddy. One DNS record, one certificate, no cross-origin
requests. Separate VM hosts keep their own names (`infra/caddy/host.Caddyfile`).

## D-042 The API container uses host networking

The agent listens on loopback only. Host networking lets the API reach it (and Postgres and
Redis, published on loopback) without opening any port to a Docker network.

## D-043 The firewall has its own systemd unit

`/etc/nftables.conf` begins with `flush ruleset` on Debian and Ubuntu, which would delete the
rules libvirt installs. Zynthera's rules live in one table, loaded by
`zynthera-firewall.service` from `/etc/zynthera/firewall.nft`.

## D-044 `seed` can register a production host

With `ZYN_SEED_HOST_PUBLIC_URL` set, `zynthera-api seed` registers this server as a VM host
using the id and secrets from the environment. The installer uses it so nobody has to copy
secrets from the admin area into `agent.env`.

## D-045 The web image is Debian-based

`npm ci` refuses a lock file that does not match the platform, and the lock file is resolved
for glibc Linux. The API image is Alpine (a static Go binary has no such concern).

## D-046 Shutdown repeats its request

`virsh shutdown` is sent again every five seconds until the domain is off. A guest that is
still booting has no agent yet and ignores the ACPI button, so one early request is lost.

## D-047 Clock sync falls back to `date` in the guest

See docs/DEPLOY_HOST.md, "Clock after resume".

## D-048 Deleting an account anonymizes the user row

Deleted PCs and usage records (kept for billing) refer to the user row, so it is not
removed. The account is disabled at once; a periodic task deletes the PCs and then replaces
the email with `deleted-<id>@deleted.invalid`, clears the password hash, and overwrites the
PC names and Linux usernames the person chose. The original email is free for a new sign-up.
The only remaining administrator cannot delete their own account.
