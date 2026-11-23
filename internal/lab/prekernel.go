// SPDX-License-Identifier: Apache-2.0

package lab

// Experiment #611 only (branch exp/firmware-split-611), never for main: the ledger's span before
// the kernel's clock, split on the workspace's own machine. The ledger boots spin's shape through
// spin-machine, with QEMU traced, and this reads the trace: QEMU's init, each QMP command of the
// held start and of the probe's plug, `cont` to the run state and the vCPU's first entry, qboot's
// fw_cfg steps, and the marker kernel's port writes (kernel/patches/0099 on this branch) through
// the decompressor to start_kernel. The kernel's clock is put on the trace's by kvm-clock (the
// journal's sched offset from the run state's change), and the ledger's own kernel_zero, from its
// markers in the guest's trace, checks it.

import (
	"bufio"
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// prekernelEvents are the QEMU trace events the split reads, beside qmpTraceEvents.
const prekernelEvents = qmpTraceEvents + `fw_cfg_select
fw_cfg_add_file
qemu_anon_ram_alloc
kvm_init_vcpu
kvm_create_vcpu
kvm_set_user_memory
cpu_reset
loader_write_rom
kvm_run_exit
memory_region_ops_write
flatview_new
kvm_ioctl
kvm_vm_ioctl
kvm_vcpu_ioctl
kvm_device_ioctl
kvm_irqchip_commit_routes
virtio_mem_*
`

// prekernelQEMU writes a QEMU for m's tree that traces prekernelEvents into dir/qemu-PID.log, and
// under strace when withStrace, its ioctls with their durations into dir/strace.txt, and returns
// the boot flag that runs it.
func prekernelQEMU(m *Measurement, dir string, withStrace bool) (string, error) {
	events := filepath.Join(dir, "prekernel-events")
	if err := os.WriteFile(events, []byte(prekernelEvents), 0o600); err != nil {
		return "", fmt.Errorf("writing the trace's events: %w", err)
	}
	qemu, err := filepath.EvalSymlinks(filepath.Join(m.Tree.Dir, "bin", "qemu-system-x86_64"))
	if err != nil {
		return "", fmt.Errorf("finding the tree's QEMU: %w", err)
	}
	prefix := ""
	if withStrace {
		if _, err := os.Stat("/usr/bin/strace"); err != nil {
			return "", Refuse("strace=true and this host has no /usr/bin/strace")
		}
		prefix = fmt.Sprintf("/usr/bin/strace -f -qq -ttt -T -e trace=execve,ioctl -o %s/strace.txt ", dir)
	}
	wrapper := filepath.Join(dir, "qemu-prekernel")
	script := fmt.Sprintf("#!/bin/sh\nexec %s%s -msg timestamp=on -D %s/qemu-$$.log -trace events=%s \"$@\"\n", prefix, qemu, dir, events)
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil { // #nosec G306 -- a script this process runs
		return "", fmt.Errorf("writing the traced QEMU: %w", err)
	}
	return "--qemu=" + wrapper, nil
}

// readPrekernelTrace is the largest qemu-*.log in dir: the machine's QEMU, whatever else ran.
func readPrekernelTrace(dir string) (string, error) {
	logs, _ := filepath.Glob(filepath.Join(dir, "qemu-*.log"))
	var best string
	var size int64 = -1
	for _, l := range logs {
		if st, err := os.Stat(l); err == nil && st.Size() > size {
			best, size = l, st.Size()
		}
	}
	if best == "" {
		return "", fmt.Errorf("no QEMU trace in %s", dir)
	}
	b, err := os.ReadFile(best) // #nosec G304 -- the probe's own file
	if err != nil {
		return "", fmt.Errorf("reading QEMU's trace: %w", err)
	}
	return string(b), nil
}

// qev is one line of QEMU's trace, at ms from the probe's t0.
type qev struct {
	at   float64
	kind string
	args string
}

func parseQEMUEvents(trace string, t0 time.Time) []qev {
	var out []qev
	for line := range strings.Lines(trace) {
		stamp, rest, _ := strings.Cut(strings.TrimSpace(line), " ")
		kind, args, _ := strings.Cut(rest, " ")
		at, err := time.Parse("2006-01-02T15:04:05.000000Z", stamp)
		if err != nil {
			continue
		}
		out = append(out, qev{at: ms(at.Sub(t0)), kind: kind, args: args})
	}
	return out
}

// pkMark is a point of the split, in ms from t0.
type pkMark struct {
	Name string  `json:"name"`
	At   float64 `json:"at"`
}

// pkInterval is the stretch from one mark to the next, named for the first, with what QEMU did in it.
type pkInterval struct {
	Name      string  `json:"name"`
	Ms        float64 `json:"ms"`
	Exits     int     `json:"exits"`
	Flatviews int     `json:"flatviews"`
	Ioctls    int     `json:"ioctls"`
}

// pkCommand is one QMP command, in ms from t0.
type pkCommand struct {
	Monitor  string  `json:"monitor"` // start (the held start's) or plug
	Name     string  `json:"name"`
	Arrived  float64 `json:"arrived"`
	Taken    float64 `json:"taken"`
	Answered float64 `json:"answered"`
}

// pkIoctl is one kind of KVM ioctl from QEMU's exec to cont: how many, and the trace's time from
// each to the next event (an upper bound on its own time).
type pkIoctl struct {
	Name  string  `json:"name"`
	N     int     `json:"n"`
	GapMs float64 `json:"gap_ms"`
}

// prekernel is one boot's split.
type prekernel struct {
	Intervals []pkInterval       `json:"intervals"`
	Marks     []pkMark           `json:"marks"`
	Commands  []pkCommand        `json:"commands"`
	Ioctls    []pkIoctl          `json:"ioctls_to_cont"`
	Strace    []pkIoctl          `json:"strace_ioctls_to_cont,omitempty"`
	Values    map[string]float64 `json:"values"`
}

// markerNames are kernel/patches/0099's port 0x80 values, in the order the boot writes them, each
// naming the stretch it starts.
var markerNames = []struct {
	v    uint64
	name string
}{
	{0xd1, "setup code (real mode), main() to protected mode"},
	{0xd2, "the switch to protected mode, to startup_32"},
	{0xd3, "startup_32 and startup_64: page tables, long mode, 5-level check"},
	{0xd4, "the copy of the compressed kernel to the end of the buffer"},
	{0xd5, "after the copy: .bss cleared"},
	{0xd6, "stage 2 IDT and the identity map"},
	{0xd7, "extract_kernel to KASLR: console, RSDP"},
	{0xd8, "choose_random_location (KASLR)"},
	{0xd9, "__decompress (LZ4), with the output's first touch"},
	{0xda, "parse_elf: the segments moved"},
	{0xdb, "handle_relocations"},
	{0xdc, "extract_kernel's return and the jump to the kernel"},
	{0xdd, "the kernel's startup_64 to start_kernel"},
	{0xde, "start_kernel to the kernel's clock (setup_arch to kvm-clock)"},
}

// The short names the table records each stretch under.
var markerMetric = map[uint64]string{
	0xd1: "setup_code", 0xd2: "pm_switch", 0xd3: "startup_32_64", 0xd4: "copy", 0xd5: "bss",
	0xd6: "identity_map", 0xd7: "to_kaslr", 0xd8: "kaslr", 0xd9: "lz4", 0xda: "parse_elf",
	0xdb: "relocations", 0xdc: "jump", 0xdd: "head_64", 0xde: "start_kernel_to_zero",
}

var (
	reOpsWrite = regexp.MustCompile(`addr 0x([0-9a-f]+) value 0x([0-9a-f]+) size \d+ name '([^']*)'`)
	reIoctl    = regexp.MustCompile(`type 0x([0-9a-f]+)`)
	reSel      = regexp.MustCompile(`key 0x([0-9a-f]+) '([^']*)'`)
	reAdd      = regexp.MustCompile(`#\d+: (\S+) \(`)
)

// splitPrekernel reads one boot's QEMU trace into the split. exec is QEMU's exec and offset the
// kernel's sched offset, both in ms; ledgerZero the ledger's kernel_zero, which the trace's is
// checked against.
func splitPrekernel(trace string, t0 time.Time, exec, offset, ledgerZero float64) (prekernel, error) {
	evs := parseQEMUEvents(trace, t0)
	q := parseQMPTrace(trace)
	if q.Running.IsZero() {
		return prekernel{}, fmt.Errorf("QEMU's trace has no run state set to running")
	}
	rel := func(t time.Time) float64 { return ms(t.Sub(t0)) }
	running := rel(q.Running)
	// kvm-clock is zeroed by KVM_SET_CLOCK in the run state's change notifier, right after it.
	for _, e := range parseQEMUEvents(trace, t0) {
		if e.at >= running && e.kind == "kvm_vm_ioctl" && strings.Contains(e.args, "type 0x4030ae7b") {
			running = e.at
			break
		}
	}
	zero := running + offset
	p := prekernel{Values: map[string]float64{}}

	// The monitors: the one that sent cont is the held start's, the other the probe's plug.
	start := ""
	for _, c := range q.Commands {
		if c.Name == "cont" {
			start = c.Monitor
		}
	}
	if start == "" {
		return prekernel{}, fmt.Errorf("QEMU's trace has no cont")
	}
	var cont pkCommand
	firstTaken := -1.0
	for _, c := range q.Commands {
		pc := pkCommand{Monitor: "plug", Name: c.Name, Arrived: rel(c.Arrived), Taken: rel(c.Taken), Answered: rel(c.Answered)}
		if c.Monitor == start {
			pc.Monitor = "start"
		}
		if firstTaken < 0 || pc.Taken < firstTaken {
			firstTaken = pc.Taken
		}
		if pc.Monitor == "start" && pc.Name == "cont" && cont.Name == "" {
			cont = pc
		}
		p.Commands = append(p.Commands, pc)
	}

	// QEMU's init ends at its last event of making the machine before the main loop first takes a
	// command: the reset's ROM copies and CPU resets.
	initEnd := exec
	for _, e := range evs {
		if e.at >= firstTaken {
			break
		}
		switch {
		case strings.HasPrefix(e.kind, "monitor_qmp"), strings.HasPrefix(e.kind, "qmp_enter"), e.kind == "runstate_set":
		default:
			initEnd = e.at
		}
	}
	firstVCPU := -1.0
	for _, e := range evs {
		if e.at >= running && e.kind == "kvm_vcpu_ioctl" && strings.Contains(e.args, "type 0xae80") {
			firstVCPU = e.at
			break
		}
	}

	// The marks, in order: QEMU's, the held start's, qboot's steps by its fw_cfg selects, the marker
	// kernel's port writes, and the kernel's clock.
	marks := []pkMark{{"QEMU's init: exec to its last reset event", exec}, {"init's end to the first QMP command taken", initEnd}}
	var startCmds []pkCommand
	for _, c := range p.Commands {
		if c.Monitor == "start" && c.Taken <= cont.Taken {
			startCmds = append(startCmds, c)
		}
	}
	for i, c := range startCmds {
		if i == 0 && c.Taken > firstTaken {
			marks = append(marks, pkMark{"the plug's commands, before the held start's first", firstTaken})
		}
		if c.Name == "cont" {
			break
		}
		marks = append(marks, pkMark{fmt.Sprintf("held start: %s (to the next command taken)", c.Name), c.Taken})
	}
	marks = append(marks, pkMark{"cont taken to kvm-clock's KVM_SET_CLOCK", cont.Taken}, pkMark{"kvm-clock zeroed to the vCPU's first KVM_RUN", running})
	if firstVCPU >= 0 {
		marks = append(marks, pkMark{"the vCPU's first KVM_RUN to qboot's first exit", firstVCPU})
	}

	files := fwFiles(evs)
	lastStage := ""
	for _, e := range evs {
		if e.at < running || e.at > zero {
			continue
		}
		switch e.kind {
		case "kvm_run_exit":
			if lastStage == "" {
				lastStage = "qboot's first exit"
				marks = append(marks, pkMark{"qboot: first exit to its first fw_cfg step", e.at})
			}
		case "fw_cfg_select":
			if s := fwStage(e.args, files); s != "" && s != lastStage {
				lastStage = s
				marks = append(marks, pkMark{"qboot: " + s, e.at})
			}
		}
	}
	next := 0
	for _, e := range evs {
		if e.at < running || e.at > zero+40 || e.kind != "memory_region_ops_write" {
			continue
		}
		m := reOpsWrite.FindStringSubmatch(e.args)
		if m == nil || m[1] != "80" {
			continue
		}
		v, _ := strconv.ParseUint(m[2], 16, 64)
		// 0xdf is written as the kernel reads kvm-clock for its sched offset: the kernel's clock's
		// zero seen directly, which checks both other placements of it.
		if v == 0xdf {
			if _, ok := p.Values["zero_marker_vs_trace"]; !ok {
				p.Values["zero_marker_vs_trace"] = e.at - zero
				p.Values["zero_marker_vs_ledger"] = e.at - ledgerZero
			}
			continue
		}
		if next == len(markerNames) {
			continue
		}
		for i := next; i < len(markerNames); i++ {
			if markerNames[i].v == v {
				marks = append(marks, pkMark{"kernel: " + markerNames[i].name, e.at})
				next = i + 1
				break
			}
		}
	}
	if next == 0 {
		marks = append(marks, pkMark{"qboot's jump, decompression and early kernel, to the kernel's clock", lastFwSelect(evs, running, zero)})
	}
	marks = append(marks, pkMark{"the kernel's clock", zero})
	slices.SortStableFunc(marks, func(a, b pkMark) int { return cmp.Compare(a.At, b.At) })
	p.Marks = marks

	for i := 0; i+1 < len(marks); i++ {
		a, b := marks[i].At, marks[i+1].At
		iv := pkInterval{Name: marks[i].Name, Ms: b - a}
		for _, e := range evs {
			if e.at < a || e.at >= b {
				continue
			}
			switch e.kind {
			case "kvm_run_exit":
				iv.Exits++
			case "flatview_new":
				iv.Flatviews++
			case "kvm_ioctl", "kvm_vm_ioctl", "kvm_vcpu_ioctl", "kvm_device_ioctl":
				if !strings.Contains(e.args, "type 0xae80") {
					iv.Ioctls++
				}
			}
		}
		p.Intervals = append(p.Intervals, iv)
	}

	// The ioctls from QEMU's exec to cont, by kind.
	byName := map[string]*pkIoctl{}
	for i, e := range evs {
		if e.at > cont.Taken {
			break
		}
		switch e.kind {
		case "kvm_ioctl", "kvm_vm_ioctl", "kvm_vcpu_ioctl", "kvm_device_ioctl":
		default:
			continue
		}
		m := reIoctl.FindStringSubmatch(e.args)
		if m == nil {
			continue
		}
		code, _ := strconv.ParseUint(m[1], 16, 64)
		name := strings.TrimPrefix(e.kind, "kvm_") + " " + kvmIoctlName(code)
		if byName[name] == nil {
			byName[name] = &pkIoctl{Name: name}
		}
		byName[name].N++
		if i+1 < len(evs) {
			byName[name].GapMs += evs[i+1].at - e.at
		}
	}
	for _, v := range byName {
		p.Ioctls = append(p.Ioctls, *v)
	}
	slices.SortFunc(p.Ioctls, func(a, b pkIoctl) int { return cmp.Compare(b.GapMs, a.GapMs) })

	// The values the table records.
	v := p.Values
	v["qemu_init"] = initEnd - exec
	v["to_first_command"] = firstTaken - initEnd
	if len(startCmds) > 0 {
		v["held_qmp"] = cont.Taken - startCmds[0].Taken
		v["held_qmp_cmds"] = float64(len(startCmds))
		var client float64
		for i := 1; i < len(startCmds); i++ {
			client += startCmds[i].Arrived - startCmds[i-1].Answered
		}
		v["held_qmp_client"] = client
		v["start_first_arrived"] = startCmds[0].Arrived - exec
	}
	v["cont_to_running"] = running - cont.Taken
	if firstVCPU >= 0 {
		v["running_to_first_run"] = firstVCPU - running
	}
	v["qemu_start_trace"] = running - exec
	v["guest_to_zero_trace"] = offset
	v["zero_check"] = zero - ledgerZero
	handoff := lastFwSelect(evs, running, zero)
	v["qboot"] = handoff - running
	v["handoff_to_zero"] = zero - handoff
	var plugBefore float64
	for _, c := range p.Commands {
		if c.Monitor == "plug" && c.Taken < cont.Answered {
			plugBefore++
		}
	}
	v["plug_cmds_before_cont"] = plugBefore
	for _, iv := range p.Intervals {
		if iv.Name == "the kernel's clock" {
			continue
		}
		for _, mk := range markerNames {
			if iv.Name == "kernel: "+mk.name {
				v[markerMetric[mk.v]] = iv.Ms
			}
		}
		if iv.Name == "the vCPU's first KVM_RUN to qboot's first exit" || strings.HasPrefix(iv.Name, "qboot") {
			v["exits_qboot"] += float64(iv.Exits)
		}
	}
	var fvInit, fvHeld, fvGuest, ioc float64
	for _, e := range evs {
		switch {
		case e.kind == "flatview_new" && e.at <= initEnd:
			fvInit++
		case e.kind == "flatview_new" && e.at <= running:
			fvHeld++
		case e.kind == "flatview_new" && e.at <= zero:
			fvGuest++
		}
	}
	for _, x := range p.Ioctls {
		ioc += float64(x.N)
		v["ioctl_gap_ms"] += x.GapMs
	}
	v["flatviews_init"], v["flatviews_held"], v["flatviews_guest"], v["ioctls_to_cont"] = fvInit, fvHeld, fvGuest, ioc
	v["acpi_init_build"], v["acpi_select_build"] = acpiBuilds(evs, files, running)
	return p, nil
}

// lastFwSelect is qboot's last fw_cfg select before the kernel's clock: its step before the jump.
func lastFwSelect(evs []qev, running, zero float64) float64 {
	last := running
	for _, e := range evs {
		if e.kind == "fw_cfg_select" && e.at >= running && e.at <= zero {
			last = e.at
		}
	}
	return last
}

// fwFiles are fw_cfg's files by key, from their registrations: a file's key is its place in the
// directory, which QEMU keeps sorted by name.
func fwFiles(evs []qev) map[uint64]string {
	var names []string
	for _, e := range evs {
		if e.kind == "fw_cfg_add_file" {
			if m := reAdd.FindStringSubmatch(e.args); m != nil {
				names = append(names, m[1])
			}
		}
	}
	slices.Sort(names)
	out := map[uint64]string{}
	for i, n := range names {
		out[uint64(0x20+i)] = n
	}
	return out
}

// fwStage names the step of qboot a select starts, as boot/fwstages_test.go's stageOf does.
func fwStage(args string, files map[uint64]string) string {
	m := reSel.FindStringSubmatch(args)
	if m == nil {
		return ""
	}
	key, _ := strconv.ParseUint(m[1], 16, 16)
	name := files[key]
	switch {
	case key == 0x19:
		return "fw_cfg directory"
	case strings.HasPrefix(name, "etc/table-loader"), strings.HasPrefix(name, "etc/acpi"):
		return "ACPI tables"
	case name == "etc/e820":
		return "e820"
	case strings.HasPrefix(name, "etc/smbios"):
		return "SMBIOS"
	case key == 0x14 || key == 0x15:
		return "command line"
	case key == 0x0b || key == 0x0a || key == 0x12:
		return "initrd"
	case key == 0x10 || key == 0x08:
		return "kernel size and entry"
	case key == 0x16 || key == 0x17 || key == 0x18 || key == 0x11:
		return "kernel load"
	}
	return ""
}

// acpiBuilds are the ACPI tables' build in QEMU's init, the interval that ends in the registration
// of etc/acpi/tables, and the longest select callback of the tables' files once the guest runs,
// where QEMU builds them again (acpi_build_update): QEMU traces a select after its callback, so the
// time from the event before it is the callback's.
func acpiBuilds(evs []qev, files map[uint64]string, running float64) (initBuild, selectBuild float64) {
	for i := 1; i < len(evs); i++ {
		e := evs[i]
		switch {
		case e.kind == "fw_cfg_add_file" && strings.Contains(e.args, "etc/acpi/tables") && initBuild == 0:
			initBuild = e.at - evs[i-1].at
		case e.kind == "fw_cfg_select" && e.at >= running:
			m := reSel.FindStringSubmatch(e.args)
			if m == nil {
				continue
			}
			key, _ := strconv.ParseUint(m[1], 16, 16)
			if n := files[key]; strings.HasPrefix(n, "etc/acpi") || strings.HasPrefix(n, "etc/table-loader") {
				selectBuild = max(selectBuild, e.at-evs[i-1].at)
			}
		}
	}
	return initBuild, selectBuild
}

// kvmIoctlName is a KVM ioctl's name, from its number (KVMIO, 0xae) and direction.
func kvmIoctlName(code uint64) string {
	nr, typ, dir := code&0xff, (code>>8)&0xff, code>>30
	if typ != 0xae {
		return fmt.Sprintf("0x%x", code)
	}
	names := map[uint64]string{
		0x00: "GET_API_VERSION", 0x01: "CREATE_VM", 0x02: "GET_MSR_INDEX_LIST", 0x03: "CHECK_EXTENSION",
		0x04: "GET_VCPU_MMAP_SIZE", 0x05: "GET_SUPPORTED_CPUID", 0x09: "GET_EMULATED_CPUID",
		0x0a: "GET_MSR_FEATURE_INDEX_LIST", 0x41: "CREATE_VCPU", 0x46: "SET_USER_MEMORY_REGION",
		0x47: "SET_TSS_ADDR", 0x48: "SET_IDENTITY_MAP_ADDR", 0x49: "SET_USER_MEMORY_REGION2",
		0x60: "CREATE_IRQCHIP", 0x61: "IRQ_LINE", 0x62: "GET_IRQCHIP", 0x63: "SET_IRQCHIP", 0x64: "CREATE_PIT",
		0x67: "IRQ_LINE_STATUS", 0x6a: "SET_GSI_ROUTING", 0x76: "IRQFD", 0x77: "CREATE_PIT2",
		0x79: "IOEVENTFD", 0x7b: "SET_CLOCK", 0x7c: "GET_CLOCK", 0x80: "RUN", 0x81: "GET_REGS",
		0x82: "SET_REGS", 0x83: "GET_SREGS", 0x84: "SET_SREGS", 0x88: "GET_MSRS", 0x89: "SET_MSRS",
		0x8d: "SET_FPU", 0x8e: "GET_LAPIC", 0x8f: "SET_LAPIC", 0x90: "SET_CPUID2", 0x98: "GET_MP_STATE",
		0x99: "SET_MP_STATE", 0x9c: "X86_SETUP_MCE", 0x9d: "X86_GET_MCE_CAP_SUPPORTED",
		0x9f: "GET_VCPU_EVENTS", 0xa0: "SET_VCPU_EVENTS", 0xa1: "GET_DEBUGREGS", 0xa4: "GET_XSAVE",
		0xa6: "GET_XCRS", 0xa7: "SET_XCRS", 0xab: "GET_ONE_REG", 0xac: "SET_ONE_REG", 0xad: "KVMCLOCK_CTRL",
		0xcf: "GET_XSAVE2", 0xe0: "CREATE_DEVICE", 0xe1: "SET_DEVICE_ATTR", 0xe2: "GET_DEVICE_ATTR",
		0xe3: "HAS_DEVICE_ATTR",
	}
	switch {
	case nr == 0xa2 && dir == 0:
		return "SET_TSC_KHZ"
	case nr == 0xa2:
		return "SET_DEBUGREGS"
	case nr == 0xa3 && dir == 0:
		return "GET_TSC_KHZ"
	case nr == 0xa3:
		return "ENABLE_CAP"
	case nr == 0xa5 && code>>16&0x3fff == 0x20:
		return "SIGNAL_MSI"
	case nr == 0xa5:
		return "SET_XSAVE"
	}
	if n, ok := names[nr]; ok {
		return n
	}
	return fmt.Sprintf("nr 0x%x", nr)
}

// straceIoctls are the ioctls strace saw from QEMU's exec to cont (both on the wall clock, ms from
// t0), by request, with the time each took (strace -T).
func straceIoctls(path string, t0 time.Time, until float64) ([]pkIoctl, float64, error) {
	f, err := os.Open(path) // #nosec G304 -- the probe's own file
	if err != nil {
		return nil, 0, fmt.Errorf("reading strace's log: %w", err)
	}
	defer func() { _ = f.Close() }()
	t0s := float64(t0.UnixNano()) / 1e9
	pending := map[string]string{} // a pid's unfinished ioctl request
	byName := map[string]*pkIoctl{}
	exec := -1.0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		fields := strings.SplitN(sc.Text(), " ", 3)
		if len(fields) < 3 {
			continue
		}
		pid, rest := fields[0], fields[2]
		ts, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			continue
		}
		at := (ts - t0s) * 1000
		if strings.HasPrefix(rest, "execve(") && strings.Contains(rest, "qemu-system") && exec < 0 {
			exec = at
			continue
		}
		if exec < 0 || at > until {
			continue
		}
		var req string
		switch {
		case strings.HasPrefix(rest, "ioctl("):
			parts := strings.SplitN(rest, ", ", 3)
			if len(parts) < 2 {
				continue
			}
			req = strings.TrimSuffix(parts[1], ")")
			if strings.Contains(rest, "<unfinished ...>") {
				pending[pid] = req
				continue
			}
		case strings.HasPrefix(rest, "<... ioctl resumed>"):
			req = pending[pid]
			delete(pending, pid)
		default:
			continue
		}
		if i := strings.IndexAny(req, " ,"); i > 0 {
			req = req[:i]
		}
		if req == "KVM_RUN" {
			continue
		}
		var took float64
		if i := strings.LastIndex(rest, "<"); i >= 0 {
			took, _ = strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(rest[i+1:]), ">"), 64)
		}
		if byName[req] == nil {
			byName[req] = &pkIoctl{Name: req}
		}
		byName[req].N++
		byName[req].GapMs += took * 1000
	}
	if err := sc.Err(); err != nil {
		return nil, 0, fmt.Errorf("reading strace's log: %w", err)
	}
	var out []pkIoctl
	for _, v := range byName {
		out = append(out, *v)
	}
	slices.SortFunc(out, func(a, b pkIoctl) int { return cmp.Compare(b.GapMs, a.GapMs) })
	return out, exec, nil
}

