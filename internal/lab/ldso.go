// SPDX-License-Identifier: Apache-2.0

package lab

// ldsoEdit is #638's instrument, an experiment: glibc's dynamic linker reports its own start-up
// (LD_DEBUG=statistics: total cycles, relocation and load cycles, relocation counts) for PID 1,
// journald, logind and root's user manager, the four that map libsystemd-shared and
// libsystemd-core on the first login's path. Each writes to /dev/kmsg, opened by the executor
// before any sandbox (StandardError=file:), so the report reaches the journal the ledger keeps
// even from journald itself and from PID 1 before journald runs. Boot it with
// --append printk.devkmsg=on, or /dev/kmsg's per-open ratelimit (10 lines) may drop a line.
//
// PID 1 gets the variable from spin's handoff, which execs it: systemd.setenv= does not reach
// PID 1 itself. It goes after the login's edits, which write the handoff.
var ldsoEdit = Edit{
	Files: map[string]string{
		"/etc/systemd/system/systemd-journald.service.d/zz-ldso.conf": ldsoDropin,
		"/etc/systemd/system/systemd-logind.service.d/zz-ldso.conf":   ldsoDropin,
		"/etc/systemd/system/user@0.service.d/zz-ldso.conf":           ldsoDropin,
	},
	Setup: `f="$MNT` + spinHandoffInit + `"
grep -qx 'exec /sbin/init' "$f" || { echo "ldso edits spin's handoff: boot it with login=spin" >&2; exit 1; }
sed -i 's|^exec /sbin/init$|export LD_DEBUG=statistics\nexec /sbin/init 2>/dev/kmsg|' "$f"
grep -qx 'exec /sbin/init 2>/dev/kmsg' "$f"
`,
}

const ldsoDropin = "[Service]\nEnvironment=LD_DEBUG=statistics\nStandardError=file:/dev/kmsg\n"
