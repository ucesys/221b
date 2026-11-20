#!/usr/bin/env bash
# Sherlock's evaluation (P5-06, P6U-19): the simulator with its sample
# problems and a pod with planted instructions, the
# console with sign-in off and Sherlock on from its settings, and bin/221b-eval asking about
# each problem. With ANTHROPIC_API_KEY set it asks the Claude API (the
# nightly run) and fails below MIN cases found; without, it asks the
# simulator's fake API, which checks the plumbing and grades nothing.
# The console keeps its records in PostgreSQL and does not start without
# it (DB-69): the one DEV_DATABASE names (host:port, the user postgres
# without a password, the database DEV_DB_NAME, empty or this evaluation's
# own), else a throwaway one in Docker, of the image CI pins
# (POSTGRES_IMAGE in hack/base-images.env).
#
#   ANTHROPIC_API_KEY=... MODEL=claude-opus-5-5 hack/sherlock-eval.sh
set -euo pipefail
# shellcheck source=hack/lib.sh
source "$(dirname "$0")/lib.sh"
cd "$ROOT"

MODEL=${MODEL:-claude-opus-5-5}
MIN=${MIN:-9}
UI_MIN=${UI_MIN:-90}
REPORT=${REPORT:-$ROOT/bin/sherlock-eval.md}
SIM_PORT=${SIM_PORT:-7460}
PORT=${PORT:-8460}
DB_PORT=${DB_PORT:-8461}

step "Build"
(cd server && go build -o ../bin/221b ./cmd/221b && go build -o ../bin/221b-sim ./cmd/221b-sim && go build -o ../bin/221b-eval ./cmd/221b-eval)
endstep

api="http://127.0.0.1:$SIM_PORT/claude"
key="sk-ant-fake-evaluation-0123456789"
if [ -n "${ANTHROPIC_API_KEY:-}" ]; then
  api="https://api.anthropic.com"
  key=$ANTHROPIC_API_KEY
else
  MIN=0
  UI_MIN=0
  notice "Sherlock evaluation" "No ANTHROPIC_API_KEY: asking the simulator's fake Claude API, which checks the plumbing only."
fi

step "Start the database, the simulator and the console"
db=
if [ -n "${DEV_DATABASE:-}" ]; then
  dbaddr=$DEV_DATABASE
else
  db=221b-eval-db-$$
  dbaddr=127.0.0.1:$DB_PORT
  docker run -d --rm --name "$db" -e POSTGRES_HOST_AUTH_METHOD=trust -p "$dbaddr:5432" "$(sed -n 's/^POSTGRES_IMAGE=//p' hack/base-images.env)" > /dev/null
fi
bin/221b-sim -nodes 10 -pods 200 -problems -planted -listen "127.0.0.1:$SIM_PORT" > "$WORK/eval-sim.log" 2>&1 &
sim=$!
# The container was started with --rm: stopping it removes it.
trap 'kill $sim ${console:-} 2>/dev/null || true; if [ -n "$db" ]; then docker stop -t 1 "$db" > /dev/null; fi' EXIT
if [ -n "$db" ]; then
  for _ in $(seq 60); do docker exec "$db" pg_isready -q -h 127.0.0.1 && break; sleep 1; done
fi
for _ in $(seq 50); do curl -fs "http://127.0.0.1:$SIM_PORT/version" > /dev/null && break; sleep 0.2; done
# Sherlock on, saved in the settings ConfigMap the simulator serves before
# the console starts: with sign-in off nobody may change the settings
# (everyone is a Viewer), so they come in through the one-time copy of a
# release before 0.13 (DB-17), into a database that holds none yet.
python3 - "$MODEL" "${DAILY_USD:-10}" "${MONTHLY_USD:-100}" > "$WORK/eval-settings.json" <<'PY'
import json, sys
model, daily, monthly = sys.argv[1], float(sys.argv[2]), float(sys.argv[3])
s = {"version": 1, "refreshSeconds": 3, "sherlock": {"clusters": {"local": True}, "model": model, "dailyUSD": daily, "monthlyUSD": monthly}}
print(json.dumps({"data": {"settings.json": json.dumps(s)}}))
PY
curl -fsS -X PATCH -H 'Content-Type: application/merge-patch+json' --data @"$WORK/eval-settings.json" \
  "http://127.0.0.1:$SIM_PORT/api/v1/namespaces/221b/configmaps/k8s-console-221b" > /dev/null
SHERLOCK_API_KEY=$key bin/221b -api-server "http://127.0.0.1:$SIM_PORT" -cluster-name eval -listen "127.0.0.1:$PORT" -log-format text -sherlock-api-url "$api" \
  -settings-configmap 221b/k8s-console-221b \
  -db-host "${dbaddr%:*}" -db-port "${dbaddr##*:}" -db-user postgres -db-name "${DEV_DB_NAME:-postgres}" -db-sslmode disable \
  > "$WORK/eval-audit.log" 2> "$WORK/eval-console.log" &
console=$!
# The first start makes the schema and copies the settings above in.
for _ in $(seq 600); do curl -fs "http://127.0.0.1:$PORT/readyz" > /dev/null && break; sleep 0.2; done
endstep

step "Ask Sherlock about each sample problem"
status=0
bin/221b-eval -console "http://127.0.0.1:$PORT" -report "$REPORT" -min "$MIN" -ui-min "$UI_MIN" || status=$?
endstep
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then cat "$REPORT" >> "$GITHUB_STEP_SUMMARY"; fi
exit $status
