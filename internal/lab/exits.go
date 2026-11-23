// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spin-stack/machine/internal/qmpdef"
	"github.com/spin-stack/machine/qmp"
)

// exitsProbe counts the VM exits of one cold boot to the first login, by reason, and what the
// host's KVM spent on each kind: the profile that says where nesting's cost goes (nested KVM put
// the kernel at 84.8 ms to PID 1 against 51.8 on metal with the same CPU, labs 45073887732,
// 45075435494). It is a diagnostic and never a goal number: the host's trace runs beside the boot.
//
// Two views of the same exits. QEMU's query-stats is the guest's own vCPUs' counters, as KVM keeps
// them, read the moment the first login returns. The host's tracefs histograms, armed as root
// before QEMU's exec, key kvm_exit by its reason and time each exit to the vCPU's next kvm_entry:
// the time the host's KVM, and QEMU for an exit it hands to userspace, spent before the guest ran
// again. On a nested host that is the L1's handling alone. The two trips through the L0 around it,
// and exits the L0 handles without the L1 (its own EPT faults, its own interrupts), are not in it,
// so a nested exit's time here is a floor on what the guest waited. A HLT's time is the guest's
// idle as well as its wake-up. The MSRs, ports, MMIO addresses and CPUID leaves behind the exits,
// and the exits split between the guest's kernel and the rest (its userspace, the firmware and the
// decompressor, which run at low addresses), are kept beside result.json for a person to read.
//
// The host is the run's alone: every exit the histograms see is this guest's.
type exitsProbe struct{}

const exitsName = "exits"

func (exitsProbe) Name() string { return exitsName }
func (exitsProbe) About() string {
	return "the VM exits of one cold boot to the first login, by reason, from QEMU's query-stats and the host's kvm_exit traced as root: detail kept beside result.json"
}
func (exitsProbe) Options() []Option     { return shape }
func (exitsProbe) Rows(Options) []string { return []string{"boot"} }
func (exitsProbe) Phases() int           { return 1 }

func (exitsProbe) Needs(o Options) []Need {
	return append(append(bootNeeds, Need{"boot", "vsock-cid"}, Need{"boot", "qmp"}, Need{"boot", "append"}), plugNeeds(o)...)
}

func (exitsProbe) Metrics() []Metric {
	return []Metric{
		{"first_login", "ms", "the first login, from the moment before QEMU is exec'd, with the host's trace running"},
		{"exits", "count", "KVM's exits for the guest's vCPUs at the first login (query-stats)"},
		{"io_exits", "count", "of them, port I/O handed to QEMU"},
		{"mmio_exits", "count", "of them, MMIO handed to QEMU"},
		{"halt_exits", "count", "of them, HLT"},
		{"irq_exits", "count", "of them, an external interrupt"},
		{"traced_exits", "count", "the exits the host's trace timed, every reason"},
		{"handled_ms", "ms", "the host's time from kvm_exit to the next kvm_entry, every reason but HLT"},
		{"halted_ms", "ms", "the same for HLT: the guest's idle and its wake-up"},
	}
}

// The host's histograms. Each trigger is removed by its own text, so arming and disarming share
// it. The synthetic event carries an exit's time from kvm_exit to the same thread's next kvm_entry,
// keyed by the vCPU thread, so that several vCPUs do not pair one's exit with another's entry.
const (
	exitsEvent    = "spin_exit u64 lat; unsigned int reason"
	exitsOnExit   = "hist:keys=common_pid:ts0=common_timestamp:r=exit_reason"
	exitsOnEntry  = "hist:keys=common_pid:lat=common_timestamp-$ts0:onmatch(kvm.kvm_exit).spin_exit($lat,$r)"
	exitsByReason = "hist:keys=reason:vals=hitcount,lat:sort=reason"
	exitsByMSR    = "hist:keys=ecx,write:sort=hitcount.descending"
	exitsByPort   = "hist:keys=port,rw:sort=hitcount.descending"
	exitsByMMIO   = "hist:keys=gpa,type:sort=hitcount.descending"
	exitsByLeaf   = "hist:keys=function,index:sort=hitcount.descending"
	// The guest's kernel runs in the upper half of the address space and its userspace in the lower.
	exitsInKernel = "hist:keys=exit_reason:sort=hitcount.descending if guest_rip >= 18446603336221196288"
	exitsInUser   = "hist:keys=exit_reason,isa:sort=hitcount.descending if guest_rip < 18446603336221196288"
)

