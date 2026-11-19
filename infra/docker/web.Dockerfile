# The web app as a small standalone Node server.
# Debian-based images, not Alpine: package-lock.json is resolved for glibc
# Linux, and "npm ci" refuses a lock file that does not match the platform.
# Build context: the repository's web/ directory.

FROM node:24-bookworm-slim AS deps
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci

FROM node:24-bookworm-slim AS build
WORKDIR /app
COPY --from=deps /app/node_modules ./node_modules
COPY . .
ENV NEXT_TELEMETRY_DISABLED=1
# In production Caddy sends /api straight to the API, so this address is only
# a fallback for requests that reach the web server directly.
ARG API_INTERNAL_URL=http://127.0.0.1:8080
ENV API_INTERNAL_URL=${API_INTERNAL_URL}
RUN npm run build

FROM node:24-bookworm-slim AS web
WORKDIR /app
ENV NODE_ENV=production NEXT_TELEMETRY_DISABLED=1 PORT=3000 HOSTNAME=0.0.0.0
RUN useradd --no-create-home --uid 10001 zynthera
# "output: standalone" in next.config.ts puts everything the server needs here.
COPY --from=build /app/.next/standalone ./
COPY --from=build /app/.next/static ./.next/static
COPY --from=build /app/public ./public
USER zynthera
EXPOSE 3000
CMD ["node", "server.js"]
