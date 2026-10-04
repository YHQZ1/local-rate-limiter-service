# Ansible: configuring the ratelimiter server

Ansible turns a bare Ubuntu machine into a running, hardened ratelimiter server, and keeps it that way. Run the playbook once to build the server, run it again any time to repair drift. It only uses built-in modules, so there are no collections to install.

## What it does

| Assignment item | Where |
| --------------- | ----- |
| Install packages | `roles/common`: `ca-certificates`, `curl`, `jq` via `apt` |
| Create users | `roles/ratelimiter`: a locked-down `ratelimiter` system user and group (no login shell, no home directory contents) |
| Manage files | `/etc/ratelimiter/ratelimiter.env` (config), `/etc/systemd/system/ratelimiter.service` (sandboxed unit), `/etc/motd`, the versioned release directories and the `current` symlink |
| Inventory | `inventory/hosts.yml` and `inventory/group_vars/ratelimiter.yml` |

The play also installs the binary, starts and enables the service, and verifies it: it waits for `/health`, then asserts `/version` is the version it just deployed.

```
ansible/
├── ansible.cfg
├── site.yml                      the playbook
├── inventory/
│   ├── hosts.yml                 which servers, how to reach them
│   └── group_vars/ratelimiter.yml  the settings for those servers
├── roles/
│   ├── common/                   packages, login banner
│   └── ratelimiter/              user, files, binary, systemd service, health check
├── lab/                          a disposable Ubuntu "server" to try it on
└── requirements.txt              pinned Ansible + ansible-lint
```

## Try it locally

You need Docker and Python 3. The lab is an Ubuntu 24.04 container running real systemd and sshd, so Ansible reaches it over SSH exactly as it would a VM.

```bash
# 1. Tooling (a local virtualenv, nothing system-wide)
python3 -m venv ansible/.venv
ansible/.venv/bin/pip install -r ansible/requirements.txt

# 2. A fresh server to configure (also generates a throwaway SSH key, git-ignored)
ansible/lab/lab.sh up

# 3. A Linux binary for the playbook to install
make dist

# 4. Configure it
cd ansible
.venv/bin/ansible-playbook site.yml --check --diff   # dry run: shows what WOULD change
.venv/bin/ansible-playbook site.yml                  # for real
.venv/bin/ansible-playbook site.yml                  # again: changed=0, it is idempotent
```

The service is published on the Mac at http://localhost:18090. Run the project's smoke test against it:

```bash
scripts/smoke.sh http://localhost:18090 local
```

Tear the lab down with `ansible/lab/lab.sh down`.

## Variables

Set these in `inventory/group_vars/ratelimiter.yml`, per host in `hosts.yml`, or on the command line with `-e`. Defaults live in `roles/ratelimiter/defaults/main.yml`.

| Variable | Default | Meaning |
| -------- | ------- | ------- |
| `ratelimiter_version` | `""` | Release tag to download (for example `v1.0.0`). Empty installs the local `bin/ratelimiter_linux_<arch>` instead |
| `ratelimiter_rate_per_sec` | `5` | Tokens refilled per second, per key |
| `ratelimiter_burst` | `10` | Bucket size, per key |
| `ratelimiter_port` | `8080` | Listen port |
| `ratelimiter_max_keys` | `100000` | Distinct keys tracked |
| `ratelimiter_log_level` | `info` | `debug`, `info`, `warn`, `error` |
| `ratelimiter_cleanup_interval` / `ratelimiter_shutdown_timeout` | `30s` / `10s` | Janitor interval, graceful shutdown limit |

## How deployments work

Every version is installed in its own directory and a symlink selects the live one:

```
/opt/ratelimiter/
├── current -> releases/v1.2.0        what systemd runs
└── releases/
    ├── v1.1.0/ratelimiter
    └── v1.2.0/ratelimiter
```

- **Deploy a release:** `ansible-playbook site.yml -e ratelimiter_version=v1.2.0`. The binary is downloaded from the GitHub release and verified against that release's `checksums.txt`; a mismatch aborts the run and leaves the running service untouched.
- **Roll back:** run the playbook with the previous version. Its directory is still on disk, so nothing is downloaded and `current` is simply repointed.
- **Change configuration:** edit the variables and re-run. Only the config file changes, and the service is restarted once.

## What I checked

All on the lab host, with ansible-core 2.21.4:

- The dry run changes nothing on the host, and reports the 18 packages, user, directories and files it would create.
- First run on the bare server: `changed=11, failed=0`. Second run: `changed=0`.
- **Drift repair:** I changed the config by hand, stopped the service and deleted the banner. One run restored the file contents and its permissions (`666` back to `640`), the banner, and the running service.
- Changing the burst and rate through variables changed only the config file, with a single restart.
- `kill -9` on the service: systemd restarts it within seconds.
- A release served from a local stand-in (laid out as the CI release stage lays it out) deployed and verified. A release with a wrong checksum was refused and the running service was untouched.
- `systemd-analyze security` rates the unit **1.5 (OK)**.
- `ansible-lint` passes at the strictest (`production`) profile.

## Pointing it at a real server

Add the host to `inventory/hosts.yml` with its address and login user (the commented example is in the file), and remove the two lab-only SSH options (`StrictHostKeyChecking=no`, `UserKnownHostsFile=/dev/null`). The login user needs passwordless `sudo`, and the target needs `python3` (and `python3-apt` for `--check` mode, which Ubuntu cloud images include).

## Limitations

- Debian and Ubuntu only (the play asserts this).
- The lab is a container, not a VM, so the lab host shares the Mac's kernel and the sandbox score is measured there.
- A refused or failed download leaves an empty `releases/<version>` directory behind. It is harmless and gets reused by the next attempt.
