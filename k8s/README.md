# Docker and Kubernetes

## Docker

The [`Dockerfile`](../Dockerfile) is a two-stage build: Go compiles a static binary, which is copied onto a distroless base image that has no shell and runs as a non-root user. The result is about 18 MB. CI builds it for `linux/amd64` and `linux/arm64` and publishes it to `ghcr.io/yhqz1/local-rate-limiter-service` (see the main README).

```bash
make docker                                          # build ratelimiter:dev locally
docker run -p 8080:8080 -e RATE_PER_SEC=1 -e BURST=3 ratelimiter:dev
scripts/smoke.sh http://localhost:8080               # same checks CI runs
```

## Kubernetes manifests

| File | What it is |
| ---- | ---------- |
| [`namespace.yaml`](namespace.yaml) | `ratelimiter` namespace |
| [`configmap.yaml`](configmap.yaml) | Runtime settings (`RATE_PER_SEC`, `BURST`, ...) injected as environment variables |
| [`deployment.yaml`](deployment.yaml) | 3 replicas, rolling updates, probes, resource limits, hardened pod |
| [`service.yaml`](service.yaml) | `ClusterIP` Service: `ratelimiter.ratelimiter.svc:80` load-balances across Ready pods |
| [`demo/loadgen.yaml`](demo/loadgen.yaml) | A throw-away in-cluster client used to prove rollouts drop no requests |

Choices in the Deployment worth knowing:

- **`maxSurge: 1`, `maxUnavailable: 0`**: a new pod must be Ready before an old one is removed, so capacity never drops below 3.
- **`minReadySeconds: 5`, `progressDeadlineSeconds: 60`**: a new pod has to stay healthy for 5 s to count, and a rollout that stalls is reported as failed after 60 s.
- **Readiness and liveness probes on `/health`**: readiness gates traffic and rollouts, liveness restarts a wedged container.
- **`preStop` sleep of 5 s plus the app's graceful shutdown**: a terminating pod first stops receiving new connections, then drains in-flight requests on SIGTERM. (The image has no shell, so this uses Kubernetes' native `sleep` action.)
- **Security**: non-root, read-only root filesystem, no privilege escalation, all capabilities dropped, default seccomp profile.
- The image tag is the release. `deployment.yaml` pins `sha-8424ac9`; every CI build is also tagged by its commit.

## Run it on a local cluster

Needs Docker, `kubectl` and [minikube](https://minikube.sigs.k8s.io/) (kind works the same way).

```bash
minikube start -p ratelimiter --driver=docker --cpus=2 --memory=3072   # Kubernetes v1.34

kubectl apply -f k8s/
kubectl -n ratelimiter rollout status deployment/ratelimiter
kubectl -n ratelimiter get pods,svc

# Reach it from your laptop (port-forward pins to one pod, fine for poking around)
kubectl -n ratelimiter port-forward svc/ratelimiter 8080:80
open http://localhost:8080
scripts/smoke.sh http://localhost:8080
```

Re-applying `k8s/` makes the cluster match the files again, so it resets the image to the tag in `deployment.yaml`.

## Demo: rolling update, bad release, rollback

Start the load generator first so every step below runs under traffic. It makes about 20 requests a second to the Service and logs one status code per request.

```bash
k() { kubectl -n ratelimiter "$@"; }
kubectl apply -f k8s/demo/loadgen.yaml
k wait --for=condition=Ready pod/loadgen
```

Helper to see which version answers (8 requests through the Service):

```bash
vers() { for i in $(seq 8); do k exec loadgen -- curl -s http://ratelimiter/version; echo; done | sort | uniq -c; }
```

### 1. Rolling update (v1 to v2)

```bash
vers                                            # all 8424ac9
k get pods -w &                                 # watch pods being replaced one at a time
k set image deployment/ratelimiter ratelimiter=ghcr.io/yhqz1/local-rate-limiter-service:sha-0f2f8f7
k annotate deployment/ratelimiter kubernetes.io/change-cause="rollout 0f2f8f7"   # after set image, so only the new revision is labelled
k rollout status deployment/ratelimiter
vers                                            # all 0f2f8f7
k rollout history deployment/ratelimiter
```

### 2. Bad release and rollback

An invalid setting makes the new pods crash at start-up. Kubernetes keeps the healthy pods serving and never finishes the rollout.

```bash
k set env deployment/ratelimiter BURST=0        # the app rejects this: "BURST must be an integer >= 1"
k rollout status deployment/ratelimiter --timeout=50s   # times out: the rollout cannot finish
k get pods                                      # new pod CrashLoopBackOff, 3 old pods still Running
k logs <the crashing pod>                       # error: invalid configuration: BURST="0" ...

k rollout undo deployment/ratelimiter           # back to the last good revision
k rollout status deployment/ratelimiter
```

### 3. Roll back to an earlier version

```bash
k rollout history deployment/ratelimiter        # pick a revision
k rollout undo deployment/ratelimiter --to-revision=<n>
```

### Did anything fail? Ask the load generator

```bash
k logs loadgen | awk '{print $2}' | sort | uniq -c    # 200 = allowed, 429 = limited; anything else is an outage
k delete pod loadgen
```

## What I observed

On a one-node minikube cluster (Kubernetes v1.34) with the load generator running throughout:

| Scenario | Result |
| -------- | ------ |
| Rolling update `8424ac9` to `0f2f8f7` | Finished in 21 s. One new pod at a time was created, became Ready, then an old pod terminated. Old and new versions answered side by side during the switch; all 10 samples before were `8424ac9`, all 10 after were `0f2f8f7`. About 850 requests, **0 failed** |
| Bad release (`BURST=0`) | New pod went to `CrashLoopBackOff`; the 3 old pods kept serving and the Deployment stayed `Available=True`. After 60 s it reported `Progressing=False (ProgressDeadlineExceeded)` |
| `rollout undo` of the bad release | Restored the previous ReplicaSet without restarting the healthy pods. 1,439 requests across the bad release, the stall and the rollback, **0 failed** |
| Rollback to the old version, then forward again | `8424ac9` and `0f2f8f7` swapped cleanly. 744 requests, **0 failed** |

## Caveat: limits are per pod

Each pod keeps its own token buckets in memory, and the Service spreads requests across pods. Measured with one key and a burst of 10 per pod, 60 rapid requests through the Service got **30 allowed** (3 pods x 10), while the same test against a single pod got **10**. So with N replicas a key's effective limit is roughly N times the configured one. Fixing that properly means shared state (for example Redis) or routing each key to a fixed pod, which this project deliberately does not do.

## Clean up

```bash
kubectl delete -f k8s/          # remove the app
minikube delete -p ratelimiter  # remove the cluster
```
