# Running the control plane

The control plane is the API, the web app, Postgres and Redis. `scripts/install.sh` sets it
up on the same server as the VMs. This document describes the parts, for running it by hand
or on a separate machine.

## Parts

| Service | Image | Listens on | Notes |
|---|---|---|---|
| `postgres` | `postgres:18.7-alpine` | `127.0.0.1:5432` | all state; volume `pgdata` |
| `redis` | `redis:8.8.4-alpine` | `127.0.0.1:6379` | job queue, rate limits, live updates; append-only file in volume `redisdata` |
| `migrate` | `zynthera-api` | none | applies migrations, then exits; the API waits for it |
| `api` | `zynthera-api` (built from `infra/docker/go.Dockerfile`) | `127.0.0.1:8080` | HTTP, job workers and the periodic task scheduler in one process |
| `web` | `zynthera-web` (built from `infra/docker/web.Dockerfile`) | `127.0.0.1:3000` | the Next.js app |

Nothing is published to the internet. Caddy on the host is the only public entrance.

```text
docker compose --env-file /etc/zynthera/control-plane.env -f infra/docker-compose.prod.yml up -d --wait
docker compose --env-file /etc/zynthera/control-plane.env -f infra/docker-compose.prod.yml run --rm api seed
```

`seed` creates the admin account and the OS image rows. It is safe to run again.

## Settings

All settings are environment variables in `/etc/zynthera/control-plane.env` (mode 0600).
`.env.example` documents every one. In production the API refuses to start when:

- `ZYN_SECRETS_KEY` or `ZYN_SEED_ADMIN_PASSWORD` still hold a development placeholder,
- `ZYN_DATABASE_URL` uses the development password,
- `ZYN_WEB_ORIGIN` is not `https://`.

Generate secrets with `openssl rand -base64 32` (`ZYN_SECRETS_KEY`) and `openssl rand -hex 24`.

**Back up `ZYN_SECRETS_KEY` separately from the database.** Without it the host credentials
stored in the database cannot be read, and every host has to be registered again.

## Routing

On one server, `infra/caddy/single-server.Caddyfile` routes one domain:

| Path | Goes to |
|---|---|
| `/stream`, `/files/*` | the agent, `127.0.0.1:9090` |
| `/internal/*` | answered with 404: agents on this server report to the API on loopback |
| `/api/*` | the API, `127.0.0.1:8080`, unbuffered (the live update stream needs that) |
| everything else | the web app, `127.0.0.1:3000` |

With separate VM hosts, agents call `https://app.<domain>/internal/agent/...`. Then
`/internal/*` must be routed to the API and limited to the hosts' addresses.

## Only one scheduler

`zynthera-api serve` runs the periodic tasks. Two instances would queue each of them twice.
To run more API instances later, start the extra ones with `--no-scheduler`.

## Email

Set `ZYN_SMTP_HOST`, `ZYN_SMTP_PORT`, `ZYN_SMTP_USER`, `ZYN_SMTP_PASS` and `ZYN_MAIL_FROM`.
The connection is upgraded with STARTTLS when the server offers it.
