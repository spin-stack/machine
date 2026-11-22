// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"fmt"
	"strings"

	xssh "golang.org/x/crypto/ssh"
)

// managerDiagScript prints the guest's CLOCK_MONOTONIC timestamps (µs) of root's user manager's
// start, as "key value" lines (experiment #467).
const managerDiagScript = `systemctl show -p Id -p InactiveExitTimestampMonotonic -p ExecMainStartTimestampMonotonic -p ActiveEnterTimestampMonotonic user-runtime-dir@0.service user@0.service | awk -F= '$1=="Id"{id=$2;next} NF>1{print id"."$1, $2}'
systemctl --user show -p UserspaceTimestampMonotonic -p GeneratorsStartTimestampMonotonic -p GeneratorsFinishTimestampMonotonic -p UnitsLoadStartTimestampMonotonic -p UnitsLoadFinishTimestampMonotonic -p FinishTimestampMonotonic | awk -F= 'NF>1{print "manager."$1, $2}'
systemctl --user show -p Id -p InactiveExitTimestampMonotonic -p ActiveEnterTimestampMonotonic dbus.socket ssh-agent.socket systemd-ask-password.socket sockets.target basic.target default.target | awk -F= '$1=="Id"{id=$2;next} NF>1{print "u:"id"."$1, $2}'
`

func managerDiag(m *Measurement, cid uint32) {
	_, out, err := m.Key.Run(cid, xssh.KeyExchangeMLKEM768X25519, managerDiagScript)
	if err != nil {
		m.Logf("manager diag: %v", err)
		return
	}
	v := map[string]float64{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		var x float64
		if _, err := fmt.Sscan(f[1], &x); err == nil && x > 0 {
			v[f[0]] = x
		}
	}
	span := func(metric, from, to string) {
		a, okA := v[from]
		b, okB := v[to]
		if okA && okB {
			m.Record(metric, (b-a)/1000)
		}
	}
	span("mgr_total", "user@0.service.InactiveExitTimestampMonotonic", "user@0.service.ActiveEnterTimestampMonotonic")
	span("mgr_spawn", "user@0.service.InactiveExitTimestampMonotonic", "manager.UserspaceTimestampMonotonic")
	span("mgr_pre_gen", "manager.UserspaceTimestampMonotonic", "manager.GeneratorsStartTimestampMonotonic")
	span("mgr_generators", "manager.GeneratorsStartTimestampMonotonic", "manager.GeneratorsFinishTimestampMonotonic")
	span("mgr_load", "manager.UnitsLoadStartTimestampMonotonic", "manager.UnitsLoadFinishTimestampMonotonic")
	span("mgr_to_basic", "manager.UnitsLoadFinishTimestampMonotonic", "u:basic.target.ActiveEnterTimestampMonotonic")
	span("mgr_ready_lag", "u:basic.target.ActiveEnterTimestampMonotonic", "user@0.service.ActiveEnterTimestampMonotonic")
	span("mgr_finish", "manager.UserspaceTimestampMonotonic", "manager.FinishTimestampMonotonic")
	span("runtime_dir", "user-runtime-dir@0.service.InactiveExitTimestampMonotonic", "user-runtime-dir@0.service.ActiveEnterTimestampMonotonic")
	span("u_dbus_socket", "u:dbus.socket.InactiveExitTimestampMonotonic", "u:dbus.socket.ActiveEnterTimestampMonotonic")
	span("u_agent_socket", "u:ssh-agent.socket.InactiveExitTimestampMonotonic", "u:ssh-agent.socket.ActiveEnterTimestampMonotonic")
	span("u_sockets", "manager.UnitsLoadFinishTimestampMonotonic", "u:sockets.target.ActiveEnterTimestampMonotonic")
	if m.Rep == 0 {
		m.Logf("manager diag raw:\n%s", out)
		const units = "systemctl --user list-units --all --plain --no-legend; ls /etc/systemd/user /usr/lib/systemd/user-generators /usr/lib/systemd/user-environment-generators; cat /etc/pam.d/systemd-user /usr/lib/pam.d/systemd-user /etc/pam.d/common-session 2>&1; systemctl --user show-environment; journalctl -b -o short-monotonic --no-pager _SYSTEMD_UNIT=user@0.service | head -400; journalctl -b -o short-monotonic --no-pager -u user@0.service -u user-runtime-dir@0.service -u systemd-logind.service | head -60; true"
		if _, list, err := m.Key.Run(cid, xssh.KeyExchangeMLKEM768X25519, units); err == nil {
			m.Logf("user units:\n%s", list)
		}
	}
}
