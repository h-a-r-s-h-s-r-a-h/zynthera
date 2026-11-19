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
