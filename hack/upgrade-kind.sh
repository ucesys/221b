#!/usr/bin/env bash
# Upgrades a release of 221B to this build in a kind cluster and checks that
# nothing kept in the cluster is lost (P7-03): the chart and image of an
# earlier release are installed, given a local user, settings, a fleet, an
# acknowledged alert, a cluster connection and a Sherlock key through its
# own API, then upgraded to this build, rolled back and upgraded again.
#
# This build keeps its records in the bundled database (DB-69), which a
# release before 0.13 does not have: the upgrade adds it, and the first
# start copies the records from the ConfigMaps and Secrets into it and
# removes them there (DB-17, DB-61, DB-64, DB-72). So after the upgrade the
# records are checked in the database, and the ConfigMaps of before are
# checked gone: the chart renders them only while they are there, with
# helm.sh/resource-policy: keep, so Helm does not delete one before the
# console has copied it, and the console deletes each once its record sets
# are copied (DB-72, ADR-167). The rollback to
# the release before is the one docs/upgrading.md describes: it starts with
# the accounts and Sherlock's key, which stay in their Secrets, and with
# none of the settings, fleets, acknowledgements or connections, which are
# in the database only; the upgrade again finds them all there.
#
#   IMAGE=ghcr.io/ucesys/221b:ci DB_IMAGE=ghcr.io/ucesys/221b-postgres:ci hack/upgrade-kind.sh v0.12.1
#
# The release's image (ghcr.io/ucesys/221b:0.12.1 for v0.12.1) is pulled
# unless FROM_IMAGE names one already in the local docker; the release's
# chart is taken from its git tag. IMAGE and DB_IMAGE are built when not in
# the local docker (hack/lib.sh). CLUSTER names the kind cluster
# (221b-upgrade). Needs docker, kubectl, helm and git with the tag fetched.
# Set KEEP=1 to keep the cluster afterwards.
#
# A release of 0.13 or later keeps its records in its own bundled database
# already (FROM_DB): its database image (ghcr.io/ucesys/221b-postgres of
# the same version) is pulled too, nothing is copied at the upgrade, and the
# rollback finds every record still in the database, since a build runs
# against a newer schema, which only added (server/internal/db/migrate.go).
set -euo pipefail
# shellcheck source=hack/lib.sh
source "$(dirname "$0")/lib.sh"

