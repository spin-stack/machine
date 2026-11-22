// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	xssh "golang.org/x/crypto/ssh"
)

// chain is an experiment's diagnostic, not a goal number: ssh-first's boot, and after its first
// login, read from the guest, when each unit started and became active on the guest's clock and
// how much CPU each cgroup had used. On one vCPU everything before the login shares the CPU, so
// the CPU column says what the login waited for.
type chain struct{}

func (chain) Name() string { return "chain" }
func (chain) About() string {
	return "ssh-first's boot, then each unit's start, active and CPU read from the guest after the first login"
}
func (chain) Options() []Option     { return shape }
func (chain) Rows(Options) []string { return []string{"boot"} }
func (chain) Phases() int           { return 1 }
func (chain) Needs(o Options) []Need {
	return append(append(bootNeeds, Need{"boot", "vsock-cid"}, Need{"boot", "console"}, Need{"boot", "append"}), plugNeeds(o)...)
}

func (chain) Metrics() []Metric {
	return []Metric{
		{"sshd_accepts", "ms", "the first vsock connection accepted, from before QEMU's exec"},
		{"first_login", "ms", "`true` run over SSH, from before QEMU's exec"},
		{"userspace", "ms", "guest clock: PID 1 started"},
		{"sysinit", "ms", "guest clock: sysinit.target active"},
		{"basic", "ms", "guest clock: basic.target active"},
		{"ssh_active", "ms", "guest clock: ssh.service active"},
		{"forward_active", "ms", "guest clock: the forwarder active"},
		{"logind_active", "ms", "guest clock: systemd-logind active"},
		{"at_read", "ms", "guest clock: when the units were read, right after the first login"},
		{"cpu_total", "ms", "the guest's CPU used to the read, every cgroup"},
		{"cpu_kernel", "ms", "of it, the root cgroup's own: kernel threads and interrupts"},
		{"cpu_pid1", "ms", "of it, init.scope"},
	}
}

const chainScript = `echo "U $(cut -d' ' -f1 /proc/uptime)"
cd /sys/fs/cgroup
find . -name cpu.stat | while read -r f; do echo "C ${f%/cpu.stat} $(awk '$1=="usage_usec"{print $2}' "$f")"; done
echo "C /. $(awk '$1=="usage_usec"{print $2}' cpu.stat)"
systemctl show -p UserspaceTimestampMonotonic | sed 's/^/M /'
systemctl show --all -p Id,InactiveExitTimestampMonotonic,ActiveEnterTimestampMonotonic '*' | sed 's/^/S /'
`