// exitsTriggers are the files the triggers go in, in the order they are armed.
var exitsTriggers = []struct{ file, trigger string }{
	{"synthetic_events", exitsEvent},
	{"events/kvm/kvm_exit/trigger", exitsOnExit},
	{"events/kvm/kvm_entry/trigger", exitsOnEntry},
	{"events/synthetic/spin_exit/trigger", exitsByReason},
	{"events/kvm/kvm_msr/trigger", exitsByMSR},
	{"events/kvm/kvm_pio/trigger", exitsByPort},
	{"events/kvm/kvm_mmio/trigger", exitsByMMIO},
	{"events/kvm/kvm_cpuid/trigger", exitsByLeaf},
	{"events/kvm/kvm_exit/trigger", exitsInKernel},
	{"events/kvm/kvm_exit/trigger", exitsInUser},
}

// exitsHists are the histograms kept, by the name they are kept under.
var exitsHists = []struct{ name, file string }{
	{"exit reasons (lat: ns from kvm_exit to the next kvm_entry)", "events/synthetic/spin_exit/hist"},
	{"MSRs (ecx, write)", "events/kvm/kvm_msr/hist"},
	{"ports", "events/kvm/kvm_pio/hist"},
	{"MMIO (type 0 read unsatisfied, 1 read, 2 write)", "events/kvm/kvm_mmio/hist"},
	{"CPUID leaves", "events/kvm/kvm_cpuid/hist"},
	{"exits from the guest's kernel, then from below it (userspace, firmware, decompressor)", "events/kvm/kvm_exit/hist"},
}

// exitsScript arms (or, with disarm, removes) the host's triggers, as root. Disarming ignores
// what is not there, so it also clears what a run killed before its own disarm left behind.
func exitsScript(disarm bool) string {
	var b strings.Builder
	b.WriteString("T=/sys/kernel/tracing\nmountpoint -q $T || mount -t tracefs tracefs $T\n")
	if disarm {
		for _, t := range slices.Backward(exitsTriggers) {
			fmt.Fprintf(&b, "echo '!%s' >> $T/%s 2>/dev/null\n", t.trigger, t.file)
		}
		b.WriteString("true\n")
		return b.String()
	}
	b.WriteString("set -e\n")
	for _, t := range exitsTriggers {
		fmt.Fprintf(&b, "echo '%s' >> $T/%s\n", t.trigger, t.file)
	}
	return b.String()
}

