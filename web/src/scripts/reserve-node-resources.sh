#!/bin/sh
# 221b: describe: Keep CPU and memory away from Kubernetes on RKE2 or k3s nodes, for software that runs beside it such as MinIO, and restart the agent so the kubelet takes the change
# 221b: param: SYSTEM_RESERVED_CPU required CPU kept for everything outside Kubernetes, such as 4 or 500m; 0 keeps none
# 221b: param: SYSTEM_RESERVED_MEMORY required Memory kept for everything outside Kubernetes, such as 16Gi or 512Mi; 0 keeps none
# 221b: param: KUBE_RESERVED_CPU optional CPU kept for the agent itself, such as 500m
# 221b: param: KUBE_RESERVED_MEMORY optional Memory kept for the agent itself, such as 1Gi
# 221b: privilege: full
# 221b: dry-run: yes
set -eu

KUBE_RESERVED_CPU=${KUBE_RESERVED_CPU:-}
KUBE_RESERVED_MEMORY=${KUBE_RESERVED_MEMORY:-}

result() { printf '{"changed": %s, "msg": "%s"}\n' "$1" "$2"; }
fail() {
  result false "$1"
  exit 1
}

# The agent decides where its configuration lives.
unit=
for u in rke2-server rke2-agent k3s k3s-agent; do
  if systemctl is-active --quiet "$u"; then
    unit=$u
    break
  fi
done
case "$unit" in
  rke2-*) dir=/etc/rancher/rke2/config.yaml.d ;;
  k3s*) dir=/etc/rancher/k3s/config.yaml.d ;;
  *) fail "no rke2 or k3s service is running on this node" ;;
esac
file=$dir/50-221b-reserved.yaml

cpu_ok() { printf '%s' "$1" | grep -Eq '^([0-9]+(\.[0-9]+)?|[0-9]+m)$'; }
memory_ok() { printf '%s' "$1" | grep -Eq '^[0-9]+(Ki|Mi|Gi|Ti)?$'; }
cpu_ok "$SYSTEM_RESERVED_CPU" || fail "SYSTEM_RESERVED_CPU is not a CPU amount: use 4, 0.5 or 500m"
memory_ok "$SYSTEM_RESERVED_MEMORY" || fail "SYSTEM_RESERVED_MEMORY is not a memory amount: use 16Gi, 512Mi or 0"
[ -z "$KUBE_RESERVED_CPU" ] || cpu_ok "$KUBE_RESERVED_CPU" || fail "KUBE_RESERVED_CPU is not a CPU amount: use 4, 0.5 or 500m"
[ -z "$KUBE_RESERVED_MEMORY" ] || memory_ok "$KUBE_RESERVED_MEMORY" || fail "KUBE_RESERVED_MEMORY is not a memory amount: use 16Gi, 512Mi or 0"

millicores() {
  case "$1" in
    "") echo 0 ;;
    *m) echo "${1%m}" ;;
    *) awk -v v="$1" 'BEGIN { printf "%.0f", v * 1000 }' ;;
  esac
}
bytes() {
  case "$1" in
    "") echo 0 ;;
    *Ki) awk -v v="${1%Ki}" 'BEGIN { printf "%.0f", v * 1024 }' ;;
    *Mi) awk -v v="${1%Mi}" 'BEGIN { printf "%.0f", v * 1048576 }' ;;
    *Gi) awk -v v="${1%Gi}" 'BEGIN { printf "%.0f", v * 1073741824 }' ;;
    *Ti) awk -v v="${1%Ti}" 'BEGIN { printf "%.0f", v * 1099511627776 }' ;;
    *) echo "$1" ;;
  esac
}

# Pods need something left: all of it reserved, nothing could run here.
cpu_held=$(($(millicores "$SYSTEM_RESERVED_CPU") + $(millicores "$KUBE_RESERVED_CPU")))
memory_held=$(($(bytes "$SYSTEM_RESERVED_MEMORY") + $(bytes "$KUBE_RESERVED_MEMORY")))
cpu_here=$(($(nproc) * 1000))
memory_here=$(awk '/^MemTotal:/ { printf "%.0f", $2 * 1024 }' /proc/meminfo)
[ "$cpu_held" -lt "$cpu_here" ] || fail "reserving ${cpu_held}m leaves pods no CPU: $NODE has ${cpu_here}m"
[ "$memory_held" -lt "$memory_here" ] || fail "reserving $memory_held bytes leaves pods no memory: $NODE has $memory_here"

kube=
[ -z "$KUBE_RESERVED_CPU" ] || kube=cpu=$KUBE_RESERVED_CPU
[ -z "$KUBE_RESERVED_MEMORY" ] || kube=${kube:+$kube,}memory=$KUBE_RESERVED_MEMORY
want="# Written by 221B's reserve-node-resources script.
kubelet-arg+:
  - \"system-reserved=cpu=$SYSTEM_RESERVED_CPU,memory=$SYSTEM_RESERVED_MEMORY\""
[ -z "$kube" ] || want="$want
  - \"kube-reserved=$kube\""

had=no
have=
if [ -f "$file" ]; then
  had=yes
  have=$(cat "$file")
fi
if [ "$have" = "$want" ]; then
  result false "already set in $file"
  exit 0
fi
if [ "$DRY_RUN" = 1 ]; then
  result true "would set system-reserved cpu=$SYSTEM_RESERVED_CPU,memory=$SYSTEM_RESERVED_MEMORY in $file and restart $unit"
  exit 0
fi

put() {
  mkdir -p "$dir"
  printf '%s\n' "$1" >"$file.tmp"
  chmod 644 "$file.tmp"
  mv "$file.tmp" "$file"
}

kubelet_up() {
  command -v curl >/dev/null 2>&1 || return 0
  tries=0
  while [ "$tries" -lt 60 ]; do
    curl -fsm 3 http://127.0.0.1:10248/healthz >/dev/null 2>&1 && return 0
    tries=$((tries + 1))
    sleep 5
  done
  return 1
}

put "$want"
if systemctl restart "$unit" && kubelet_up; then
  result true "set system-reserved cpu=$SYSTEM_RESERVED_CPU,memory=$SYSTEM_RESERVED_MEMORY and restarted $unit"
  exit 0
fi

# The agent did not come back: put the node back as it was.
if [ "$had" = yes ]; then
  put "$have"
else
  rm -f "$file"
fi
systemctl restart "$unit" || true
fail "$unit did not come back with the new setting, so $file is back as it was"