FROM_TAG=${1:?usage: hack/upgrade-kind.sh vX.Y.Z}
FROM_IMAGE=${FROM_IMAGE:-ghcr.io/ucesys/221b:${FROM_TAG#v}}
FROM_DB=
if [ "$(printf '%s\n' 0.13.0 "${FROM_TAG#v}" | sort -V | head -1)" = 0.13.0 ]; then
  FROM_DB=1
  FROM_DB_IMAGE=${FROM_DB_IMAGE:-ghcr.io/ucesys/221b-postgres:${FROM_TAG#v}}
fi
MINOR=${MINOR:-1.37}
CLUSTER=${CLUSTER:-221b-upgrade}
NS=221b
RELEASE=k8s-console
FULLNAME=k8s-console-221b
DBNAME=$FULLNAME-db
# The port-forward to the console's Service, on the host.
PF_PORT=${PF_PORT:-18090}
URL=http://127.0.0.1:$PF_PORT
ADMIN=admin@corp.example
# Made on the Users page of the earlier release, so it lives only in the
# accounts Secret the console writes. A user name has no @: the console
# refuses one.
USER2=una
USER2_EMAIL=una@corp.example
PASSWORD=upgrade-password-12
COOKIES=$WORK/upgrade-cookies
SHERLOCK_KEY=sk-ant-upgrade-fake-key-0123456789

# signin URL USER: signs the shell in and keeps the session cookie.
signin() {
  rm -f "$COOKIES"
  local form
  form=$(curl -fsS -c "$COOKIES" "$1/signin" | grep -o 'name="form" value="[^"]*"' | cut -d'"' -f4)
  [ -n "$form" ]
  curl -fsS -b "$COOKIES" -c "$COOKIES" -o /dev/null -X POST --data-urlencode "email=$2" --data-urlencode "password=$PASSWORD" --data-urlencode "next=/" --data-urlencode "form=$form" "$1/signin"
  grep -q 221b_session "$COOKIES"
}
# A signed-in curl: api [curl options] URL
api() { curl -fsS -b "$COOKIES" "$@"; }
# A signed-in change, sent as the console's own pages send it (X-06).
change() { api -H 'Content-Type: application/json' -H 'Sec-Fetch-Site: same-origin' "$@"; }

pf=
# open_console: waits for the rollout and for its pods alone to answer, then
# forwards $URL to the Service.
open_console() {
  if [ -n "$pf" ]; then kill "$pf" 2>/dev/null || true; fi
  kubectl -n "$NS" rollout status "deployment/$FULLNAME" --timeout=120s
  local want
  want=$(kubectl -n "$NS" get deployment "$FULLNAME" -o jsonpath='{.spec.replicas}')
  for _ in $(seq 90); do
    [ "$(kubectl -n "$NS" get pods -l app.kubernetes.io/name=221b --no-headers 2>/dev/null | wc -l)" = "$want" ] && break
    sleep 1
  done
  kubectl -n "$NS" port-forward "svc/$FULLNAME" "$PF_PORT:8081" > "$WORK/upgrade-port-forward.log" 2>&1 &
  pf=$!
  for _ in $(seq 30); do curl -fs "$URL/readyz" > /dev/null && break; sleep 1; done
  curl -fsS "$URL/readyz"
}

# What must come through every upgrade and rollback, as JSON with sorted
# keys: the refresh rate, the fleets and the acknowledged alerts. A time is
# kept to the microsecond in the database (timestamptz), to the nanosecond
# before it: times are compared to the microsecond.
kept_settings() {
  # Until a replica knows which one leads, it answers 503 (ADR-70): read
  # again for a while after a rollout.
  for _ in $(seq 30); do
    api "$URL/api/v1/settings" > "$WORK/upgrade-settings.json" 2>/dev/null && break
    sleep 1
  done
  python3 -c '
import json, re, sys
s = json.load(sys.stdin)
def micro(v):
    if isinstance(v, dict):
        return {k: micro(x) for k, x in v.items()}
    if isinstance(v, list):
        return [micro(x) for x in v]
    if isinstance(v, str):
        return re.sub(r"^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)(?:\.(\d+))?Z$", lambda m: m.group(1) + "." + (m.group(2) or "").ljust(6, "0")[:6] + "Z", v)
    return v
print(json.dumps(micro({"refreshSeconds": s.get("refreshSeconds"), "fleets": s.get("fleets"), "acks": s.get("acks")}), sort_keys=True))' < "$WORK/upgrade-settings.json"
}

# The format version of the JSON document on standard input, or 0.
stored_version() {
  python3 -c 'import json, sys; print(json.loads(sys.stdin.read() or "{}").get("version", 0))'
}

cleanup() {
  local code=$?
  if [ -n "$pf" ]; then kill "$pf" 2>/dev/null || true; fi
  if [ "$code" -ne 0 ]; then
    step "Diagnostics"
    {
      helm -n "$NS" history "$RELEASE"
      kubectl get pods -A -o wide
      kubectl -n "$NS" describe pods
      kubectl get events -A --sort-by=.lastTimestamp | tail -40
      kubectl -n "$NS" get configmap "$FULLNAME" -o yaml
      kubectl -n "$NS" logs -l app.kubernetes.io/name=221b-db --tail=100 --all-containers --prefix
      kubectl -n "$NS" logs -l app.kubernetes.io/name=221b --tail=200 --all-containers --prefix
    } > "$WORK/diagnostics.log" 2>&1 || true
    if [ -z "${GITHUB_ACTIONS:-}" ]; then cat "$WORK/diagnostics.log"; fi
    endstep
  fi
  if [ -z "${KEEP:-}" ]; then kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true; fi
  exit "$code"
}
trap cleanup EXIT

step "The chart and image of $FROM_TAG"
install_kind "$MINOR"
build_images
db_values
FROM_CHART=$WORK/from-chart
rm -rf "$FROM_CHART"
mkdir -p "$FROM_CHART"
git -C "$ROOT" archive "$FROM_TAG" deploy/helm/221b | tar -x -C "$FROM_CHART"
FROM_CHART=$FROM_CHART/deploy/helm/221b
grep -E '^(version|appVersion):' "$FROM_CHART/Chart.yaml"
docker image inspect "$FROM_IMAGE" > /dev/null 2>&1 || docker pull -q "$FROM_IMAGE"
FROM_VALUES=()
if [ -n "$FROM_DB" ]; then
  docker image inspect "$FROM_DB_IMAGE" > /dev/null 2>&1 || docker pull -q "$FROM_DB_IMAGE"
  FROM_VALUES=(--set "database.bundled.image.repository=${FROM_DB_IMAGE%:*}" --set "database.bundled.image.tag=${FROM_DB_IMAGE##*:}"
    --set database.bundled.image.pullPolicy=Never)
  echo "from $FROM_IMAGE and $FROM_DB_IMAGE to $IMAGE and $DB_IMAGE"
else
  echo "from $FROM_IMAGE to $IMAGE"
fi
endstep

step "Create a kind cluster with Kubernetes $MINOR"
kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
kind create cluster --name "$CLUSTER" --image "${KIND_NODE_IMAGE[$MINOR]}" --config "$(kind_config)" --wait 120s
kind load docker-image "$FROM_IMAGE" --name "$CLUSTER"
if [ -n "$FROM_DB" ]; then kind load docker-image "$FROM_DB_IMAGE" --name "$CLUSTER"; fi
load_images "$CLUSTER"
endstep

step "helm install $FROM_TAG"
kubectl create namespace "$NS"
kubectl label namespace "$NS" pod-security.kubernetes.io/enforce=restricted pod-security.kubernetes.io/enforce-version=latest
hash=$(printf '%s' "$PASSWORD" | docker run --rm -i "$FROM_IMAGE" hash-password)
# The earlier chart's appVersion is not its release's version, so the
# image is named outright; the same values serve every release since.
helm install "$RELEASE" "$FROM_CHART" -n "$NS" \
  --set clusterName=kind-upgrade \
  --set image.repository="${FROM_IMAGE%:*}" --set image.tag="${FROM_IMAGE##*:}" --set image.pullPolicy=Never \
  --set auth.mode=local \
  --set "auth.local.users[0].email=$ADMIN" --set "auth.local.users[0].name=Ada Admin" --set "auth.local.users[0].passwordHash=$hash" \
  --set 'auth.local.users[0].roles[0]=superAdmin' \
  --set "auth.admins[0]=$ADMIN" \
  "${FROM_VALUES[@]}" \
  --wait --timeout "$HELM_TIMEOUT"
open_console
signin "$URL" "$ADMIN"
api "$URL/api/v1/me" | has '"admin":true'
endstep

step "Give $FROM_TAG something to keep"
# A local user made on the Users page.
change -X POST -d "{\"username\":\"$USER2\",\"name\":\"Una Upgrade\",\"email\":\"$USER2_EMAIL\",\"password\":\"$PASSWORD\",\"groups\":[]}" "$URL/api/v1/admin/users" > /dev/null
kubectl -n "$NS" get secret "$FULLNAME-users" -o jsonpath='{.data.users\.json}' | base64 -d | has "\"$USER2\""
# Settings and a fleet.
change -X PUT -d '{"refreshSeconds":13}' "$URL/api/v1/settings" | has '"refreshSeconds":13'
change -X PUT -d '{"fleets":[{"id":"upgrade","name":"Upgrade pool","color":2,"labels":["upgrade-test=yes"],"taints":[]}]}' "$URL/api/v1/fleets" > /dev/null
# An acknowledged alert: a pod that fails to start, in a namespace of its own.
kubectl create namespace upgrade-test
kubectl -n upgrade-test run failing --image="$FROM_IMAGE" --image-pull-policy=Never --restart=Always -- -no-such-flag
key=pod:upgrade-test/failing
for _ in $(seq 90); do
  api "$URL/api/v1/snapshot?views=alerts" | python3 -c '
import json, sys
rows = json.load(sys.stdin)["views"]["alerts"]["rows"]
sys.exit(0 if any(r["key"] == sys.argv[1] and r["severity"] != "recovered" for r in rows) else 1)' "$key" && break
  sleep 2
done
# A crash-looping pod's alert flickers between failing and recovered, and
# the release refuses (409) to acknowledge one that has just cleared: try
# again for a minute.
for i in $(seq 30); do
  change -X POST -d "{\"key\":\"$key\",\"acknowledge\":true}" "$URL/api/v1/alerts/ack" > /dev/null && break
  [ "$i" -lt 30 ] || { echo "$FROM_TAG would not acknowledge $key"; exit 1; }
  sleep 2
done
# A connection to the cluster itself, kept in a Secret the chart does not own.
TOKEN=$(kubectl -n "$NS" create token "$FULLNAME" --duration=2h)
python3 - "$TOKEN" "$(kubectl -n "$NS" get configmap kube-root-ca.crt -o jsonpath='{.data.ca\.crt}')" > "$WORK/upgrade-draft.json" <<'PY'
import json, sys
print(json.dumps({"name": "kept", "env": "staging", "description": "kind, through its in-cluster address", "server": "https://kubernetes.default.svc", "ca": "pem", "caPem": sys.argv[2], "auth": "token", "token": sys.argv[1], "mode": "read"}))
PY
change -X POST --data-binary "@$WORK/upgrade-draft.json" "$URL/api/v1/clusters" | has '"id":"kept"'
# Sherlock's key, kept in the release's Secret.
change -X PUT -d "{\"key\":\"$SHERLOCK_KEY\"}" "$URL/api/v1/sherlock/key" > /dev/null
before=$(kept_settings)
echo "kept: $before"
echo "$before" | has '"refreshSeconds": 13'
echo "$before" | has '"upgrade"'
echo "$before" | has "$key"
if [ -n "$FROM_DB" ]; then
  # Where a release of 0.13 keeps them: rows of its database, and the
  # connection's credential alone in its Secret (DB-64).
  [ "$(db_sql "$NS" "$DBNAME-0" "select count(*) from settings where value::text ~ '\"refreshSeconds\": ?13[,}]'")" -ge 1 ] ||
    { echo "$FROM_TAG has no settings row with refreshSeconds 13"; exit 1; }
  kubectl -n "$NS" get secret "$FULLNAME-cluster-kept" -o jsonpath='{.data}' | python3 -c '
import json, sys
d = json.load(sys.stdin)
assert "spec.json" not in d and "token" in d, sorted(d)'
else
  # Where the release before keeps them: the settings ConfigMap, and the
  # connection's Secret (its record and its credential).
  kubectl -n "$NS" get configmap "$FULLNAME" -o jsonpath='{.data.settings\.json}' | has '"refreshSeconds":13'
  kubectl -n "$NS" get secret "$FULLNAME-cluster-kept" -o jsonpath='{.data}' | python3 -c '
import json, sys
d = json.load(sys.stdin)
assert "spec.json" in d and "token" in d, sorted(d)'
fi
endstep

# check_kept WHAT: everything given above is still there.
check_kept() {
  signin "$URL" "$ADMIN"
  local now
  now=$(kept_settings)
  if [ "$now" != "$before" ]; then
    echo "The settings changed $1:"
    echo "before: $before"
    echo "after:  $now"
    exit 1
  fi
  api "$URL/api/v1/admin/users" | has "\"$USER2\""
  for _ in $(seq 60); do
    api "$URL/api/v1/clusters/kept" | has '"state":"connected"' && break
    sleep 1
  done
  api "$URL/api/v1/clusters/kept" | tee "$WORK/upgrade-kept.json" | has '"state":"connected"' ||
    { echo "the connection did not come back $1: $(cat "$WORK/upgrade-kept.json")"; exit 1; }
  # Sherlock answers once the new pod holds the Lease (P7-02).
  for _ in $(seq 30); do
    api "$URL/api/v1/sherlock/config" > "$WORK/upgrade-sherlock.json" 2>/dev/null && break
    sleep 1
  done
  grep -q '"set":true' "$WORK/upgrade-sherlock.json"
  [ "$(kubectl -n "$NS" get secret "$FULLNAME-sherlock" -o jsonpath='{.data.apiKey}' | base64 -d)" = "$SHERLOCK_KEY" ]
  # The user made on the Users page signs in with the same password.
  signin "$URL" "$USER2"
  api "$URL/api/v1/me" | has "\"username\":\"$USER2\""
  signin "$URL" "$ADMIN"
  echo "the settings, fleet, acknowledgement, user, connection and Sherlock key came through $1"
}

# upgrade: the earlier release to this build, reusing its values; the
# chart's defaults add the bundled database, of DB_IMAGE.
upgrade() {
  helm upgrade "$RELEASE" "$ROOT/deploy/helm/221b" -n "$NS" --reset-then-reuse-values \
    --set image.repository="${IMAGE%:*}" --set image.tag="${IMAGE##*:}" --set image.pullPolicy=Never \
    "${DB_VALUES[@]}" \
    --wait --timeout "$HELM_TIMEOUT"
  open_console
}

# in_database: the records given to the release before are in this
# build's database, and gone from where that release kept them.
in_database() {
  check_database "$URL" api
  # Each record set copied is written down once (DB-17).
  db_sql "$NS" "$DBNAME-0" "select import_id || ' from ' || source || ': ' || record_count from imports order by import_id" | tee "$WORK/upgrade-imports.txt"
  if [ -z "$FROM_DB" ]; then
    has -i "configmap" "$WORK/upgrade-imports.txt" || { echo "no record set was copied from a ConfigMap"; exit 1; }
  fi
  # The rows: the settings with their refresh rate, the fleet, the
  # acknowledged alert and the connection.
  [ "$(db_sql "$NS" "$DBNAME-0" "select count(*) from settings where value::text ~ '\"refreshSeconds\": ?13[,}]'")" -ge 1 ] ||
    { echo "no settings row holds refreshSeconds 13"; exit 1; }
  [ "$(db_sql "$NS" "$DBNAME-0" "select count(*) from fleets where fleet_key = 'upgrade'")" = 1 ] || { echo "the fleet is not a row"; exit 1; }
  [ "$(db_sql "$NS" "$DBNAME-0" "select count(*) from alert_acks where alert_key = '$key'")" = 1 ] || { echo "the acknowledgement is not a row"; exit 1; }
  [ "$(db_sql "$NS" "$DBNAME-0" "select count(*) from clusters where cluster_id = 'kept' and is_kept")" = 1 ] || { echo "the connection is not a row"; exit 1; }
  # The ConfigMaps of before are deleted once their record sets are copied
  # (DB-72); the configuration ConfigMaps stay. The deletion follows the
  # copy, which the console does once it is ready, so wait a little.
  for _ in $(seq 30); do
    kubectl -n "$NS" get configmap "$FULLNAME" > /dev/null 2>&1 || break
    sleep 2
  done
  if kubectl -n "$NS" get configmap "$FULLNAME" > /dev/null 2>&1; then
    echo "the settings ConfigMap of before is still there"; exit 1
  fi
  left=$(kubectl -n "$NS" get configmap -o name | sed 's|configmap/||' | grep "^$FULLNAME" || true)
  echo "ConfigMaps of the release left: $(echo "$left" | tr '\n' ' ')"
  for cm in $left; do
    case "$cm" in
      "$FULLNAME"-agents | "$FULLNAME"-auth-config | "$FULLNAME"-toolbox | "$DBNAME"-*) ;;
      *) echo "a ConfigMap of before is still there: $cm"; exit 1 ;;
    esac
  done
  # The connection's Secret keeps its credential and not its record (DB-64).
  kubectl -n "$NS" get secret "$FULLNAME-cluster-kept" -o jsonpath='{.data}' | python3 -c '
import json, sys
d = json.load(sys.stdin)
assert "spec.json" not in d and "token" in d, sorted(d)
print("the connection Secret holds", sorted(d))'
  echo "the records are in the database, the ConfigMaps of before are gone, and the record is gone from the Secret"
}

step "helm upgrade from $FROM_TAG to this build"
start=$(date +%s)
upgrade
notice "Upgrade from $FROM_TAG" "Ready $(( $(date +%s) - start ))s after helm upgrade."
check_kept "the upgrade"
in_database
endstep

step "Saved again, the settings are a row of the database, and the accounts carry their format's version"
change -X PUT -d '{"refreshSeconds":13,"timeFormat":{"date":"eu","clock":"24h"}}' "$URL/api/v1/settings" > /dev/null
[ "$(db_sql "$NS" "$DBNAME-0" "select count(*) from settings where value::text ~ '\"clock\": ?\"24h\"'")" -ge 1 ] ||
  { echo "the time format saved is not in the database"; exit 1; }
# Nothing is saved in a ConfigMap any more (DB-69), and the settings
# ConfigMap of before is not made again (DB-72).
! kubectl -n "$NS" get configmap "$FULLNAME" > /dev/null 2>&1 || { echo "the settings ConfigMap is back"; exit 1; }
change -X PATCH -d '{"name":"Una Upgraded"}' "$URL/api/v1/admin/users/$USER2" > /dev/null
v=$(kubectl -n "$NS" get secret "$FULLNAME-users" -o jsonpath='{.data.users\.json}' | base64 -d | stored_version)
echo "users.json: version $v"
[ "$v" -ge 1 ]
endstep

if [ -n "$FROM_DB" ]; then
  step "helm rollback to $FROM_TAG: its database keeps every record, and it runs on the newer schema"
  helm rollback "$RELEASE" 1 -n "$NS" --wait --timeout "$HELM_TIMEOUT"
  kubectl -n "$NS" rollout status "statefulset/$DBNAME" --timeout=300s
  open_console
  [ "$(kubectl -n "$NS" get deployment "$FULLNAME" -o jsonpath='{.spec.template.spec.containers[0].image}')" = "$FROM_IMAGE" ]
  check_kept "the rollback"
  api "$URL/api/v1/admin/users" | has 'Una Upgraded'
  echo "the rollback to $FROM_TAG kept every record"
  endstep
else
step "helm rollback to $FROM_TAG: the accounts and Sherlock's key, and none of what is in the database (docs/upgrading.md)"
helm rollback "$RELEASE" 1 -n "$NS" --wait --timeout "$HELM_TIMEOUT"
open_console
[ "$(kubectl -n "$NS" get deployment "$FULLNAME" -o jsonpath='{.spec.template.spec.containers[0].image}')" = "$FROM_IMAGE" ]
# The revision before has no database: Helm removes its StatefulSet, and
# keeps what is kept on purpose, the Secret and the volume, for the
# upgrade again.
for _ in $(seq 60); do
  [ -z "$(kubectl -n "$NS" get pods -l app.kubernetes.io/name=221b-db -o name)" ] && break
  sleep 2
done
[ -z "$(kubectl -n "$NS" get statefulset -o name)" ]
kubectl -n "$NS" get secret "$DBNAME" -o name
kubectl -n "$NS" get pvc "data-$DBNAME-0" -o name
# What stays in Secrets comes through: the accounts (the user made on the
# Users page signs in, with the name this build gave it) and Sherlock's key.
signin "$URL" "$USER2"
api "$URL/api/v1/me" | has "\"username\":\"$USER2\""
signin "$URL" "$ADMIN"
api "$URL/api/v1/admin/users" | has 'Una Upgraded'
for _ in $(seq 30); do
  api "$URL/api/v1/sherlock/config" > "$WORK/upgrade-sherlock.json" 2>/dev/null && break
  sleep 1
done
grep -q '"set":true' "$WORK/upgrade-sherlock.json"
# What moved to the database is not there: the settings are the release's
# defaults, with no fleet and no acknowledgement, and the connection's
# Secret has no record to read, so the cluster menu has the local cluster
# alone. Going back means a restore (docs/upgrading.md, 0.13.0).
rolled=$(kept_settings)
echo "after the rollback: $rolled"
if [ "$rolled" = "$before" ] || echo "$rolled" | has '"upgrade"' || echo "$rolled" | has "$key"; then
  echo "the release before found the records the database holds: $rolled"
  exit 1
fi
[ "$(curl -s -b "$COOKIES" -o /dev/null -w '%{http_code}' "$URL/api/v1/clusters/kept")" = 404 ] ||
  { echo "the release before found the connection"; exit 1; }
echo "the rollback has the accounts and Sherlock's key, and not the settings, fleet, acknowledgement or connection, as documented"
endstep
fi

step "helm upgrade again: the database has everything, what the earlier release did not know too"
upgrade
check_kept "the second upgrade"
check_database "$URL" api
api "$URL/api/v1/settings" | python3 -c '
import json, sys
s = json.load(sys.stdin)
assert s.get("timeFormat") == {"date": "eu", "clock": "24h"}, s.get("timeFormat")
print("the time format, unknown to the earlier release, is still in the database after its rollback")'
api "$URL/api/v1/admin/users" | has 'Una Upgraded'
notice "Upgrade from $FROM_TAG" "Upgrade, rollback and upgrade again kept every setting, user, connection and key."
endstep
