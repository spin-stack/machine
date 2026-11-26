#!/usr/bin/env bash
# docs/image.md, "Units masked"
set -euo pipefail
IFS=$'\n\t'

log()   { printf '[%s] %s\n' "$(date +%T)" "$*"; }

MASK_DIR="/etc/systemd/system"
mkdir -p "$MASK_DIR"

mask_unit() {
    local unit=$1
    log "Masking $unit"
    ln -sf /dev/null "$MASK_DIR/$unit"
}

MASK_UNITS=(
    systemd-timedated.service

    systemd-journal-flush.service

    systemd-fsck-root.service

    systemd-modules-load.service
    systemd-hibernate-clear.service
    systemd-pstore.service

    systemd-firstboot.service
    systemd-machine-id-commit.service

    systemd-random-seed.service

    modprobe@configfs.service
    modprobe@drm.service
    modprobe@fuse.service
    modprobe@efi_pstore.service
    kmod-static-nodes.service

    sys-kernel-debug.mount
    sys-kernel-tracing.mount

    systemd-tmpfiles-setup-dev.service
    systemd-tmpfiles-setup-dev-early.service

    dev-hugepages.mount
    dev-mqueue.mount
)

log "Masking unnecessary systemd units..."
for unit in "${MASK_UNITS[@]}"; do
    mask_unit "$unit"
done

log "Disabling unnecessary systemd generators..."
mkdir -p /etc/systemd/system-generators

DISABLE_GENERATORS=(
    systemd-fstab-generator
    systemd-debug-generator
    systemd-gpt-auto-generator
    systemd-sysv-generator
    systemd-rc-local-generator
    systemd-hibernate-resume-generator
    systemd-system-update-generator
    systemd-factory-reset-generator
)

for gen in "${DISABLE_GENERATORS[@]}"; do
    ln -sf /dev/null "/etc/systemd/system-generators/$gen"
    log "Disabled generator: $gen"
done

# Root's manager starts in the boot (root lingers) and the first login waits for it, so what it
# runs is on the login's clock. The XDG autostart generator reads .desktop files for graphical
# sessions, which a workspace has none of: first login -3.8 [-6.2, -0.7], 100 a side (bench,
# spin's shape, against v20261122.06, lab 45070150688, #527).
mkdir -p /etc/systemd/user-generators
ln -sf /dev/null /etc/systemd/user-generators/systemd-xdg-autostart-generator

log "Masking unnecessary tmpfiles rules..."
mkdir -p /etc/tmpfiles.d

MASK_TMPFILES=(
    systemd-pstore.conf
    systemd-network.conf
    x11.conf
)

for conf in "${MASK_TMPFILES[@]}"; do
    ln -sf /dev/null "/etc/tmpfiles.d/$conf"
done

log "Configuring journald for minimal overhead..."
mkdir -p /etc/systemd/journald.conf.d

cat <<'EOF' >/etc/systemd/journald.conf.d/volatile.conf
[Journal]
Storage=volatile
RuntimeMaxUse=8M
RuntimeMaxFileSize=1M

Compress=no
Seal=no
SyncIntervalSec=0

RateLimitBurst=10000
RateLimitIntervalSec=30s

ForwardToConsole=no
ForwardToWall=no
ForwardToSyslog=no
ForwardToKMsg=no

SplitMode=none
Audit=no
LineMax=48K
EOF

log "Configuring Docker and containerd for socket activation..."

rm -f /etc/systemd/system/multi-user.target.wants/docker.service 2>/dev/null || true
rm -f /etc/systemd/system/multi-user.target.wants/containerd.service 2>/dev/null || true

mkdir -p /etc/systemd/system/sockets.target.wants
ln -sf /etc/systemd/system/containerd.socket /etc/systemd/system/sockets.target.wants/containerd.socket

# docs/image.md, "The host keys"
mkdir -p /etc/systemd/system/sysinit.target.wants
ln -sf /usr/lib/systemd/system/spin-machine-sshd-keygen.service \
    /etc/systemd/system/sysinit.target.wants/spin-machine-sshd-keygen.service
# docs/image.md, "The pressure sender"
mkdir -p /etc/systemd/system/multi-user.target.wants
ln -sf /usr/lib/systemd/system/spin-machine-pressure.service \
    /etc/systemd/system/multi-user.target.wants/spin-machine-pressure.service
# docs/image.md, "Kernel modules"
ln -sf /usr/lib/systemd/system/spin-machine-static-nodes.service \
    /etc/systemd/system/multi-user.target.wants/spin-machine-static-nodes.service

# docs/image.md, "The first login's target"
log "Setting default target to the first login's..."
ln -sf /usr/lib/systemd/system/spin-machine-login.target /etc/systemd/system/default.target

log "Pre-warming caches..."
ldconfig || true
locale-gen en_US.UTF-8 2>/dev/null || true
update-locale LANG=en_US.UTF-8 2>/dev/null || true
iconvconfig 2>/dev/null || true
fc-cache -f 2>/dev/null || true

log "Running cleanup..."
rm -rf /usr/share/doc/* 2>/dev/null || true
rm -rf /usr/share/man/* 2>/dev/null || true
rm -rf /var/lib/apt/lists/* 2>/dev/null || true
rm -rf /var/log/journal/* 2>/dev/null || true

log "Boot optimization complete!"
