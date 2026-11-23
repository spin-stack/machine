// SPDX-License-Identifier: Apache-2.0

package lab

// Batch 3's image arms, each one change to a published release's overlay (exp-batch3, never
// merged): #513, #514, #401 and #574's seccomp bound.

// stampsEdit writes /etc/.updated and /var/.updated with /usr's mtime, as systemd-update-done
// writes them, and unmasks the five units of ConditionNeedsUpdate, which then skip themselves
// (#513).
var stampsEdit = Edit{Setup: `for u in ldconfig.service systemd-sysusers.service systemd-hwdb-update.service \
	systemd-journal-catalog-update.service systemd-update-done.service; do
	test "$(readlink "$MNT/etc/systemd/system/$u")" = /dev/null
	grep -q '^ConditionNeedsUpdate=' "$MNT/usr/lib/systemd/system/$u"
	rm "$MNT/etc/systemd/system/$u"
done
test ! -e "$MNT/etc/.updated" && test ! -e "$MNT/var/.updated"
ns=$(stat -c %.9Y "$MNT/usr" | tr -d . | sed 's/^0*//')
ns=${ns:-0}
for d in etc var; do
	printf 'TIMESTAMP_NSEC=%s\n' "$ns" > "$MNT/$d/.updated"
	touch -r "$MNT/usr" "$MNT/$d/.updated"
done
`}

// journalSizeEdit raises the volatile journal's cap from 4 MB (#514).
var journalSizeEdit = Edit{Setup: `f="$MNT/etc/systemd/journald.conf.d/volatile.conf"
grep -qx 'RuntimeMaxUse=4M' "$f"
sed -i 's/^RuntimeMaxUse=4M$/RuntimeMaxUse=16M/' "$f"
grep -qx 'RuntimeMaxUse=16M' "$f"
`}

// pressureRestEdit starts the pressure sender with multi-user.target, which the boot's rest
// starts after the login's units, and deletes its timer (#401).
var pressureRestEdit = Edit{Setup: `w="$MNT/etc/systemd/system/timers.target.wants/spin-machine-pressure.timer"
test "$(readlink "$w")" = /usr/lib/systemd/system/spin-machine-pressure.timer
rm "$w" "$MNT/usr/lib/systemd/system/spin-machine-pressure.timer"
test -x "$MNT/usr/libexec/spin-machine-pressure"
printf '\n[Install]\nWantedBy=multi-user.target\n' >> "$MNT/usr/lib/systemd/system/spin-machine-pressure.service"
mkdir -p "$MNT/etc/systemd/system/multi-user.target.wants"
ln -s /usr/lib/systemd/system/spin-machine-pressure.service "$MNT/etc/systemd/system/multi-user.target.wants/spin-machine-pressure.service"
`}

// seccompBoundEdit empties journald's, logind's and user@'s seccomp and sandbox settings, a bound
// on what their executors spend on them (#574, experiment 4). A bound, never a candidate.
const seccompBoundDropIn = `[Service]
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
ProtectControlGroups=no
ProtectHome=no
ProtectHostname=no
ProtectKernelLogs=no
ProtectKernelModules=no
ProtectKernelTunables=no
ProtectSystem=no
ProtectProc=default
`

var seccompBoundEdit = Edit{
	Files: map[string]string{
		"/etc/systemd/system/systemd-journald.service.d/zz-seccomp-bound.conf": seccompBoundDropIn,
		"/etc/systemd/system/systemd-logind.service.d/zz-seccomp-bound.conf":   seccompBoundDropIn,
		"/etc/systemd/system/user@.service.d/zz-seccomp-bound.conf":            seccompBoundDropIn,
	},
	Setup: `grep -q '^SystemCallFilter=' "$MNT/usr/lib/systemd/system/systemd-journald.service"
grep -q '^SystemCallFilter=' "$MNT/usr/lib/systemd/system/systemd-logind.service"
test -e "$MNT/usr/lib/systemd/system/user@.service"
`,
}
