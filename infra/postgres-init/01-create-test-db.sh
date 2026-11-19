#!/bin/sh
# Runs once, when the Postgres data volume is first created.
# Creates the database used by the Go integration tests.
set -eu
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
  -c "CREATE DATABASE \"${POSTGRES_DB}_test\" OWNER \"$POSTGRES_USER\";"
