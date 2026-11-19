# Zynthera

A personal Linux computer in a browser tab. Each user gets a real virtual machine that
runs on our servers, is suspended when they leave and resumes exactly where they left it.

- `docs/DEPLOY_QUICKSTART.md`: put it on a server with one command.
- `PROJECT_SPEC.md`: the full specification.
- `docs/PROGRESS.md`: what is built so far and what is next.
- `docs/DECISIONS.md`: choices that differ from the specification.

## Quick start (development)

You need Docker, Go and Node.js.

```text
cp .env.example .env
make up
make migrate
make seed
make dev
```

Then open http://localhost:3000.

If you already run Postgres or Redis on this machine, `make up` fails with "port is already
allocated". Change `POSTGRES_PORT` / `REDIS_PORT` in `.env`, and the same port numbers in
`ZYN_DATABASE_URL` / `ZYN_REDIS_URL`.

| Address | What |
|---|---|
| http://localhost:3000 | Web app |
| http://127.0.0.1:8080/health | API health |
| http://127.0.0.1:9090/health | Agent health |
| http://localhost:8025 | Mailpit: every email the app sends |
| http://localhost:8081 | Adminer: database browser |

## Commands

Run `make help` for the full list. The common ones:

| Command | What it does |
|---|---|
| `make dev` | Run the API, the agent and the web app |
| `make test` | Go tests and web unit tests |
| `make lint` | golangci-lint, eslint, prettier |
| `make build` | Build the three binaries into `bin/` |
| `make down` / `make nuke` | Stop the services / also delete their data |

## Layout

| Path | Contents |
|---|---|
| `cmd/` | The three binaries: `api`, `agent`, `file-bridge` |
| `internal/` | Go packages (`shared`, `api`, `agent`, `bridge`) |
| `db/` | SQL migrations and queries |
| `web/` | Next.js app |
| `infra/` | Docker Compose, host setup, systemd, Caddy |
| `images/` | OS image build templates |
| `docs/` | Progress, decisions, deployment and operations guides |
