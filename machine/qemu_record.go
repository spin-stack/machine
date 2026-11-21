// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Target is what this machine is, read from QEMU: its version, the formats this package reads,
// the CPU its vCPU has and the machine it was started with (QEMU.machine).
//
// Read once and kept, because none of it changes for the life of the process: a device the
// machine was started with cannot be unplugged from pcie.0, a device's properties are fixed once
// it is realized, a RAM block keeps its maximum length, and a vCPU's features are fixed when it
// is made. One full read is 8 round trips (2026-11-21). Resume does not ask it: the caller hands
// Resume the Target of the machine it resumes into, read once for its host.
func (m *QEMU) Target(ctx context.Context) (Target, error) {
	if m.record != nil {
		return *m.record, nil
	}
	var version struct {
		QEMU struct {
			Major int `json:"major"`
			Minor int `json:"minor"`
			Micro int `json:"micro"`
		} `json:"qemu"`
	}
	if err := m.q.run(ctx, "query-version", nil, &version); err != nil {
		return Target{}, fmt.Errorf("asking the machine's QEMU version: %w", err)
	}
	cpu, err := m.CPU(ctx)
	if err != nil {
		return Target{}, err
	}
	machine, err := m.machine(ctx)
	if err != nil {
		return Target{}, fmt.Errorf("reading the machine QEMU runs: %w", err)
	}
	v := version.QEMU
	t := Target{
		VMM:      fmt.Sprintf("qemu %d.%d.%d", v.Major, v.Minor, v.Micro),
		Formats:  []string{FormatMappedRAM, FormatStream},
		CPU:      cpu,
		Hardware: machine,
	}
	m.record = &t
	return t, nil
}

// machine is the hardware QEMU gives the guest, from four places, each read once:
//
//   - the machine: "accel", "machine type", and "machine <option>" for each option Args puts on
//     -machine (machineOpts, read from the string itself) and for -smp and -m, a struct's fields
//     as "machine smp.maxcpus";
//   - every device the machine was started with (machineDevices): "device 02.0" for one on
//     pcie.0, by its slot, and "device i8042" for one that is not - "device i8257/2" for the second
//     of a type there are more of, in the order its bus has them - each valued its type, and
//     "device 02.0 <property>" for each of its properties;
//   - every RAM block by its maximum length (ramBlocks), which is where a larger BIOS or a larger
//     base RAM is seen: mapped-ram loads both and refuses only a smaller block (ram.c,
//     parse_ramblock);
//   - the at-least facts: a device property in mayHaveMore, as a count.
func (m *QEMU) machine(ctx context.Context) (Hardware, error) {
	var kvm struct {
		Enabled bool `json:"enabled"`
	}
	if err := m.q.run(ctx, "query-kvm", nil, &kvm); err != nil {
		return Hardware{}, fmt.Errorf("asking the machine's accelerator: %w", err)
	}
	accel := "tcg"
	if kvm.Enabled {
		accel = "kvm"
	}
	objs, err := m.qomObjects(ctx, append([]string{"/machine"}, deviceParents...)...)
	if err != nil {
		return Hardware{}, fmt.Errorf("listing the machine's devices: %w", err)
	}
	exact, err := machineFacts(objs[0])
	if err != nil {
		return Hardware{}, err
	}
	exact = append(exact, part{"accel", accel})
	devices := machineDevices(objs[1:])
	paths := make([]string, len(devices))
	for i, d := range devices {
		paths[i] = d.path
	}
	props, err := m.qomObjects(ctx, paths...)
	if err != nil {
		return Hardware{}, fmt.Errorf("reading the machine's devices: %w", err)
	}
	facts, counts, err := deviceFacts(devices, props)
	if err != nil {
		return Hardware{}, err
	}
	var blocks struct {
		Text string `json:"human-readable-text"`
	}
	if err := m.q.run(ctx, "x-query-ramblock", nil, &blocks); err != nil {
		return Hardware{}, fmt.Errorf("listing the machine's RAM blocks: %w", err)
	}
	ram, err := ramBlocks(blocks.Text)
	if err != nil {
		return Hardware{}, err
	}
	return newHardware(slices.Concat(exact, facts, ram), counts)
}

// mayHaveMore are the device properties, by device type, a machine may have more of than the
// checkpoint's: the whole of "the machine resumed into may have more". A larger max_target is a
// controller with targets the guest never scanned and can still be given: a checkpoint resumed
// into one, and the guest used the extra targets (2026-11-21). A larger RAM block, a larger BIOS
// or an extra device is not here and cannot be: QEMU loads each, and the guest is left short or
// wrong.
var mayHaveMore = map[string][]string{
	"virtio-scsi-pci": {"max_target"},
}

// perVM are the device properties, by device type ("*" for all), that are one VM's and not its
// machine's: an identity the stream does not carry and the guest reads again when the transport
// resets (mac, guest-cid, vmgenid's guid, which QEMU makes anew for every VM), a backend on the
// host (netdev, memdev, chardev), a descriptor (vhostfd). A property not listed is compared, so a
// missing entry refuses a resume that would have worked - boot:record's self-resume finds it - and
// never lets through one that would not.
var perVM = map[string][]string{
	"*":               {"id"},
	"virtio-net-pci":  {"mac", "netdev"},
	"vhost-vsock-pci": {"guest-cid", "vhostfd"},
	"virtio-mem-pci":  {"memdev"},
	"isa-serial":      {"chardev"},
	"vmgenid":         {"guid"},
}

