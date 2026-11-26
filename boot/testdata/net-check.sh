#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
set -eu
# What brought the network up, or did not, before the verdict.
failed() {
	systemctl --no-pager status systemd-network-generator.service systemd-networkd.service || true
	ls -l /run/systemd/network /run/udev/rules.d || true
	journalctl -b --no-pager -o cat -u systemd-networkd -u systemd-network-generator | tail -n 30 || true
	echo NET_CHECK_FAILED
}
trap failed EXIT

# NET is "passt" for a machine with a NIC on passt (machine.NIC.Passt), "none" for one without a
# NIC (net_test.go).
echo "interfaces: $(ls /sys/class/net | tr '\n' ' ')"
case "$NET" in
none)
	if ls /sys/class/net | grep -q '^eth'; then
		echo "a machine without a NIC has one"
		exit 1
	fi
	trap - EXIT
	echo NET_CHECK_OK
	exit 0
	;;
passt) ;;
*) echo "NET is passt or none, not '$NET'"; exit 1 ;;
esac

# networkd applies the address the command line gave (machine.passtGuest) once the link is up.
for _ in $(seq 100); do
	ip -4 addr show dev eth0 | grep -q 'inet 10.0.2.15/24' && break
	sleep 0.1
done
ip addr show dev eth0
ip route
cat /etc/resolv.conf
ip -4 addr show dev eth0 | grep -q 'inet 10.0.2.15/24'
ip route | grep -q '^default via 10.0.2.2 dev eth0'
grep -qx 'nameserver 10.0.2.3' /etc/resolv.conf

getent hosts github.com
status=$(curl -sSI --max-time 20 https://github.com | head -n 1)
echo "curl: $status"
case "$status" in
HTTP/*) ;;
*) echo "curl https://github.com said no status line"; exit 1 ;;
esac
trap - EXIT
echo NET_CHECK_OK
