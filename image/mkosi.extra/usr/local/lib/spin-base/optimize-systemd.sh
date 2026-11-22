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
    systemd-journal-catalog-update.service

    systemd-fsck-root.service
    systemd-update-done.service

    systemd-modules-load.service
    systemd-hibernate-clear.service
    systemd-pstore.service
    systemd-hwdb-update.service

    systemd-firstboot.service
    systemd-machine-id-commit.service

    systemd-random-seed.service

    ldconfig.service

    systemd-sysusers.service

    modprobe@configfs.service
    modprobe@drm.service
    modprobe@fuse.service
    modprobe@efi_pstore.service

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

# docs/image.md, "Units, generators and the manager"
log "Configuring systemd manager timeouts..."
mkdir -p /etc/systemd/system.conf.d

cat <<'EOF' >/etc/systemd/system.conf.d/fast-boot.conf
[Manager]
DefaultTimeoutStartSec=15s
DefaultTimeoutStopSec=10s
DefaultDeviceTimeoutSec=10s

RuntimeWatchdogSec=0
ShutdownWatchdogSec=0
EOF

log "Configuring journald for minimal overhead..."
mkdir -p /etc/systemd/journald.conf.d

cat <<'EOF' >/etc/systemd/journald.conf.d/volatile.conf
[Journal]
Storage=volatile
RuntimeMaxUse=4M
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
ln -sf /etc/systemd/system/docker.socket /etc/systemd/system/sockets.target.wants/docker.socket

# docs/image.md, "The host keys"
mkdir -p /etc/systemd/system/sysinit.target.wants
ln -sf /usr/lib/systemd/system/spin-machine-sshd-keygen.service \
    /etc/systemd/system/sysinit.target.wants/spin-machine-sshd-keygen.service
# docs/image.md, "The pressure sender"
mkdir -p /etc/systemd/system/timers.target.wants
ln -sf /usr/lib/systemd/system/spin-machine-pressure.timer \
    /etc/systemd/system/timers.target.wants/spin-machine-pressure.timer

log "Setting default target to multi-user..."
ln -sf /lib/systemd/system/multi-user.target /etc/systemd/system/default.target

log "Optimizing nsswitch.conf..."
cat <<'EOF' >/etc/nsswitch.conf
passwd:         files
group:          files
shadow:         files
gshadow:        files
hosts:          files dns
networks:       files
protocols:      files
services:       files
ethers:         files
rpc:            files
EOF

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
