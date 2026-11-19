// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"slices"
	"strings"
)

// QEMU is a machine running under QEMU, reached through one of its monitors.
type QEMU struct {
	q *qmpClient
}

var _ VM = (*QEMU)(nil)

// Attach takes over a QMP monitor of a machine started from a Spec's Args, and returns the
// machine. The connection is a Unix socket because disks and states are handed to QEMU as
// descriptors over it.
//
// It takes no Spec: what it needs - which places for disks the machine has and what each is,
// whether it is held for a boot or for a checkpoint, its memory and vCPU slots - it asks QEMU,
// which cannot disagree with the command line it was started from the way a second copy of the
// Spec could.
func Attach(conn *net.UnixConn) (*QEMU, error) {
	q, err := newQMP(conn)
	if err != nil {
		return nil, err
	}
	return &QEMU{q: q}, nil
}

// Close lets the monitor go. The machine runs on: a monitor's client leaving is nothing to QEMU.
func (m *QEMU) Close() error { return m.q.c.Close() }

// Quit ends QEMU. Its reply may never arrive, because QEMU can close the monitor before sending
// it, so a connection that ends instead is the quit having worked.
func (m *QEMU) Quit(ctx context.Context) error {
	err := m.q.run(ctx, "quit", nil, nil)
	if err != nil && !errors.Is(err, net.ErrClosed) && !strings.Contains(err.Error(), "EOF") {
		return fmt.Errorf("quitting: %w", err)
	}
	return nil
}

// status is QEMU's run state: prelaunch for a machine held for a boot (-S), inmigrate for one
// held for a checkpoint (-incoming defer).
func (m *QEMU) status(ctx context.Context) (string, error) {
	var st struct {
		Status string `json:"status"`
	}
	if err := m.q.run(ctx, "query-status", nil, &st); err != nil {
		return "", err
	}
	return st.Status, nil
}

// Boot binds the disks to their places and lets the vCPUs go.
func (m *QEMU) Boot(ctx context.Context, disks ...Disk) error {
	st, err := m.status(ctx)
	if err != nil {
		return fmt.Errorf("booting: %w", err)
	}
	if st != "prelaunch" {
		// inmigrate takes a cont and stays inmigrate: QEMU only notes that the machine is to
		// run once a migration has arrived. A machine for a boot is started without
		// Spec.Restore.
		return fmt.Errorf("booting a machine that is %s: only one started held for a boot boots", st)
	}
	if err := m.bindAll(ctx, disks, nil); err != nil {
		return fmt.Errorf("booting: %w", err)
	}
	if err := m.q.run(ctx, "cont", nil, nil); err != nil {
		return fmt.Errorf("booting: %w", err)
	}
	return nil
}

// Continue runs a machine a checkpoint left stopped.
func (m *QEMU) Continue(ctx context.Context) error {
	if err := m.q.run(ctx, "cont", nil, nil); err != nil {
		return fmt.Errorf("continuing the machine: %w", err)
	}
	return nil
}

// slot is one place for a disk the machine was started with (Spec.Disks).
type slot struct {
	index    int
	serial   string
	readonly bool
}

// slots are the machine's places for disks, read from QEMU: each is a device over a node named
// slot<i> (appendDisks).
func (m *QEMU) slots(ctx context.Context) ([]slot, error) {
	var blocks []struct {
		Qdev     string `json:"qdev"`
		Inserted *struct {
			NodeName string `json:"node-name"`
			RO       bool   `json:"ro"`
		} `json:"inserted"`
	}
	if err := m.q.run(ctx, "query-block", nil, &blocks); err != nil {
		return nil, fmt.Errorf("listing the machine's disks: %w", err)
	}
	var out []slot
	for _, b := range blocks {
		if b.Inserted == nil {
			continue
		}
		var i int
		if _, err := fmt.Sscanf(b.Inserted.NodeName, slotNodePrefix+"%d", &i); err != nil {
			continue
		}
		var serial string
		if err := m.q.run(ctx, "qom-get", map[string]any{"path": peripheral + slotDevice(i), "property": "serial"}, &serial); err != nil {
			return nil, fmt.Errorf("reading the serial of disk place %d: %w", i, err)
		}
		out = append(out, slot{index: i, serial: serial, readonly: b.Inserted.RO})
	}
	return out, nil
}

const peripheral = "/machine/peripheral/"