// mutate-lane: TestEveryProbeMeasuresATree
func (chain) Measure(_ context.Context, m *Measurement) error {
	overlay, err := m.Overlay("overlay.qcow2", m.Key.Edits()...)
	if err != nil {
		return err
	}
	cid := NewCID()
	t0 := time.Now()
	qmp := filepath.Join(m.Socks, "m.qmp")
	args := append([]string{"--vsock-cid", fmt.Sprint(cid), "--console", "file:/dev/null", "--append", m.Tree.Cmdline()}, m.qmpToPlug(qmp)...)
	mc, err := m.Launch(io.Discard, m.BootArgs(overlay, args...)...)
	if err != nil {
		return err
	}
	plugged := m.Plug(qmp)
	defer mc.Stop()
	deadline := t0.Add(60 * time.Second)
	var accepts time.Duration
	for time.Now().Before(deadline) {
		if VsockAccepts(cid, 22) {
			accepts = time.Since(t0)
			break
		}
		if err := mc.Exited(); err != nil {
			return err
		}
		time.Sleep(2 * time.Millisecond)
	}
	if accepts == 0 {
		return Miss("sshd never accepted on vsock")
	}
	var login time.Duration
	var why error
	for time.Now().Before(deadline) {
		if _, why = m.Key.Login(cid, xssh.KeyExchangeMLKEM768X25519); why != nil {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		login = time.Since(t0)
		break
	}
	if login == 0 {
		return Miss("sshd accepted and no login came: %v", why)
	}
	_, out, err := m.Key.Run(cid, xssh.KeyExchangeMLKEM768X25519, chainScript)
	if err != nil {
		return Miss("reading the units: %v", err)
	}
	if err := plugged(); err != nil {
		return err
	}
	m.RecordMs("sshd_accepts", accepts)
	m.RecordMs("first_login", login)
	parseChain(m, out)
	return nil
}

func parseChain(m *Measurement, out string) {
	cpu := map[string]float64{}
	var unit string
	var start, active float64
	flush := func() {
		if unit != "" && (start > 0 || active > 0) {
			m.Detail("start\x00"+unit, start)
			m.Detail("active\x00"+unit, active)
		}
		unit, start, active = "", 0, 0
	}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		switch {
		case len(f) == 2 && f[0] == "U":
			if s, err := strconv.ParseFloat(f[1], 64); err == nil {
				m.Record("at_read", s*1000)
			}
		case len(f) == 3 && f[0] == "C":
			if us, err := strconv.ParseFloat(f[2], 64); err == nil {
				cpu[strings.TrimPrefix(f[1], ".")] = us / 1000
			}
		case len(f) == 2 && f[0] == "M":
			if v, ok := strings.CutPrefix(f[1], "UserspaceTimestampMonotonic="); ok {
				us, _ := strconv.ParseFloat(v, 64)
				m.Record("userspace", us/1000)
			}
		case len(f) == 1 && f[0] == "S":
			flush()
		case len(f) == 2 && f[0] == "S":
			k, v, _ := strings.Cut(f[1], "=")
			us, _ := strconv.ParseFloat(v, 64)
			switch k {
			case "Id":
				unit = v
			case "InactiveExitTimestampMonotonic":
				start = us / 1000
			case "ActiveEnterTimestampMonotonic":
				active = us / 1000
			}
		}
	}
	flush()
	for name, ms := range cpu {
		m.Detail("cpu\x00"+name, ms)
	}
	total := cpu["/."]
	var children float64
	for name, ms := range cpu {
		if name != "/." && strings.Count(name, "/") == 1 {
			children += ms
		}
	}
	m.Record("cpu_total", total)
	m.Record("cpu_kernel", total-children)
	m.Record("cpu_pid1", cpu["/init.scope"])
	for metric, u := range map[string]string{"sysinit": "sysinit.target", "basic": "basic.target",
		"ssh_active": "ssh.service", "forward_active": "spin-lab-forward.service", "logind_active": "systemd-logind.service"} {
		if v, ok := m.detail["active\x00"+u]; ok {
			m.Record(metric, v)
		}
	}
}

func (chain) Detail(r *Result) string {
	var b strings.Builder
	for _, t := range r.Trees {
		byKey := map[string][]float64{}
		for _, s := range r.Samples {
			if s.Tree != t.Name {
				continue
			}
			for k, v := range s.Detail {
				byKey[k] = append(byKey[k], v)
			}
		}
		p50 := func(k string) float64 {
			p, _ := Percentile(byKey[k], .5)
			return p
		}
		fmt.Fprintf(&b, "\n  %s: CPU by cgroup, p50 ms (over 0.3)\n", t.Name)
		var cgs []string
		for k := range byKey {
			if name, ok := strings.CutPrefix(k, "cpu\x00"); ok && p50(k) > 0.3 {
				cgs = append(cgs, name)
			}
		}
		slices.SortFunc(cgs, func(a, c string) int { return int(1000 * (p50("cpu\x00"+c) - p50("cpu\x00"+a))) })
		for _, c := range cgs {
			fmt.Fprintf(&b, "    %9.1f  %s\n", p50("cpu\x00"+c), c)
		}
		fmt.Fprintf(&b, "\n  %s: units by active, p50 ms on the guest's clock: start, active\n", t.Name)
		var units []string
		for k := range byKey {
			if name, ok := strings.CutPrefix(k, "active\x00"); ok && p50(k) > 0 && p50(k) < 2000 {
				units = append(units, name)
			}
		}
		slices.SortFunc(units, func(a, c string) int { return int(1000 * (p50("active\x00"+a) - p50("active\x00"+c))) })
		for _, u := range units {
			fmt.Fprintf(&b, "    %8.1f %8.1f  %s\n", p50("start\x00"+u), p50("active\x00"+u), u)
		}
	}
	return b.String()
}
