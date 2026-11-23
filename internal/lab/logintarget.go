// SPDX-License-Identifier: Apache-2.0

package lab

// loginFirstBound is #574's first experiment as a bound: the boot's default target holds only what
// spin's first login needs, and multi-user.target is started 3 s after the boot, long after the
// probes' first login. Every unit still runs. The units in the target, and the ones they are
// ordered against, carry no default dependencies, so PID 1 neither starts nor loads sysinit.target,
// basic.target and what they reach before the login. It is a bound, not a candidate: the timer
// stands for "after the login", and the forwarder, spin's supervisor, is taken off basic.target.
var loginFirstBound = Edit{
	Files: map[string]string{
		"/etc/systemd/system/spin-login.target": `[Unit]
Description=What the first login needs
DefaultDependencies=no
Wants=ssh.socket ssh.service spin-lab-forward.service spin-machine-sshd-keygen.service
Wants=systemd-journald.service systemd-logind.service dbus.socket dbus-broker.service
Wants=spin-boot-rest.timer
`,
		"/etc/systemd/system/spin-boot-rest.timer": `[Unit]
Description=The rest of the boot, after the first login
DefaultDependencies=no
[Timer]
OnBootSec=3s
AccuracySec=1ms
`,
		"/etc/systemd/system/spin-boot-rest.service": `[Unit]
Description=The rest of the boot, after the first login
DefaultDependencies=no
[Service]
Type=oneshot
ExecStart=/usr/bin/systemctl --no-block start multi-user.target
`,
		"/etc/systemd/system/spin-lab-forward.service.d/zz-early.conf":      "[Unit]\nDefaultDependencies=no\nConflicts=shutdown.target\nBefore=shutdown.target\n",
		"/etc/systemd/system/systemd-user-sessions.service.d/zz-early.conf": "[Unit]\nDefaultDependencies=no\nConflicts=shutdown.target\nBefore=shutdown.target\n",
	},
	Links: map[string]string{
		"/etc/systemd/system/default.target": "/etc/systemd/system/spin-login.target",
	},
	Setup: `test -e "$MNT/etc/systemd/system/spin-lab-forward.service" || { echo "login-first-bound stands on spin's path: boot it with login=spin" >&2; exit 1; }
`,
}