// hotDisk is a disk attached while the machine ran, at its target on the hotplug controller.
type hotDisk struct {
	Serial string `json:"serial"`
	Target int    `json:"target"`
}

// bindAll binds each disk to the place with its serial, and attaches at its target any disk
// that has no place but was attached while the machine it is resumed from ran.
func (m *QEMU) bindAll(ctx context.Context, disks []Disk, hot []hotDisk) error {
	slots, err := m.slots(ctx)
	if err != nil {
		return err
	}
	for _, d := range disks {
		if err := d.validate(); err != nil {
			return fmt.Errorf("disk %q: %w", d.Serial, err)
		}
		if i := slices.IndexFunc(slots, func(s slot) bool { return s.serial == d.Serial }); i >= 0 {
			if err := m.bind(ctx, slots[i], d); err != nil {
				return err
			}
			continue
		}
		if i := slices.IndexFunc(hot, func(h hotDisk) bool { return h.Serial == d.Serial }); i >= 0 {
			if err := m.attachAt(ctx, hot[i].Target, d); err != nil {
				return err
			}
			continue
		}
		return fmt.Errorf("disk %q: the machine has no place for a disk of that serial", d.Serial)
	}
	return nil
}

// bind puts disk d under the place s. The place is a raw node over an empty placeholder, the
// device's drive since exec; blockdev-reopen moves the raw node onto the disk's top image, and
// the placeholder goes. It is the seam because a realized device's drive cannot change and a raw
// node's file can, and raw passes every request through (measured on QEMU 11.1.3 and 11.2.0-rc0:
// the guest sees the image's size and writes land in it).
func (m *QEMU) bind(ctx context.Context, s slot, d Disk) error {
	if d.Readonly != s.readonly {
		// The device took its permissions when it was realized: QEMU refuses a writable
		// device a read-only node ("cannot support read-write users"), and a read-only place
		// given a writable disk would hand the guest a disk it cannot write.
		return fmt.Errorf("disk %q: read-only %t, and its place is read-only %t", d.Serial, d.Readonly, s.readonly)
	}
	top, err := m.addImages(ctx, d)
	if err != nil {
		return err
	}
	place := placeNode(s.index, top, s.readonly)
	if err := m.q.run(ctx, "blockdev-reopen", map[string]any{"options": []placeNodeOptions{place}}, nil); err != nil {
		return fmt.Errorf("disk %q: putting it in its place: %w", d.Serial, err)
	}
	if err := m.q.run(ctx, "blockdev-del", map[string]any{"node-name": emptyNode(s.index)}, nil); err != nil {
		return fmt.Errorf("disk %q: deleting its place's placeholder: %w", d.Serial, err)
	}
	return nil
}

