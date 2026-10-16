#!/usr/bin/env bash
# Sherlock's evaluation (P5-06, P6U-19): the simulator with its sample
# problems and a pod with planted instructions, the
# console with sign-in off and Sherlock on, and bin/221b-eval asking about
# each problem. With ANTHROPIC_API_KEY set it asks the Claude API (the
# nightly run) and fails below MIN cases found; without, it asks the
# simulator's fake API, which checks the plumbing and grades nothing.
#
#   ANTHROPIC_API_KEY=... MODEL=claude-opus-5-5 hack/sherlock-eval.sh
set -euo pipefail
source "$(dirname "$0")/lib.sh"
cd "$ROOT"

MODEL=${MODEL:-claude-opus-5-5}
MIN=${MIN:-9}
UI_MIN=${UI_MIN:-90}
REPORT=${REPORT:-$ROOT/bin/sherlock-eval.md}
SIM_PORT=${SIM_PORT:-7460}
PORT=${PORT:-8460}

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

step "Start the simulator and the console"
bin/221b-sim -nodes 10 -pods 200 -problems -planted -listen "127.0.0.1:$SIM_PORT" > "$WORK/eval-sim.log" 2>&1 &
sim=$!
trap 'kill $sim ${console:-} 2>/dev/null || true' EXIT
for _ in $(seq 50); do curl -fs "http://127.0.0.1:$SIM_PORT/version" > /dev/null && break; sleep 0.2; done
SHERLOCK_API_KEY=$key bin/221b -api-server "http://127.0.0.1:$SIM_PORT" -cluster-name eval -listen "127.0.0.1:$PORT" -log-format text -sherlock-api-url "$api" > "$WORK/eval-audit.log" 2> "$WORK/eval-console.log" &
console=$!
for _ in $(seq 100); do curl -fs "http://127.0.0.1:$PORT/readyz" > /dev/null && break; sleep 0.2; done
curl -fsS -X PUT -H 'Content-Type: application/json' "http://127.0.0.1:$PORT/api/v1/sherlock/config" \
  -d "{\"clusters\":{\"local\":true},\"model\":\"$MODEL\",\"dailyUSD\":${DAILY_USD:-10},\"monthlyUSD\":${MONTHLY_USD:-100}}" > /dev/null
endstep

step "Ask Sherlock about each sample problem"
status=0
bin/221b-eval -console "http://127.0.0.1:$PORT" -report "$REPORT" -min "$MIN" -ui-min "$UI_MIN" || status=$?
endstep
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then cat "$REPORT" >> "$GITHUB_STEP_SUMMARY"; fi
exit $status
