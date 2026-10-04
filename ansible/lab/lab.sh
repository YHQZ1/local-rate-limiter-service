#!/usr/bin/env bash
# Manage the local Ansible target (a systemd + sshd container).
#   ./lab.sh up      build and start it, authorise a throwaway SSH key
#   ./lab.sh down    remove it
#   ./lab.sh status  show whether it is running
#   ./lab.sh ssh     open a shell on it
set -euo pipefail
cd "$(dirname "$0")"

NAME=rl-node1
IMAGE=rl-lab-node
SSH_PORT=${LAB_SSH_PORT:-2222}   # host port -> container sshd
APP_PORT=${LAB_APP_PORT:-18090}  # host port -> the service once deployed
KEY=./id_ed25519

case "${1:-}" in
  up)
    [ -f "$KEY" ] || ssh-keygen -q -t ed25519 -N '' -C ansible-lab -f "$KEY"
    docker build -q -t "$IMAGE" -f Dockerfile . >/dev/null
    docker rm -f "$NAME" >/dev/null 2>&1 || true
    docker run -d --name "$NAME" --hostname "$NAME" \
      --privileged --tmpfs /run --tmpfs /run/lock \
      -p "127.0.0.1:${SSH_PORT}:22" -p "127.0.0.1:${APP_PORT}:8080" \
      "$IMAGE" >/dev/null
    # Ubuntu 24.04 starts sshd on demand: ssh.socket listens, ssh.service follows.
    ssh_up() { docker exec "$NAME" systemctl is-active --quiet ssh.socket 2>/dev/null \
                 || docker exec "$NAME" systemctl is-active --quiet ssh 2>/dev/null; }
    for _ in $(seq 1 40); do ssh_up && break; sleep 0.5; done
    ssh_up || { echo "sshd did not come up" >&2; docker logs "$NAME" 2>&1 | tail -20 >&2; exit 1; }
    docker exec "$NAME" install -d -m 0700 -o ansible -g ansible /home/ansible/.ssh
    docker cp "${KEY}.pub" "$NAME:/home/ansible/.ssh/authorized_keys"
    docker exec "$NAME" chown ansible:ansible /home/ansible/.ssh/authorized_keys
    docker exec "$NAME" chmod 0600 /home/ansible/.ssh/authorized_keys
    echo "$NAME is up: ssh -i ansible/lab/id_ed25519 -p ${SSH_PORT} ansible@127.0.0.1"
    ;;
  down)
    docker rm -f "$NAME" >/dev/null 2>&1 && echo "$NAME removed" || echo "$NAME was not running"
    ;;
  status)
    docker ps --filter "name=^${NAME}$" --format '{{.Names}}: {{.Status}}' | grep . || echo "$NAME is not running"
    ;;
  ssh)
    exec ssh -i "$KEY" -p "$SSH_PORT" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null ansible@127.0.0.1
    ;;
  *)
    echo "usage: $0 {up|down|status|ssh}" >&2
    exit 2
    ;;
esac
