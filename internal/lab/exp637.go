// SPDX-License-Identifier: Apache-2.0

package lab

// #637's split of #574's sandbox bound into its two halves, each still a bound, never a candidate:
// the settings systemd's executor enforces with seccomp, and the ones it enforces with a mount
// namespace. Each is a drop-in for journald, logind and user@.

const seccompHalfDropIn = `[Service]
SystemCallFilter=
SystemCallArchitectures=
SystemCallErrorNumber=
RestrictAddressFamilies=
RestrictNamespaces=no
RestrictRealtime=no
RestrictSUIDSGID=no
MemoryDenyWriteExecute=no
LockPersonality=no
ProtectClock=no
ProtectKernelModules=no
ProtectKernelLogs=no
ProtectHostname=no
`

const namespaceHalfDropIn = `[Service]
ProtectSystem=no
ProtectHome=no
ProtectControlGroups=no
ProtectKernelTunables=no
ProtectProc=default
`

func halfEdit(name, dropIn string) Edit {
	return Edit{
		Files: map[string]string{
			"/etc/systemd/system/systemd-journald.service.d/zz-" + name + ".conf": dropIn,
			"/etc/systemd/system/systemd-logind.service.d/zz-" + name + ".conf":   dropIn,
			"/etc/systemd/system/user@.service.d/zz-" + name + ".conf":            dropIn,
		},
		Setup: `grep -q '^SystemCallFilter=' "$MNT/usr/lib/systemd/system/systemd-journald.service"
grep -q '^SystemCallFilter=' "$MNT/usr/lib/systemd/system/systemd-logind.service"
grep -q '^ProtectSystem=strict' "$MNT/usr/lib/systemd/system/systemd-logind.service"
test -e "$MNT/usr/lib/systemd/system/user@.service"
`,
	}
}

var (
	seccompHalfEdit   = halfEdit("seccomp-half", seccompHalfDropIn)
	namespaceHalfEdit = halfEdit("namespace-half", namespaceHalfDropIn)
)
