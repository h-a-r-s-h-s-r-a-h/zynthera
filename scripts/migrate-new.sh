#!/usr/bin/env bash
# Creates the next pair of migration files: NNNNNN_<name>.up.sql / .down.sql
set -euo pipefail
cd "$(dirname "$0")/../db/migrations"

name="$1"
if [[ ! "$name" =~ ^[a-z][a-z0-9_]*$ ]]; then
  echo "name must be lower_snake_case, for example: add_vm_tags" >&2
  exit 1
fi

last="$(ls | sed -n 's/^\([0-9]\{6\}\)_.*/\1/p' | sort | tail -n 1)"
next="$(printf '%06d' "$((10#${last:-0} + 1))")"

touch "${next}_${name}.up.sql" "${next}_${name}.down.sql"
echo "created db/migrations/${next}_${name}.up.sql"
echo "created db/migrations/${next}_${name}.down.sql"
