#!/usr/bin/env bash
# Generate traffic from inside the cluster so the dashboard has something to show.
#
#   monitoring/demo/traffic.sh normal [seconds]   20 keys, ~20 req/s: everything allowed
#   monitoring/demo/traffic.sh abuse  [seconds]   one key, ~100 req/s: mostly 429
#   monitoring/demo/traffic.sh unique [seconds]   a new key every request, ~60+ req/s (see below)
#   monitoring/demo/traffic.sh stop               remove all traffic pods
#
# "unique" fills the limiter's key table. With the default MAX_KEYS of 100000
# nothing happens, but after
#   kubectl -n ratelimiter set env deployment/ratelimiter MAX_KEYS=15
# the table overflows and the service answers 503: a genuine server error,
# which is what the error-rate panel and alert are for. Undo with:
#   kubectl -n ratelimiter rollout undo deployment/ratelimiter
set -euo pipefail

NS=${NS:-ratelimiter}
IMAGE=${IMAGE:-curlimages/curl:8.22.0}
mode=${1:-}
duration=${2:-120}

case "$mode" in
  normal) sleep_s=0.05 ;;
  abuse)  sleep_s=0.01 ;;
  unique) sleep_s=0.01 ;;
  stop)
    kubectl -n "$NS" delete pod -l app.kubernetes.io/name=traffic --ignore-not-found
    exit 0 ;;
  *)
    sed -n '2,15p' "$0" | sed 's/^# \{0,1\}//'
    exit 2 ;;
esac

read -r -d '' script <<'SH' || true
end=$(( $(date +%s) + DURATION )); i=0
while [ "$(date +%s)" -lt "$end" ]; do
  i=$((i + 1))
  case "$MODE" in
    normal) key="user-$((i % 20))" ;;
    abuse)  key="abuser" ;;
    unique) key="k-$end-$i" ;;
  esac
  curl -s -o /dev/null -m 2 -X POST http://ratelimiter.ratelimiter.svc/check -d "{\"key\":\"$key\"}"
  sleep "$SLEEP"
done
SH

kubectl -n "$NS" delete pod "traffic-$mode" --ignore-not-found --wait=true >/dev/null
kubectl -n "$NS" run "traffic-$mode" --restart=Never --image="$IMAGE" \
  --labels=app.kubernetes.io/name=traffic \
  --env="MODE=$mode" --env="DURATION=$duration" --env="SLEEP=$sleep_s" \
  --command -- sh -c "$script" >/dev/null
echo "traffic-$mode running for ${duration}s (stop early: $0 stop)"
