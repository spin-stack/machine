#!/usr/bin/env bash
set -euo pipefail

echo "Configuring system settings..."

echo "Creating system directories..."
mkdir -p \
    /run/sshd \
    /root/.ssh

chmod 700 /root/.ssh

echo "Configuring SSH server..."
sed -i 's/#PermitRootLogin prohibit-password/PermitRootLogin yes/' /etc/ssh/sshd_config
sed -i 's/#PasswordAuthentication yes/PasswordAuthentication no/' /etc/ssh/sshd_config
sed -i 's/#PubkeyAuthentication yes/PubkeyAuthentication yes/' /etc/ssh/sshd_config

# docs/image.md, "Users and sessions"
mkdir -p /etc/ssh/sshd_config.d
cat <<'EOF' > /etc/ssh/sshd_config.d/10-spin-user-ca.conf
TrustedUserCAKeys /etc/ssh/spin_user_ca.pub
EOF

cat <<'EOF' > /etc/ssh/sshd_config.d/10-spin-kex.conf
KexAlgorithms mlkem768x25519-sha256
EOF

grep -qE '^[[:space:]]*Include[[:space:]]+/etc/ssh/sshd_config\.d/\*\.conf' /etc/ssh/sshd_config || {
    echo "ERROR: sshd_config does not include sshd_config.d; the user CA would be ignored" >&2
    exit 1
}

echo "Configuring network interface naming..."
mkdir -p /etc/systemd/network/99-default.link.d
cat <<'EOF' > /etc/systemd/network/99-default.link.d/traditional-naming.conf
[Link]
NamePolicy=keep kernel
EOF

# docs/image.md, "Users and sessions"
echo "Creating the workspace user..."
useradd --create-home --shell /bin/bash --uid 1000 --groups sudo,docker spin
passwd --lock root
passwd --lock spin

# docs/image.md, "Users and sessions"
install -d -m 0755 /var/lib/systemd/linger
touch /var/lib/systemd/linger/root

# docs/image.md, "The lane"
echo "The first login's lane..."
grep -qx 'After=sysinit.target' /usr/lib/systemd/system/dbus.socket
sed -e '/^Wants=sysinit.target$/d' -e '/^After=sysinit.target$/d' \
    /usr/lib/systemd/system/dbus.socket > /etc/systemd/system/dbus.socket
# docs/image.md, "udev"
for u in systemd-udevd.service systemd-udev-trigger.service; do
    grep -qx 'Before=sysinit.target' "/usr/lib/systemd/system/$u"
    sed '/^Before=sysinit.target$/d' "/usr/lib/systemd/system/$u" > "/etc/systemd/system/$u"
done
# docs/image.md, "Users and sessions"
ln -sf /dev/null /etc/tmpfiles.d/systemd-nologin.conf
sed -i 's/^\(session.*pam_systemd\.so\)\(.*\)$/\1\2 class=user-early/' /etc/pam.d/common-session
grep -q '^session.*pam_systemd\.so.*class=user-early$' /etc/pam.d/common-session

install -m 0440 /dev/stdin /etc/sudoers.d/spin <<'EOF'
spin ALL=(ALL) NOPASSWD: ALL
EOF

# docs/image.md, "Users and sessions"
echo "Removing the MOTD..."
rm -rf /etc/update-motd.d/*
truncate -s 0 /etc/motd
sed -i 's/^\(session.*pam_motd\.so\)/#\1/' /etc/pam.d/sshd /etc/pam.d/login

# docs/image.md, "Timers"
echo "Masking the distribution's background maintenance timers..."
for unit in apt-daily.timer apt-daily-upgrade.timer motd-news.timer dpkg-db-backup.timer; do
    ln -sf /dev/null "/etc/systemd/system/$unit"
done

# docs/image.md, "Timers"
echo "Running fstrim hourly..."
mkdir -p /etc/systemd/system/fstrim.timer.d
cat > /etc/systemd/system/fstrim.timer.d/spin.conf <<'UNIT'
[Timer]
OnCalendar=
OnCalendar=hourly
RandomizedDelaySec=10min
UNIT

echo "✅ System configuration complete"