// runTime are the device properties, by device type, that QEMU or the guest change while the
// machine runs: state, which travels in the stream, and would make a machine refuse its own
// checkpoint. Measured 2026-11-21 between a machine held and the same machine after a boot, a
// memory plug, a balloon's polling and a vCPU and a disk added: the balloon's statistics, the
// memory plugged and asked for, the power management blocks the guest's firmware programs, and the
// clock.
var runTime = map[string][]string{
	"virtio-balloon-pci": {"guest-stats", "guest-stats-polling-interval"},
	"virtio-mem-pci":     {"size", "requested-size"},
	"ICH9-LPC":           {"pm_io_base", "gpe0_blk"},
	"mc146818rtc":        {"date"},
}

// devicePlaces are the device types that are a disk's place and not the machine's: a host asks
// what it resumes before any VM says how many disks it has, and Resume binds the disks it is
// given.
var devicePlaces = []string{"virtio-blk-pci"}

// listed is whether table names prop for a device of type typ.
func listed(table map[string][]string, typ, prop string) bool {
	return slices.Contains(table["*"], prop) || slices.Contains(table[typ], prop)
}

// deviceParents are where QEMU keeps the devices a machine is started with: the root complex and
// the ISA bus, for what the machine type makes (the host bridge, the LPC and what is behind it),
// and the two containers for what -device made, by id and without one.
var deviceParents = []string{"/machine/q35/pcie.0", "isa.0", "/machine/peripheral", "/machine/peripheral-anon"}

// machineFacts are /machine's: its type, the options Args gives -machine, -smp and -m.
//
// kernel-irqchip is not among them: /machine has no property to read it back by, and a machine
// with the other irqchip saves other sections, which QEMU refuses by itself.
func machineFacts(machine qomObject) ([]part, error) {
	typ, ok := strings.CutSuffix(machine.text("type"), "-machine")
	if !ok {
		return nil, fmt.Errorf("/machine is a %q, not a machine type", machine.text("type"))
	}
	facts := []part{{"machine type", typ}}
	opts := []string{"smp", "memory"}
	_, given, _ := strings.Cut(machineOpts, ",")
	for _, o := range strings.Split(given, ",") {
		if k, _, _ := strings.Cut(o, "="); k != "kernel-irqchip" {
			opts = append(opts, k)
		}
	}
	for _, o := range opts {
		v := machine.value(o)
		if v == nil {
			return nil, fmt.Errorf("/machine has no %s to read back", o)
		}
		facts = flatten(facts, "machine "+o, v)
	}
	return facts, nil
}

// flatten is a value as parts: a struct as one part a field, "name.field", anything else as one.
func flatten(out []part, name string, v json.RawMessage) []part {
	var fields map[string]json.RawMessage
	if bytes.HasPrefix(bytes.TrimSpace(v), []byte("{")) && json.Unmarshal(v, &fields) == nil {
		for k, f := range fields {
			out = flatten(out, name+"."+k, f)
		}
		return out
	}
	return append(out, part{name, qomValue(v)})
}

// device is a device the machine was started with, by its QOM path, and whether it is on the root
// complex.
type device struct {
	path  string
	onPCI bool
}

// machineDevices are the devices under parents, each once, in the order each parent has them:
// a bus's children by their index, which is the order QEMU put them on it, and a container's by
// name. qom-list answers in no order of its own.
func machineDevices(parents []qomObject) []device {
	seen := map[string]bool{}
	var out []device
	for i, p := range parents {
		var kids []qomProperty
		for _, c := range p.Properties {
			if strings.HasPrefix(c.Type, "child<") || strings.HasPrefix(c.Name, "child[") && strings.HasPrefix(c.Type, "link<") {
				kids = append(kids, c)
			}
		}
		slices.SortFunc(kids, func(a, b qomProperty) int { return compareChildren(a.Name, b.Name) })
		for _, c := range kids {
			path := qomValue(c.Value)
			if path == "" || seen[path] {
				continue
			}
			seen[path] = true
			out = append(out, device{path: path, onPCI: i == 0}) // deviceParents[0], the root complex
		}
	}
	return out
}

// compareChildren orders child names "child[2]" before "child[10]": by what is before the
// bracket, then by the number in it.
func compareChildren(a, b string) int {
	ap, an := childIndex(a)
	bp, bn := childIndex(b)
	return cmp.Or(strings.Compare(ap, bp), cmp.Compare(an, bn))
}

func childIndex(name string) (string, int) {
	prefix, rest, ok := strings.Cut(name, "[")
	n, err := strconv.Atoi(strings.TrimSuffix(rest, "]"))
	if !ok || err != nil {
		return name, 0
	}
	return prefix, n
}

