// SPDX-License-Identifier: Apache-2.0

package lab

import "maps"

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

// The carried forms of #574's first experiment, one trigger for the rest of the boot each. The
// target is the image's candidate: the login's units, spin's supervisor by name (spin-stack/spin's
// deploy/guest/extension/supervisor), and the trigger. The forwarder stands for spin's supervisor,
// so it is wanted by the target as the name would want spin's; earlyForwarder takes it off
// basic.target, as spin would have to take its supervisor.
const loginTarget = `[Unit]
Description=What a workspace's first login needs
DefaultDependencies=no
Wants=ssh.socket ssh.service spin-machine-sshd-keygen.service
Wants=systemd-journald.service systemd-logind.service dbus.socket dbus-broker.service tmp.mount
Wants=spin-supervisor.service
`

const bootRest = `[Unit]
Description=The rest of the boot, after the first login
DefaultDependencies=no
[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/bin/systemctl --no-block start multi-user.target
`

func loginFirst(trigger map[string]string, earlyForwarder bool, setup string) Edit {
	files := map[string]string{"/etc/systemd/system/spin-login.target": loginTarget}
	maps.Copy(files, trigger)
	if earlyForwarder {
		files["/etc/systemd/system/spin-lab-forward.service.d/zz-early.conf"] = "[Unit]\nDefaultDependencies=no\nConflicts=shutdown.target\nBefore=shutdown.target\n"
	}
	return Edit{
		Files: files,
		Links: map[string]string{
			"/etc/systemd/system/default.target":                                   "/etc/systemd/system/spin-login.target",
			"/etc/systemd/system/spin-login.target.wants/spin-lab-forward.service": "/etc/systemd/system/spin-lab-forward.service",
		},
		Setup: `test -e "$MNT/etc/systemd/system/spin-lab-forward.service" || { echo "the login-first edits stand on spin's path: boot them with login=spin" >&2; exit 1; }
` + setup,
	}
}

// fallback starts the rest 2 s after the boot when no login has.
var fallback = map[string]string{
	"/etc/systemd/system/spin-boot-rest.timer":          "[Unit]\nDescription=The rest of the boot, when no login came\nDefaultDependencies=no\n[Timer]\nOnBootSec=2s\nAccuracySec=1ms\n",
	"/etc/systemd/system/spin-login.target.d/rest.conf": "[Unit]\nWants=spin-boot-rest.timer\n",
}

// idleTrigger: the rest once the boot's first jobs are done (Type=idle, at most 5 s later).
var idleTrigger = map[string]string{
	"/etc/systemd/system/spin-boot-rest.service":        bootRest + "Type=idle\n",
	"/etc/systemd/system/spin-login.target.d/rest.conf": "[Unit]\nWants=spin-boot-rest.service\n",
}

// timerTrigger: the rest 2 s after the boot, whether or not a login came.
var timerTrigger = merged(fallback, map[string]string{"/etc/systemd/system/spin-boot-rest.service": bootRest})

// pamTrigger: the rest when sshd opens a session, the first login's, and 2 s after the boot when none
// has. sshd's PAM stack starts the service, which is a no-op once it has run.
var pamTrigger = merged(fallback, map[string]string{"/etc/systemd/system/spin-boot-rest.service": bootRest})

const pamSetup = `grep -q '^@include common-session$' "$MNT/etc/pam.d/sshd"
sed -i 's|^@include common-session$|@include common-session\nsession optional pam_exec.so quiet /usr/bin/systemctl --no-block start spin-boot-rest.service|' "$MNT/etc/pam.d/sshd"
grep -q 'spin-boot-rest.service' "$MNT/etc/pam.d/sshd"
`

func merged(a, b map[string]string) map[string]string {
	m := maps.Clone(a)
	maps.Copy(m, b)
	return m
}
