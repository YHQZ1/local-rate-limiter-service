# A disposable Ubuntu "server" for trying the playbook locally: real systemd
# and sshd, so Ansible talks to it exactly as it would to a VM or cloud host.
# Deliberately bare (no curl etc.) so the playbook has real work to do. The apt
# package lists are kept, as on a real server, so --check mode can resolve names.
FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
 && apt-get install -y --no-install-recommends systemd systemd-sysv openssh-server python3 python3-apt sudo \
 && mkdir -p /run/sshd

# The account Ansible logs in as: key auth only, passwordless sudo.
RUN useradd -m -s /bin/bash -u 1500 ansible \
 && echo 'ansible ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/ansible \
 && chmod 0440 /etc/sudoers.d/ansible \
 && printf 'PasswordAuthentication no\nPermitRootLogin no\n' > /etc/ssh/sshd_config.d/10-lab.conf

# Units that make no sense inside a container.
RUN systemctl mask getty.target console-getty.service systemd-udevd.service \
    systemd-udevd-kernel.socket systemd-udevd-control.socket systemd-modules-load.service

STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
