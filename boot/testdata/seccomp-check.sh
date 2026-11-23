#!/bin/sh
# boot:seccomp's guest half: each unit's probe table (boot/seccompprobe) and the seccomp filters
# its main process carries, on the console for the test to compare.
set -u
fail() { echo "SECCOMP_CHECK_FAILED: $*"; exit 1; }
for u in systemd-journald systemd-logind user@0; do
	f=/run/spin-seccomp-$u.txt
	n=0
	while [ ! -e "$f" ]; do
		n=$((n + 1))
		[ "$n" -gt 120 ] && fail "no table from $u's probe"
		sleep 0.5
	done
	pid=$(systemctl show -P MainPID "$u.service")
	[ "${pid:-0}" -gt 0 ] || fail "$u.service has no main process"
	main=$(sed -n 's/^Seccomp_filters:[[:space:]]*//p' "/proc/$pid/status")
	[ -n "$main" ] || fail "no Seccomp_filters for $u's main process $pid"
	echo "SECCOMP-FILTERS $u main=$main probe=$(sed -n 's/^filters //p' "$f")"
	echo "SECCOMP-TABLE $u $(grep -v '^filters ' "$f" | gzip -9 | base64 -w0)"
done
{ journalctl -b -o cat; dmesg; } | grep -o 'spin-seccomp-mark.*' | sed 's/^/SECCOMP-MARK /' || true
echo SECCOMP_CHECK_OK