// asRootSh runs script with sh as root and returns what it printed.
// mutate-lane: TestEveryProbeMeasuresATree
func asRootSh(script string) (string, error) {
	out, err := exec.Command("sudo", "-n", "sh", "-c", script).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("running as root: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// mutate-lane: TestEveryProbeMeasuresATree
func (exitsProbe) Measure(_ context.Context, m *Measurement) error {
	if m.Keep == "" {
		return fmt.Errorf("the exits probe keeps its histograms, and the experiment names no directory to keep them in")
	}
	overlay, err := m.Overlay("overlay.qcow2", m.Key.Edits()...)
	if err != nil {
		return err
	}
	_, _ = asRootSh(exitsScript(true))
	if _, err := asRootSh(exitsScript(false)); err != nil {
		_, _ = asRootSh(exitsScript(true))
		return fmt.Errorf("arming the host's kvm_exit histograms (tracefs, hist triggers): %w", err)
	}
	defer func() { _, _ = asRootSh(exitsScript(true)) }()

	cid := NewCID()
	sock := filepath.Join(m.Socks, "m.qmp")
	t0 := time.Now()
	mc, err := m.Launch(nil, m.BootArgs(overlay, "--vsock-cid", fmt.Sprint(cid), "--qmp", sock, "--append", m.Tree.Cmdline(m.Key.Init()))...)
	if err != nil {
		return err
	}
	defer mc.Stop()
	plugged := m.Plug(sock)
	login, err := m.Key.LoggedIn(mc, cid, t0)
	if err != nil {
		return err
	}
	if err := plugged(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stats, err := readKVMStats(ctx, sock)
	if err != nil {
		return err
	}
	var hist strings.Builder
	for _, h := range exitsHists {
		out, err := asRootSh("cat /sys/kernel/tracing/" + h.file)
		if err != nil {
			return fmt.Errorf("reading the host's %s histogram: %w", h.name, err)
		}
		fmt.Fprintf(&hist, "== %s\n%s\n", h.name, out)
	}
	reasons := parseExitHist(hist.String())
	if len(reasons) == 0 {
		return Miss("the host's trace timed no exit: kvm_exit and kvm_entry never paired")
	}

	m.RecordMs("first_login", login)
	for _, name := range []string{"exits", "io_exits", "mmio_exits", "halt_exits", "irq_exits"} {
		m.Record(name, float64(stats["vcpu/"+name]))
	}
	var traced, handled, halted float64
	for reason, c := range reasons {
		traced += float64(c.Count)
		if reason == vmxHLT {
			halted += float64(c.Ns) / 1e6
		} else {
			handled += float64(c.Ns) / 1e6
		}
		m.Detail("exit\x00"+vmxReason(reason), float64(c.Count))
		m.Detail("exit_us\x00"+vmxReason(reason), float64(c.Ns)/1e3)
	}
	m.Record("traced_exits", traced)
	m.Record("handled_ms", handled)
	m.Record("halted_ms", halted)
	for name, v := range stats {
		m.Detail("kvm\x00"+name, float64(v))
	}

	host, _ := asRootSh("grep -m1 '^vmx flags' /proc/cpuinfo; cd /sys/module/kvm_intel/parameters 2>/dev/null && grep -H . * 2>/dev/null")
	var kept strings.Builder
	fmt.Fprintf(&kept, "%s, boot %d: first login %v\n\n== the host's KVM\n%s\n== query-stats at the first login, summed over the vCPUs\n",
		m.Tree.Name(), m.Rep+1, login.Round(time.Millisecond), host)
	for _, name := range slices.Sorted(maps.Keys(stats)) {
		fmt.Fprintf(&kept, "%-40s %d\n", name, stats[name])
	}
	fmt.Fprintf(&kept, "\n%s", hist.String())
	name := fmt.Sprintf("%s-%d.exits.txt", m.Tree.Name(), m.Rep+1)
	if err := os.WriteFile(filepath.Join(m.Keep, name), []byte(kept.String()), 0o644); err != nil { //nolint:gosec // for a person to read
		return fmt.Errorf("keeping %s: %w", name, err)
	}
	m.Logf("first login %v: %d exits, %.1f ms handled by the host, %.1f halted; kept as %s",
		login.Round(time.Millisecond), stats["vcpu/exits"], handled, halted, name)
	return nil
}

// exitCost is what one kind of exit cost the boot as the host's KVM saw it.
type exitCost struct{ Count, Ns uint64 }

// parseExitHist reads the synthetic event's histogram: a line per reason, such as
// "{ reason:         30 } hitcount:        960  lat:    12345678". Lines of any other
// histogram are passed over.
func parseExitHist(text string) map[uint32]exitCost {
	out := map[uint32]exitCost{}
	for line := range strings.Lines(text) {
		var r uint32
		var c exitCost
		if n, _ := fmt.Sscanf(line, "{ reason: %d } hitcount: %d lat: %d", &r, &c.Count, &c.Ns); n == 3 {
			out[r] = c
		}
	}
	return out
}

const vmxHLT = 12

// vmxReasons names the VMX basic exit reasons a boot makes (Intel SDM vol. 3, appendix C; the
// kernel's kvm_exit prints the same names). The bench fleet is Intel on both hosts.
var vmxReasons = map[uint32]string{
	0: "EXCEPTION_NMI", 1: "EXTERNAL_INTERRUPT", 7: "INTERRUPT_WINDOW", 8: "NMI_WINDOW", 10: "CPUID",
	vmxHLT: "HLT", 14: "INVLPG", 16: "RDTSC", 18: "VMCALL", 28: "CR_ACCESS", 29: "DR_ACCESS",
	30: "IO_INSTRUCTION", 31: "MSR_READ", 32: "MSR_WRITE", 36: "MWAIT", 40: "PAUSE",
	43: "TPR_BELOW_THRESHOLD", 44: "APIC_ACCESS", 45: "EOI_INDUCED", 46: "GDTR_IDTR", 47: "LDTR_TR",
	48: "EPT_VIOLATION", 49: "EPT_MISCONFIG", 52: "PREEMPTION_TIMER", 54: "WBINVD", 55: "XSETBV",
	56: "APIC_WRITE", 57: "RDRAND", 58: "INVPCID", 61: "RDSEED", 62: "PML_FULL", 63: "XSAVES",
	64: "XRSTORS", 74: "BUS_LOCK", 75: "NOTIFY",
}

func vmxReason(r uint32) string {
	if name, ok := vmxReasons[r&0xffff]; ok {
		return name
	}
	return fmt.Sprintf("reason %d", r)
}

// queryAllStats asks for every statistic KVM keeps, for the vCPUs or the VM. A value is a number
// or, for a histogram, a list.
var queryAllStats = qmpdef.Define[allStatsFilter, []allStatsResult]("query-stats")

type allStatsFilter struct {
	Target string `json:"target"`
}

type allStatsResult struct {
	Provider string `json:"provider"`
	Stats    []struct {
		Name  string          `json:"name"`
		Value json.RawMessage `json:"value"`
	} `json:"stats"`
}

// readKVMStats is every counter KVM keeps for the machine whose monitor is sock, the vCPUs' summed,
// as "vcpu/NAME" and "vm/NAME". Histograms are left out.
// mutate-lane: TestEveryProbeMeasuresATree
func readKVMStats(ctx context.Context, sock string) (map[string]uint64, error) {
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("dialing the monitor at %s: %w", sock, err)
	}
	q, err := qmp.New(ctx, qmp.Unix(c), qmp.Observer{})
	if err != nil {
		return nil, fmt.Errorf("talking to %s: %w", sock, err)
	}
	defer func() { _ = q.Close() }()
	out := map[string]uint64{}
	for _, target := range []string{"vcpu", "vm"} {
		res, err := qmp.Do(ctx, q, queryAllStats, allStatsFilter{Target: target})
		if err != nil {
			return nil, fmt.Errorf("asking QEMU for KVM's %s statistics: %w", target, err)
		}
		sumStats(out, target, res)
	}
	if _, ok := out["vcpu/exits"]; !ok {
		return nil, fmt.Errorf("QEMU reported no vCPU exits: a machine not under KVM, or a KVM without binary statistics")
	}
	return out, nil
}

// sumStats adds KVM's counters in res to out under target's prefix.
func sumStats(out map[string]uint64, target string, res []allStatsResult) {
	for _, r := range res {
		if r.Provider != "kvm" {
			continue
		}
		for _, s := range r.Stats {
			var v uint64
			if json.Unmarshal(s.Value, &v) == nil {
				out[target+"/"+s.Name] += v
			}
		}
	}
}

// Detail is the exits by reason and KVM's counters, p50 by tree, and names the files kept.
func (exitsProbe) Detail(r *Result) string {
	var b strings.Builder
	p50 := func(tree, key string) (float64, bool) {
		var xs []float64
		for _, s := range r.Samples {
			if v, ok := s.Detail[key]; ok && s.Tree == tree {
				xs = append(xs, v)
			}
		}
		return Percentile(xs, .5)
	}
	keys := func(prefix string) []string {
		set := map[string]bool{}
		for _, s := range r.Samples {
			for k := range s.Detail {
				if name, ok := strings.CutPrefix(k, prefix); ok {
					set[name] = true
				}
			}
		}
		return slices.Sorted(maps.Keys(set))
	}
	fmt.Fprintf(&b, "\n  %-22s", "EXIT REASON (p50)")
	for _, t := range r.Trees {
		fmt.Fprintf(&b, " %28s", t.Name+": count, µs, µs each")
	}
	fmt.Fprintln(&b)
	for _, reason := range keys("exit\x00") {
		fmt.Fprintf(&b, "  %-22s", reason)
		for _, t := range r.Trees {
			n, ok := p50(t.Name, "exit\x00"+reason)
			us, _ := p50(t.Name, "exit_us\x00"+reason)
			if !ok {
				fmt.Fprintf(&b, " %28s", "-")
				continue
			}
			fmt.Fprintf(&b, " %9.0f %10.0f %7.2f", n, us, us/n)
		}
		fmt.Fprintln(&b)
	}
	fmt.Fprintf(&b, "\n  %-40s", "KVM COUNTER (p50, non-zero)")
	for _, t := range r.Trees {
		fmt.Fprintf(&b, " %14s", t.Name)
	}
	fmt.Fprintln(&b)
	for _, name := range keys("kvm\x00") {
		line, nonzero := fmt.Sprintf("  %-40s", name), false
		for _, t := range r.Trees {
			v, _ := p50(t.Name, "kvm\x00"+name)
			nonzero = nonzero || v != 0
			line += fmt.Sprintf(" %14.0f", v)
		}
		if nonzero {
			fmt.Fprintln(&b, line)
		}
	}
	b.WriteString("\nhistograms, beside result.json:\n")
	for _, s := range r.Samples {
		if s.Missed == "" {
			fmt.Fprintf(&b, "  %s-%d.exits.txt\n", s.Tree, s.Rep)
		}
	}
	return b.String()
}