// text is the split as a person reads it.
func (p prekernel) text(title string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: QEMU's exec to the kernel's clock, from QEMU's trace (ms)\n\n", title)
	fmt.Fprintf(&b, "%9s %9s %6s %6s %6s  %s\n", "AT", "MS", "EXITS", "FVIEW", "IOCTL", "STRETCH")
	for i, iv := range p.Intervals {
		fmt.Fprintf(&b, "%9.2f %9.2f %6d %6d %6d  %s\n", p.Marks[i].At, iv.Ms, iv.Exits, iv.Flatviews, iv.Ioctls, iv.Name)
	}
	fmt.Fprintf(&b, "%9.2f %9s %6s %6s %6s  the kernel's clock\n\n", p.Marks[len(p.Marks)-1].At, "", "", "", "")
	fmt.Fprintf(&b, "QMP commands (ms from t0)\n%-6s %-28s %9s %9s %9s %8s\n", "MON", "COMMAND", "ARRIVED", "TAKEN", "ANSWERED", "TOOK")
	for _, c := range p.Commands {
		fmt.Fprintf(&b, "%-6s %-28s %9.2f %9.2f %9.2f %8.2f\n", c.Monitor, c.Name, c.Arrived, c.Taken, c.Answered, c.Answered-c.Taken)
	}
	fmt.Fprintf(&b, "\nKVM ioctls from QEMU's exec to cont, by the trace (GAP: to the next traced event, an upper bound)\n")
	for _, x := range p.Ioctls {
		fmt.Fprintf(&b, "%6d %9.3f  %s\n", x.N, x.GapMs, x.Name)
	}
	if len(p.Strace) > 0 {
		fmt.Fprintf(&b, "\nKVM ioctls from QEMU's exec to cont, by strace -T (under ptrace)\n")
		for _, x := range p.Strace {
			fmt.Fprintf(&b, "%6d %9.3f  %s\n", x.N, x.GapMs, x.Name)
		}
	}
	keys := slices.Sorted(func(yield func(string) bool) {
		for k := range p.Values {
			if !yield(k) {
				return
			}
		}
	})
	fmt.Fprintf(&b, "\nvalues\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "%-24s %9.3f\n", k, p.Values[k])
	}
	return b.String()
}

