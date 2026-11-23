// SPDX-License-Identifier: Apache-2.0

package lab

// ldsoEdit is #638's instrument, an experiment: glibc's dynamic linker reports its own start-up
// (LD_DEBUG=statistics: total cycles, relocation and load cycles, relocation counts) for PID 1,
// journald, logind and root's user manager, the four that map libsystemd-shared and
// libsystemd-core on the first login's path. PID 1 and journald write it to /dev/kmsg, which
// reaches the journal the ledger keeps before journald runs and from journald itself; boot it
// with --append printk.devkmsg=on, or /dev/kmsg's per-open ratelimit (10 lines) may drop a line.
// logind and user@0 write it to their stderr, the journal: logind's DevicePolicy refuses its
// executor /dev/kmsg, and the unit failed at STDERR (lab 45180400607).
//
// PID 1 gets the variable from spin's handoff, which execs it: systemd.setenv= does not reach
// PID 1 itself. It goes after the login's edits, which write the handoff.
var ldsoEdit = Edit{
	Files: map[string]string{
		"/etc/systemd/system/systemd-journald.service.d/zz-ldso.conf": ldsoDropin + "StandardError=file:/dev/kmsg\n",
		"/etc/systemd/system/systemd-logind.service.d/zz-ldso.conf":   ldsoDropin,
		"/etc/systemd/system/user@0.service.d/zz-ldso.conf":           ldsoDropin,
	},
	Setup: `f="$MNT` + spinHandoffInit + `"
grep -qx 'exec /sbin/init' "$f" || { echo "ldso edits spin's handoff: boot it with login=spin" >&2; exit 1; }
sed -i 's|^exec /sbin/init$|export LD_DEBUG=statistics\nexec /sbin/init 2>/dev/kmsg|' "$f"
grep -qx 'exec /sbin/init 2>/dev/kmsg' "$f"
`,
}

const ldsoDropin = "[Service]\nEnvironment=LD_DEBUG=statistics\n"