// serialPattern is what a serial may be: a virtio-blk serial is 20 bytes, and the serial names
// the disk's nodes, which QEMU allows letters, digits, '-' and '_' in.
var serialPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,20}$`)

func (d Disk) validate() error {
	switch {
	case !serialPattern.MatchString(d.Serial):
		return fmt.Errorf("serial %q is not 1-20 of A-Z, a-z, 0-9, '-' and '_'", d.Serial)
	case len(d.Images) == 0:
		return errors.New("no image")
	case d.DirectOverBacking && len(d.Images) < 2:
		// QEMU refuses backing options on an image with no backing ("Could not open backing
		// file"), and there is nothing to keep cached.
		return errors.New("direct over backing, and no image under the top")
	}
	for j, img := range d.Images {
		switch {
		case img.File == nil:
			return fmt.Errorf("image %d has no file", j)
		case !slices.Contains(diskFormats, img.Format):
			return fmt.Errorf("image %d: format %q is not one of %v", j, img.Format, diskFormats)
		case img.Format == "raw" && j != len(d.Images)-1:
			return fmt.Errorf("image %d is raw, which has no backing, and images follow it", j)
		}
	}
	return nil
}

// diskNode names the format node of image j of the disk with this serial, and its file node is
// diskNode+"-f": named from the serial, so that a detach in another process finds them.
func diskNode(serial string, j int) string { return fmt.Sprintf("d-%s-%d", serial, j) }

// TopNode is the node of the disk's top image, for whatever seals it under a new overlay through
// a monitor of its own (blockdev-snapshot): an external contract, with spin's storage.
//
// It is not the node the device reads. A disk bound to a place is under that place's raw node,
// slot<i>, which stays: QEMU 11.2 has no command that replaces a realized device's node
// (blockdev-replace is not in it), and raw passes every request through. blockdev-snapshot of
// this node puts the overlay in its place under every parent, the raw node included.
func TopNode(serial string) string { return diskNode(serial, 0) }

// addImages adds a node for each of the disk's images, the base first, each over its own
// descriptor set, and returns the top's name. Every option is the one a disk on the command line
// had:
//
// aio=io_uring, and QEMU is built with it for this reason: the default, aio=threads, hands every
// request to a worker pool and pays a context switch each way, where io_uring submits and
// completes in batches through one ring shared with the kernel.
//
// discard=unmap on both nodes: a guest's discard arrives at the format node, and QEMU drops one
// on a node opened without unmap and tells the guest it worked - so fstrim freed nothing.
//
// No backing read from a header: each image's backing is the next one handed over, and the last
// has none (JSON null; a raw image has no backing option at all).
//
// drop-cache off, on every layer. QEMU's default is on, and what it does is not caching: when a
// machine is activated after an incoming migration, raw_co_invalidate_cache flushes the node and
// calls posix_fadvise(POSIX_FADV_DONTNEED) on it, for a live migration onto a second host over
// shared storage. Neither half is true here: a layer below the top is sealed, and is one file
// every VM on the host reads, so dropping it is paid by all of them. Traced with `strace -y`, a
// restore issued the call against the base image itself; it cost 4.8 ms against a warm cache and
// has been measured at 251 ms against a colder one, and with it off cont went from 8.9 to 2.8 ms.
func (m *QEMU) addImages(ctx context.Context, d Disk) (string, error) {
	for j := len(d.Images) - 1; j >= 0; j-- {
		img := d.Images[j]
		node := diskNode(d.Serial, j)
		readOnly := d.Readonly || j > 0
		// The set's opaque is the image's path, and a writable top's set holds a read-only
		// copy of it too. spin's storage relies on both (spin-stack/spin-machine#297): it finds
		// which machine has an image open by query-fdsets' opaques, and after sealing a layer it
		// drops the writable copy, leaving QEMU the read-only one to reopen the layer with -
		// QEMU takes from a set only a descriptor of the access mode it asks for.
		opaque := img.File.Name()
		set, err := m.q.addFD(ctx, img.File, opaque)
		if err != nil {
			return "", fmt.Errorf("disk %q: %w", d.Serial, err)
		}
		if !readOnly {
			if err := m.addReadOnlyCopy(ctx, set, img.File, opaque); err != nil {
				return "", fmt.Errorf("disk %q: %w", d.Serial, err)
			}
		}
		file := fileNode{
			Driver: "file", NodeName: node + "-f",
			Filename: fmt.Sprintf("/dev/fdset/%d", set),
			AIO:      "io_uring", Discard: "unmap", ReadOnly: readOnly,
		}
		if j == 0 && d.Locking {
			file.Locking = "on"
		}
		if d.DirectOverBacking {
			file.Cache = &nodeCache{Direct: j == 0}
		}
		format := formatNode{Driver: img.Format, NodeName: node, File: node + "-f", ReadOnly: readOnly, Discard: "unmap"}
		switch {
		case j < len(d.Images)-1:
			format.Backing = diskNode(d.Serial, j+1)
		case img.Format != "raw":
			format.Backing = json.RawMessage("null")
		}
		for _, n := range []any{file, format} {
			if err := m.q.run(ctx, "blockdev-add", n, nil); err != nil {
				return "", fmt.Errorf("disk %q: adding image %d: %w", d.Serial, j, err)
			}
		}
	}
	return diskNode(d.Serial, 0), nil
}

// addReadOnlyCopy reopens f read-only and adds it to set.
func (m *QEMU) addReadOnlyCopy(ctx context.Context, set int, f *os.File, opaque string) error {
	ro, err := os.Open(fmt.Sprintf("/proc/self/fd/%d", f.Fd()))
	if err != nil {
		return fmt.Errorf("reopening %s read-only: %w", opaque, err)
	}
	defer func() { _ = ro.Close() }() // QEMU holds its own copy
	return m.q.addFDTo(ctx, set, ro, opaque)
}

// AttachDisk gives the running machine a disk on its hotplug controller, at the first target
// free.
func (m *QEMU) AttachDisk(ctx context.Context, d Disk) error {
	if err := d.validate(); err != nil {
		return fmt.Errorf("disk %q: %w", d.Serial, err)
	}
	hot, err := m.hotDisks(ctx)
	if err != nil {
		return err
	}
	t := 0
	for slices.ContainsFunc(hot, func(h hotDisk) bool { return h.Target == t }) {
		t++
	}
	return m.attachAt(ctx, t, d)
}

func (m *QEMU) attachAt(ctx context.Context, target int, d Disk) error {
	if target < 0 || target >= MaxHotplugDisks {
		return fmt.Errorf("disk %q: target %d, and the controller addresses 0 to %d", d.Serial, target, MaxHotplugDisks-1)
	}
	top, err := m.addImages(ctx, d)
	if err != nil {
		return err
	}
	if err := m.q.run(ctx, "device_add", hotplugDisk(target, top, d.Serial), nil); err != nil {
		return fmt.Errorf("disk %q: attaching it at target %d: %w", d.Serial, target, err)
	}
	return nil
}

// hotDisks are the disks attached while the machine ran: the devices named hd<target>.
func (m *QEMU) hotDisks(ctx context.Context) ([]hotDisk, error) {
	var children []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if err := m.q.run(ctx, "qom-list", map[string]any{"path": peripheral}, &children); err != nil {
		return nil, fmt.Errorf("listing the machine's devices: %w", err)
	}
	var out []hotDisk
	for _, c := range children {
		var t int
		if c.Type != "child<scsi-hd>" {
			continue
		}
		if _, err := fmt.Sscanf(c.Name, "hd%d", &t); err != nil {
			continue
		}
		var serial string
		if err := m.q.run(ctx, "qom-get", map[string]any{"path": peripheral + c.Name, "property": "serial"}, &serial); err != nil {
			return nil, fmt.Errorf("reading the serial of %s: %w", c.Name, err)
		}
		out = append(out, hotDisk{Serial: serial, Target: t})
	}
	return out, nil
}

// DetachDisk takes away a disk attached while the machine ran, then its nodes and descriptors.
func (m *QEMU) DetachDisk(ctx context.Context, serial string) error {
	hot, err := m.hotDisks(ctx)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(hot, func(h hotDisk) bool { return h.Serial == serial })
	if i < 0 {
		return fmt.Errorf("no disk %q was attached to this machine while it ran", serial)
	}
	id := hotDiskID(hot[i].Target)
	if err := m.q.run(ctx, "device_del", map[string]any{"id": id}, nil); err != nil {
		return fmt.Errorf("detaching disk %q: %w", serial, err)
	}
	if err := m.q.waitDeleted(ctx, id); err != nil {
		return fmt.Errorf("detaching disk %q: %w", serial, err)
	}
	var nodes []struct {
		NodeName string `json:"node-name"`
	}
	if err := m.q.run(ctx, "query-named-block-nodes", map[string]any{"flat": true}, &nodes); err != nil {
		return fmt.Errorf("listing disk %q's nodes: %w", serial, err)
	}
	// The top first: a node cannot go while another is backed by it.
	for j := 0; slices.ContainsFunc(nodes, func(n struct {
		NodeName string `json:"node-name"`
	}) bool {
		return n.NodeName == diskNode(serial, j)
	}); j++ {
		for _, n := range []string{diskNode(serial, j), diskNode(serial, j) + "-f"} {
			if err := m.q.run(ctx, "blockdev-del", map[string]any{"node-name": n}, nil); err != nil {
				return fmt.Errorf("deleting disk %q's node %s: %w", serial, n, err)
			}
		}
	}
	return m.removeFDSets(ctx, "d-"+serial+"-")
}

// removeFDSets lets go of the descriptor sets whose descriptors were handed over for nodes named
// with this prefix: QEMU keeps a set until it is removed, and frees it once nothing has it open.
func (m *QEMU) removeFDSets(ctx context.Context, prefix string) error {
	var sets []struct {
		ID  int `json:"fdset-id"`
		FDs []struct {
			Opaque string `json:"opaque"`
		} `json:"fds"`
	}
	if err := m.q.run(ctx, "query-fdsets", nil, &sets); err != nil {
		return fmt.Errorf("listing the descriptors handed to QEMU: %w", err)
	}
	for _, s := range sets {
		if len(s.FDs) == 0 || !strings.HasPrefix(s.FDs[0].Opaque, prefix) {
			continue
		}
		if err := m.q.run(ctx, "remove-fd", map[string]any{"fdset-id": s.ID}, nil); err != nil {
			return fmt.Errorf("letting go of descriptor set %d: %w", s.ID, err)
		}
	}
	return nil
}

// cpuSlot is one place for a vCPU, as query-hotpluggable-cpus reports it.
type cpuSlot struct {
	Type    string         `json:"type"`
	QOMPath string         `json:"qom-path,omitempty"`
	Props   map[string]any `json:"props"`
}

// addedCPU is a vCPU added after exec, as a checkpoint records it to add it again before the
// state is loaded: the stream names each vCPU, and one that is not there fails the load.
type addedCPU struct {
	ID     string         `json:"id"`
	Driver string         `json:"driver"`
	Props  map[string]any `json:"props"`
}

func (m *QEMU) cpuSlots(ctx context.Context) ([]cpuSlot, error) {
	var slots []cpuSlot
	if err := m.q.run(ctx, "query-hotpluggable-cpus", nil, &slots); err != nil {
		return nil, fmt.Errorf("listing the machine's vCPU slots: %w", err)
	}
	// QEMU lists them highest first; added in this order, vCPU n is the n-th.
	slices.Reverse(slots)
	return slots, nil
}

// SetCPUs adds vCPUs into the first free slots, or removes the last ones added, until there are
// n. A removal returns once QEMU has let the vCPU go, which waits on the guest offlining it.
func (m *QEMU) SetCPUs(ctx context.Context, n int) error {
	slots, err := m.cpuSlots(ctx)
	if err != nil {
		return err
	}
	if n < 1 || n > len(slots) {
		return fmt.Errorf("%d vCPUs, and the machine has slots for 1 to %d", n, len(slots))
	}
	have := 0
	for _, s := range slots {
		if s.QOMPath != "" {
			have++
		}
	}
	for i, s := range slots {
		if have >= n {
			break
		}
		if s.QOMPath != "" {
			continue
		}
		args := map[string]any{"driver": s.Type, "id": fmt.Sprintf("vcpu%d", i)}
		for k, v := range s.Props {
			args[k] = v
		}
		if err := m.q.run(ctx, "device_add", args, nil); err != nil {
			return fmt.Errorf("adding vCPU %d: %w", i, err)
		}
		have++
	}
	for _, s := range slices.Backward(slots) {
		if have <= n {
			break
		}
		id, ok := strings.CutPrefix(s.QOMPath, peripheral)
		if !ok {
			// Boot vCPUs are not devices anyone added, and cannot be removed.
			continue
		}
		if err := m.q.run(ctx, "device_del", map[string]any{"id": id}, nil); err != nil {
			return fmt.Errorf("removing vCPU %s: %w", id, err)
		}
		if err := m.q.waitDeleted(ctx, id); err != nil {
			return fmt.Errorf("removing vCPU %s: %w", id, err)
		}
		have--
	}
	if have != n {
		return fmt.Errorf("%d vCPUs, and only %d can be removed: the rest are the ones the machine was started with", n, have)
	}
	return nil
}

// addedCPUs are the vCPUs added since exec: QEMU puts a device with an id under
// /machine/peripheral, and the boot vCPUs under /machine/unattached.
func (m *QEMU) addedCPUs(ctx context.Context) ([]addedCPU, error) {
	slots, err := m.cpuSlots(ctx)
	if err != nil {
		return nil, err
	}
	var out []addedCPU
	for _, s := range slots {
		if id, ok := strings.CutPrefix(s.QOMPath, peripheral); ok {
			out = append(out, addedCPU{ID: id, Driver: s.Type, Props: s.Props})
		}
	}
	return out, nil
}

const memPath = peripheral + VirtioMemID

// mib is a MiB in bytes, which QEMU counts memory in.
const mib = 1 << 20

// SetMemory asks virtio-mem for what is above the memory the machine booted with.
func (m *QEMU) SetMemory(ctx context.Context, mb int) error {
	var summary struct {
		Base int64 `json:"base-memory"`
	}
	if err := m.q.run(ctx, "query-memory-size-summary", nil, &summary); err != nil {
		return fmt.Errorf("reading the machine's memory: %w", err)
	}
	var ceiling int64
	// The backend's size, which is the ceiling: the device's own size is what it has plugged.
	if err := m.q.run(ctx, "qom-get", map[string]any{"path": "/objects/" + memGrowthID, "property": "size"}, &ceiling); err != nil {
		return fmt.Errorf("reading how far the machine's memory may grow: %w", err)
	}
	want := int64(mb)*mib - summary.Base
	if want < 0 || want > ceiling {
		return fmt.Errorf("%d MiB, and the machine has %d MiB to %d MiB", mb, summary.Base/mib, (summary.Base+ceiling)/mib)
	}
	if err := m.q.run(ctx, "qom-set", map[string]any{"path": memPath, "property": "requested-size", "value": want}, nil); err != nil {
		return fmt.Errorf("asking for %d MiB: %w", mb, err)
	}
	return nil
}

// Memory is the memory the guest boots with and what virtio-mem has plugged since.
func (m *QEMU) Memory(ctx context.Context) (int, error) {
	var summary struct {
		Base    int64 `json:"base-memory"`
		Plugged int64 `json:"plugged-memory"`
	}
	if err := m.q.run(ctx, "query-memory-size-summary", nil, &summary); err != nil {
		return 0, fmt.Errorf("reading the machine's memory: %w", err)
	}
	return int((summary.Base + summary.Plugged) / mib), nil
}

// Checkpoint formats. The format is not in the fingerprint, and a QEMU without mapped-ram, or
// told to load without it, cannot read a FormatMappedRAM state whatever the machine is: so it is
// recorded with the state (Saved) and checked before a byte is read.
const (
	// FormatMappedRAM is QEMU's migration stream with mapped-ram: each RAM page at a fixed
	// offset of the file, which is what lets a load read a page when the guest touches it
	// (Load.Fast). The file is sparse, as large as the machine's RAM blocks at their ceilings;
	// what is not written is holes.
	FormatMappedRAM = "qemu-mapped-ram"
	// FormatStream is the stream before it, every page in order: what a checkpoint saved by
	// a spin-machine before mapped-ram is, which still loads, but only in full.
	FormatStream = "qemu-stream"
)

// savedMachine is what a QEMU checkpoint needs besides its state (Saved.Machine).
type savedMachine struct {
	CPUs  []addedCPU `json:"cpus,omitempty"`
	Disks []hotDisk  `json:"disks,omitempty"`
}

// checkpointBandwidth lifts QEMU's default migration cap, 128 MiB/s, which is meant for a
// network: a checkpoint is a local file.
const checkpointBandwidth = int64(64) << 30

// Checkpoint saves the machine to a file in FormatMappedRAM.
//
// pause-before-switchover stops the migration with the guest paused and the disks quiet, after
// the last RAM is written and before the device state is: seal runs there. The vCPUs and disks
// added while the machine ran are read before the migration starts, because QEMU refuses a query
// of some of them once one is under way.
func (m *QEMU) Checkpoint(ctx context.Context, to *os.File, seal func(context.Context) error) (Saved, error) {
	var sm savedMachine
	var err error
	if sm.CPUs, err = m.addedCPUs(ctx); err != nil {
		return Saved{}, fmt.Errorf("checkpointing: %w", err)
	}
	if sm.Disks, err = m.hotDisks(ctx); err != nil {
		return Saved{}, fmt.Errorf("checkpointing: %w", err)
	}
	machine, err := json.Marshal(sm)
	if err != nil {
		return Saved{}, fmt.Errorf("checkpointing: recording the machine: %w", err)
	}
	target, err := m.Target(ctx)
	if err != nil {
		return Saved{}, fmt.Errorf("checkpointing: %w", err)
	}
	if err := m.setCapabilities(ctx, map[string]bool{"mapped-ram": true, "pause-before-switchover": true, "postcopy-ram": false}); err != nil {
		return Saved{}, fmt.Errorf("checkpointing: %w", err)
	}
	if err := m.q.run(ctx, "migrate-set-parameters", map[string]any{"max-bandwidth": checkpointBandwidth}, nil); err != nil {
		return Saved{}, fmt.Errorf("checkpointing: %w", err)
	}
	if err := m.migrate(ctx, "migrate", to, os.O_WRONLY, "state"); err != nil {
		return Saved{}, fmt.Errorf("checkpointing: %w", err)
	}
	if err := m.switchover(ctx, seal); err != nil {
		// A migration left at pre-switchover holds the guest paused with nobody to continue
		// it; a cancel with none running succeeds.
		cancelErr := m.q.run(context.WithoutCancel(ctx), "migrate_cancel", nil, nil)
		return Saved{}, errors.Join(fmt.Errorf("checkpointing: %w", err), cancelErr)
	}
	return Saved{Format: FormatMappedRAM, CPU: target.CPU, Fingerprint: target.Fingerprint, VMM: target.VMM, Machine: machine}, nil
}

// Target is what this machine resumes: the formats it reads, the fingerprint Args gave QEMU as
// its name, the QEMU it is and the CPU it shows.
func (m *QEMU) Target(ctx context.Context) (Target, error) {
	var name struct {
		Name string `json:"name"`
	}
	if err := m.q.run(ctx, "query-name", nil, &name); err != nil {
		return Target{}, fmt.Errorf("asking the machine's fingerprint: %w", err)
	}
	fingerprint, ok := strings.CutPrefix(name.Name, fingerprintName)
	if !ok {
		return Target{}, fmt.Errorf("the machine is named %q, not by a fingerprint: was it started from Spec.Args?", name.Name)
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
	v := version.QEMU
	return Target{
		Formats:     []string{FormatMappedRAM, FormatStream},
		Fingerprint: fingerprint,
		VMM:         fmt.Sprintf("qemu %d.%d.%d", v.Major, v.Minor, v.Micro),
		CPU:         cpu,
	}, nil
}

// switchover runs seal at pre-switchover and waits for the migration to complete.
func (m *QEMU) switchover(ctx context.Context, seal func(context.Context) error) error {
	for {
		st, err := m.migration(ctx)
		if err != nil {
			return err
		}
		switch st {
		case "completed":
			return nil
		case "pre-switchover":
			if err := seal(ctx); err != nil {
				return fmt.Errorf("sealing: %w", err)
			}
			if err := m.q.run(ctx, "migrate-continue", map[string]any{"state": "pre-switchover"}, nil); err != nil {
				return err
			}
		}
		if err := sleep(ctx, pollInterval); err != nil {
			return err
		}
	}
}

// migration is the state of the migration in flight, either direction, or an error for one that
// failed.
func (m *QEMU) migration(ctx context.Context) (string, error) {
	var info struct {
		Status    string `json:"status"`
		ErrorDesc string `json:"error-desc"`
	}
	if err := m.q.run(ctx, "query-migrate", nil, &info); err != nil {
		return "", err
	}
	if info.Status == "failed" || info.Status == "cancelled" {
		return "", fmt.Errorf("the migration %s: %s", info.Status, cmp.Or(info.ErrorDesc, "QEMU said nothing more"))
	}
	return info.Status, nil
}

func (m *QEMU) setCapabilities(ctx context.Context, caps map[string]bool) error {
	names := make([]string, 0, len(caps))
	for n := range caps {
		names = append(names, n)
	}
	slices.Sort(names)
	list := make([]map[string]any, 0, len(caps))
	for _, n := range names {
		list = append(list, map[string]any{"capability": n, "state": caps[n]})
	}
	if err := m.q.run(ctx, "migrate-set-capabilities", map[string]any{"capabilities": list}, nil); err != nil {
		return fmt.Errorf("setting the migration's capabilities: %w", err)
	}
	return nil
}

// migrate runs migrate or migrate-incoming over f, handed to QEMU as a descriptor set. The set
// is removed once the command is in: QEMU has its own copy of the descriptor and frees the set
// when it closes it.
//
// f is reopened for access, because QEMU takes from a set only a descriptor whose access mode
// is the one it opens the file with - write-only to save, read-only to load ("Failed to find file
// descriptor with matching flags") - and the caller's file need not be either.
func (m *QEMU) migrate(ctx context.Context, command string, f *os.File, access int, opaque string) error {
	if f == nil {
		return errors.New("no file for the state")
	}
	g, err := os.OpenFile(fmt.Sprintf("/proc/self/fd/%d", f.Fd()), access, 0) // mutate-exempt: the mode is read only with O_CREATE, which access never has
	if err != nil {
		return fmt.Errorf("reopening the state's file for QEMU: %w", err)
	}
	defer func() { _ = g.Close() }() // QEMU holds its own copy
	set, err := m.q.addFD(ctx, g, opaque)
	if err != nil {
		return err
	}
	err = m.q.run(ctx, command, map[string]any{"uri": fmt.Sprintf("file:/dev/fdset/%d", set)}, nil)
	if rmErr := m.q.run(ctx, "remove-fd", map[string]any{"fdset-id": set}, nil); rmErr != nil && err == nil {
		err = rmErr
	}
	return err
}

// Resume loads a checkpoint into a machine started with Spec.Restore.
//
// Fast is QEMU 11.2's fast snapshot load (docs/devel/migration/fast-snapshot-load.rst): mapped-ram
// and postcopy-ram on a file: URI, the guest running once its device state is in while
// userfaultfd serves its faults from the file and an eager thread reads the rest. It returns at
// postcopy-active. QEMU needs userfaultfd for it: /dev/userfaultfd readable and writable by its
// user, or vm.unprivileged_userfaultfd=1, or CAP_SYS_PTRACE - the guest's faults are taken in
// the kernel, which an unprivileged UFFD_USER_MODE_ONLY descriptor does not see.
func (m *QEMU) Resume(ctx context.Context, state *os.File, saved Saved, load Load, disks ...Disk) error {
	caps := map[string]bool{"pause-before-switchover": false}
	switch {
	case saved.Format == FormatMappedRAM:
		caps["mapped-ram"] = true
		caps["postcopy-ram"] = load.Fast
	case saved.Format == FormatStream && !load.Fast:
		caps["mapped-ram"] = false
	case saved.Format == FormatStream:
		return errors.New("resuming: a stream checkpoint has no page at a fixed offset to fetch when the guest touches it, and loads only in full")
	default:
		return fmt.Errorf("resuming: a checkpoint in format %q, and this machine reads %s and %s", saved.Format, FormatMappedRAM, FormatStream)
	}
	var sm savedMachine
	if len(saved.Machine) > 0 {
		if err := json.Unmarshal(saved.Machine, &sm); err != nil {
			return fmt.Errorf("resuming: reading what the checkpoint says of its machine: %w", err)
		}
	}
	st, err := m.status(ctx)
	if err != nil {
		return fmt.Errorf("resuming: %w", err)
	}
	if st != "inmigrate" {
		return fmt.Errorf("resuming into a machine that is %s: only one started with Spec.Restore takes a checkpoint", st)
	}
	here, err := m.Target(ctx)
	if err != nil {
		return fmt.Errorf("resuming: %w", err)
	}
	if err := CanResume(saved, here); err != nil {
		return fmt.Errorf("resuming: %w", err)
	}
	if err := m.bindAll(ctx, disks, sm.Disks); err != nil {
		return fmt.Errorf("resuming: %w", err)
	}
	for _, c := range sm.CPUs {
		args := map[string]any{"driver": c.Driver, "id": c.ID}
		for k, v := range c.Props {
			args[k] = v
		}
		if err := m.q.run(ctx, "device_add", args, nil); err != nil {
			return fmt.Errorf("resuming: adding vCPU %s: %w", c.ID, err)
		}
	}
	if err := m.setCapabilities(ctx, caps); err != nil {
		return fmt.Errorf("resuming: %w", err)
	}
	if err := m.migrate(ctx, "migrate-incoming", state, os.O_RDONLY, "state"); err != nil {
		return fmt.Errorf("resuming: %w", err)
	}
	for {
		st, err := m.migration(ctx)
		if err != nil {
			return fmt.Errorf("resuming: %w", err)
		}
		if st == "completed" || (load.Fast && st == "postcopy-active") {
			break
		}
		if err := sleep(ctx, pollInterval); err != nil {
			return fmt.Errorf("resuming: %w", err)
		}
	}
	// A machine saved stopped comes back stopped: QEMU carries the source's run state across.
	if st, err = m.status(ctx); err != nil {
		return fmt.Errorf("resuming: %w", err)
	}
	if st == "paused" {
		return m.Continue(ctx)
	}
	return nil
}

// Loaded returns once the load is complete.
func (m *QEMU) Loaded(ctx context.Context) error {
	for {
		st, err := m.migration(ctx)
		if err != nil {
			return fmt.Errorf("finishing the load: %w", err)
		}
		if st == "completed" {
			return nil
		}
		if err := sleep(ctx, pollInterval); err != nil {
			return fmt.Errorf("finishing the load: %w", err)
		}
	}
}