// prekernelMetrics are the values the table records from the split.
var prekernelMetrics = []Metric{
	{"qemu_init", "ms", "QEMU's exec to its last reset event before the main loop takes a command"},
	{"to_first_command", "ms", "init's last event to the first QMP command taken"},
	{"held_qmp", "ms", "the held start's first command taken to cont taken"},
	{"held_qmp_cmds", "count", "the held start's commands through cont"},
	{"held_qmp_client", "ms", "of held_qmp, QEMU waiting for spin-machine's next command"},
	{"start_first_arrived", "ms", "the held start's first command's arrival, from QEMU's exec"},
	{"plug_cmds_before_cont", "count", "the plug's commands taken before cont was answered"},
	{"cont_to_running", "ms", "cont taken to kvm-clock zeroed (KVM_SET_CLOCK at the run state change)"},
	{"running_to_first_run", "ms", "kvm-clock zeroed to the vCPU's first KVM_RUN"},
	{"qemu_start_trace", "ms", "QEMU's exec to running, by the trace"},
	{"guest_to_zero_trace", "ms", "running to the kernel's clock: the sched offset"},
	{"zero_check", "ms", "the trace's kernel clock less the ledger's kernel_zero"},
	{"qboot", "ms", "running to qboot's last fw_cfg select"},
	{"handoff_to_zero", "ms", "qboot's last select to the kernel's clock"},
	{"exits_qboot", "count", "exits to QEMU from running to qboot's last select"},
	{"setup_code", "ms", "marker kernel: setup main() to protected mode"},
	{"pm_switch", "ms", "marker kernel: protected mode to startup_32"},
	{"startup_32_64", "ms", "marker kernel: startup_32 to the copy"},
	{"copy", "ms", "marker kernel: the compressed kernel's copy"},
	{"bss", "ms", "marker kernel: .bss cleared"},
	{"identity_map", "ms", "marker kernel: stage 2 IDT and identity map"},
	{"to_kaslr", "ms", "marker kernel: extract_kernel to KASLR"},
	{"kaslr", "ms", "marker kernel: choose_random_location"},
	{"lz4", "ms", "marker kernel: __decompress"},
	{"parse_elf", "ms", "marker kernel: parse_elf"},
	{"relocations", "ms", "marker kernel: handle_relocations"},
	{"jump", "ms", "marker kernel: extract_kernel's return to the kernel's startup_64"},
	{"head_64", "ms", "marker kernel: startup_64 to start_kernel"},
	{"start_kernel_to_zero", "ms", "marker kernel: start_kernel to the kernel's clock"},
	{"flatviews_init", "count", "flat views made in QEMU's init"},
	{"flatviews_held", "count", "flat views made from init's end to running"},
	{"flatviews_guest", "count", "flat views made from running to the kernel's clock"},
	{"ioctls_to_cont", "count", "KVM ioctls from QEMU's exec to cont"},
	{"ioctl_gap_ms", "ms", "their trace gaps summed, an upper bound"},
	{"acpi_init_build", "ms", "the trace interval ending in etc/acpi/tables' registration, QEMU's first ACPI build"},
	{"acpi_select_build", "ms", "the longest ACPI file select callback once the guest runs: the rebuild"},
	{"zero_marker_vs_trace", "ms", "marker kernel: the 0xdf write at the sched offset's read less KVM_SET_CLOCK plus the offset"},
	{"zero_marker_vs_ledger", "ms", "marker kernel: the 0xdf write less the ledger's kernel_zero"},
	{"mono_lag", "ms", "PID 1's exec on sched_clock (dmesg) less on CLOCK_MONOTONIC (the ledger's trace)"},
	{"strace_ioctls", "count", "under strace: KVM ioctls from QEMU's exec to cont"},
	{"strace_ioctl_ms", "ms", "under strace: their time, strace -T"},
}

// printkRunInit is dmesg's "Run ... as init process" on sched_clock, in ms.
func printkRunInit(dmesg string) (float64, bool) {
	for line := range strings.Lines(dmesg) {
		if !strings.Contains(line, "as init process") {
			continue
		}
		open, close := strings.Index(line, "["), strings.Index(line, "]")
		if open < 0 || close < open {
			return 0, false
		}
		sec, err := strconv.ParseFloat(strings.TrimSpace(line[open+1:close]), 64)
		return sec * 1000, err == nil
	}
	return 0, false
}