// deviceFacts are the devices as parts, objs being each device's properties, and their at-least
// counts. A device is left out when it is hot-plugged - what a checkpoint re-plugs (Saved.Added)
// - or a disk's place; of the rest every property is a part but its children and links, which
// are other objects, and the per-VM and run-time ones.
func deviceFacts(devices []device, objs []qomObject) ([]part, []count, error) {
	type named struct {
		device
		obj  qomObject
		typ  string
		name string
	}
	var kept []named
	types := map[string]int{}
	for i, d := range devices {
		o := objs[i]
		typ := o.text("type")
		if string(o.value("hotplugged")) == "true" || slices.Contains(devicePlaces, typ) {
			continue
		}
		kept = append(kept, named{device: d, obj: o, typ: typ})
		if !d.onPCI {
			types[typ]++
		}
	}
	nth := map[string]int{}
	var exact []part
	var counts []count
	for _, k := range kept {
		switch {
		case k.onPCI:
			addr, err := strconv.Atoi(string(k.obj.value("addr")))
			if err != nil {
				return nil, nil, fmt.Errorf("the device %s on the root complex has no slot: %w", k.path, err)
			}
			k.name = fmt.Sprintf("device %02x.%x", addr>>3, addr&7)
		case types[k.typ] > 1:
			nth[k.typ]++
			k.name = fmt.Sprintf("device %s/%d", k.typ, nth[k.typ])
		default:
			k.name = "device " + k.typ
		}
		exact = append(exact, part{k.name, k.typ})
		for _, p := range k.obj.Properties {
			if p.Value == nil || p.Name == "type" || strings.HasPrefix(p.Type, "child<") || strings.HasPrefix(p.Type, "link<") ||
				listed(perVM, k.typ, p.Name) || listed(runTime, k.typ, p.Name) {
				continue
			}
			name := k.name + " " + p.Name
			if !listed(mayHaveMore, k.typ, p.Name) {
				exact = append(exact, part{name, qomValue(p.Value)})
				continue
			}
			n, err := strconv.ParseInt(string(p.Value), 10, 64)
			if err != nil {
				return nil, nil, fmt.Errorf("%s is %s, not a count: %w", name, p.Value, err)
			}
			counts = append(counts, count{name, n})
		}
	}
	return exact, counts, nil
}

// ramBlocks are the RAM blocks x-query-ramblock lists, each "ram <name>" by its maximum length,
// less the fw_cfg blocks (/rom@etc/acpi/tables, its loader and the RSDP), which QEMU builds and
// resizes on a load. The text is a header and a line a block: its name, page size (a number and a
// unit), offset, used, total, host address, ro or rw; a line of another shape is an error, so a
// QEMU that changes it fails the read rather than leaving a block out.
func ramBlocks(text string) ([]part, error) {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if !strings.HasPrefix(strings.TrimSpace(lines[0]), "Block Name") {
		return nil, fmt.Errorf("x-query-ramblock answered %q, not its table", lines[0])
	}
	var out []part
	for _, l := range lines[1:] {
		f := strings.Fields(l)
		if len(f) != 8 || !strings.HasPrefix(f[5], "0x") {
			return nil, fmt.Errorf("x-query-ramblock's line %q is not a RAM block", l)
		}
		total, err := strconv.ParseUint(f[5][2:], 16, 64)
		if err != nil {
			return nil, fmt.Errorf("x-query-ramblock's line %q: %w", l, err)
		}
		if !strings.HasPrefix(f[0], "/rom@") {
			out = append(out, part{"ram " + f[0], strconv.FormatUint(total, 10)})
		}
	}
	if len(out) == 0 {
		return nil, errors.New("x-query-ramblock lists no RAM block")
	}
	return out, nil
}

// qomObject is an object as qom-list-get reads it: each of its properties, with the value of the
// ones that have one to read.
type qomObject struct {
	Properties []qomProperty `json:"properties"`
}

type qomProperty struct {
	Name  string          `json:"name"`
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value"`
}

// value is the property's value as QEMU wrote it, or nil when it has none.
func (o qomObject) value(name string) json.RawMessage {
	for _, p := range o.Properties {
		if p.Name == name {
			return p.Value
		}
	}
	return nil
}

// text is the property's value as a string.
func (o qomObject) text(name string) string { return qomValue(o.value(name)) }

// qomValue is a value as a record holds it: a string as itself, anything else as its JSON, with
// no space in it - a number as QEMU wrote its digits, not as a float64 that rounds a uint64.
func qomValue(v json.RawMessage) string {
	var s string
	if json.Unmarshal(v, &s) == nil {
		return s
	}
	var b bytes.Buffer
	if json.Compact(&b, v) != nil {
		return string(v)
	}
	return b.String()
}

// qomObjects are the objects at paths, in their order, from one qom-list-get.
func (m *QEMU) qomObjects(ctx context.Context, paths ...string) ([]qomObject, error) {
	var objs []qomObject
	if err := m.q.run(ctx, "qom-list-get", map[string]any{"paths": paths}, &objs); err != nil {
		return nil, err
	}
	if len(objs) != len(paths) {
		return nil, fmt.Errorf("qom-list-get answered %d objects for %d paths", len(objs), len(paths))
	}
	return objs, nil
}
