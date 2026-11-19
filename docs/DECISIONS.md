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
