// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"

	"github.com/spin-stack/machine/internal/qmpdef"
	"github.com/spin-stack/machine/qmp"
)

// Disk is one disk of a VM: an image and every image under it, top first, each opened by the
// caller. A VMM reads no image by path and follows no backing file an image's header names:
// what it reads is what it was handed.
type Disk struct {
	// Serial is the disk's name to the guest, which finds it by this and not by the order the
	// disks were probed in. At most 20 characters of A-Z, a-z, 0-9, '-' and '_'.
	Serial string
	// Images are the disk's images, top first. Each is opened for what it will be: the top
	// read-write unless the disk is Readonly, every other image read-only.
	Images []Image
	// Readonly gives the guest a disk it cannot write.
	Readonly bool
	// Locking has the VMM hold an image lock on the top image, for a caller outside it that
	// takes the same lock to find out whether a VM has the image open.
	Locking bool
}

// Image is one image of a Disk.
type Image struct {
	File *os.File
	// Format is qcow2 or raw, and never guessed: a guessed format is how an image the guest
	// can write is talked into being read as another one. Only the last image may be raw.
	Format string
	// Size is the size the guest sees, as OpenDisk read it: a qcow2 header's, a raw file's.
	// Nothing here reads it; a caller that bounds what its VMM may write to a file
	// (RLIMIT_FSIZE) bounds it by the writable top's.
	Size int64
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
//
// The names are this package's own. spin's storage, which seals a disk's top under a new overlay
// (blockdev-snapshot) through a monitor of its own, finds the node it seals by what it reads and
// not by its name (its qcow.topOf): the qcow2 node whose file is the descriptor set whose opaque
// is the top image's path (addImages). That node is not the one the device reads. A disk bound
// to a place stays under the place's node, slot<i>, a filter that is not qcow2 (placeNode),
// because QEMU 11.2 has no command that replaces a realized device's node (blockdev-replace is
// not in it); blockdev-snapshot of the top puts the overlay in its place under every parent, the
// place included.
func diskNode(serial string, j int) string { return fmt.Sprintf("d-%s-%d", serial, j) }

// slot is one place for a disk the machine was started with (Spec.Places).
type slot struct {
	index    int
	serial   string
	readonly bool
}

// slots are the machine's places for disks, read from QEMU: each is a device over a node named
// slot<i> (appendDisks).
func (m *QEMU) slots(ctx context.Context) ([]slot, error) {
	blocks, err := qmp.Do(ctx, m.c, qmp.QueryBlock, qmp.None{})
	if err != nil {
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
		serial, err := readProperty[string](ctx, m, peripheral+slotDevice(i), "serial")
		if err != nil {
			return nil, fmt.Errorf("reading the serial of disk place %d: %w", i, err)
		}
		out = append(out, slot{index: i, serial: serial, readonly: b.Inserted.ReadOnly})
	}
	return out, nil
}

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
	if _, err := qmp.Do(ctx, m.c, qmp.BlockdevReopen, qmp.BlockdevReopenArgs{Options: []qmp.BlockNode{place}}); err != nil {
		return fmt.Errorf("disk %q: putting it in its place: %w", d.Serial, err)
	}
	if _, err := qmp.Do(ctx, m.c, qmp.BlockdevDel, qmp.BlockdevDelArgs{NodeName: emptyNode(s.index)}); err != nil {
		return fmt.Errorf("disk %q: deleting its place's placeholder: %w", d.Serial, err)
	}
	return nil
}

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
		// copy of it too. spin's storage relies on both (spin-stack/machine#297): it finds
		// which machine has an image open by query-fdsets' opaques, and after sealing a layer it
		// drops the writable copy, leaving QEMU the read-only one to reopen the layer with -
		// QEMU takes from a set only a descriptor of the access mode it asks for.
		opaque := img.File.Name()
		set, err := qmp.Do(ctx, m.c, qmp.AddFD, qmp.AddFDArgs{File: img.File, Opaque: opaque})
		if err != nil {
			return "", fmt.Errorf("disk %q: handing %s to QEMU: %w", d.Serial, opaque, err)
		}
		if !readOnly {
			if err := m.addReadOnlyCopy(ctx, set.Set, img.File, opaque); err != nil {
				return "", fmt.Errorf("disk %q: %w", d.Serial, err)
			}
		}
		file := qmp.FileNode{NodeName: node + "-f", Filename: set.Path(), AIO: "io_uring", Discard: "unmap", ReadOnly: readOnly}
		if j == 0 && d.Locking {
			file.Locking = "on"
		}
		var format qmp.BlockNode = qmp.RawNode{NodeName: node, File: qmp.NodeRef(node + "-f"), ReadOnly: readOnly, Discard: "unmap"}
		if img.Format != "raw" {
			// The last image must not follow its header, and a raw image, which validate allows
			// only last, has no backing to give.
			backing := qmp.NoBacking
			if j < len(d.Images)-1 {
				backing = qmp.NodeRef(diskNode(d.Serial, j+1))
			}
			format = qmp.Qcow2Node{NodeName: node, File: qmp.NodeRef(node + "-f"), ReadOnly: readOnly, Discard: "unmap", Backing: backing}
		}
		for _, n := range []qmp.BlockNode{file, format} {
			if _, err := qmp.Do(ctx, m.c, qmp.BlockdevAdd, n); err != nil {
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
	if _, err := qmp.Do(ctx, m.c, qmp.AddFD, qmp.AddFDArgs{File: ro, Set: &set, Opaque: opaque}); err != nil {
		return fmt.Errorf("handing a read-only copy of %s to QEMU: %w", opaque, err)
	}
	return nil
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
	if target < 0 || target >= maxHotplugDisks {
		return fmt.Errorf("disk %q: target %d, and the controller addresses 0 to %d", d.Serial, target, maxHotplugDisks-1)
	}
	top, err := m.addImages(ctx, d)
	if err != nil {
		return err
	}
	if _, err := qmp.Do(ctx, m.c, deviceAdd, hotplugDisk(target, top, d.Serial)); err != nil {
		// What was added for a device that never came goes with it: its images open, and its
		// node names taken for the next attach of the same serial.
		return errors.Join(fmt.Errorf("disk %q: attaching it at target %d: %w", d.Serial, target, err),
			m.releaseImages(context.WithoutCancel(ctx), d.Serial))
	}
	return nil
}

// hotplugDisk is the device_add arguments for the disk arriving at hotplug target i, over the
// block node named node: a scsi-hd at target i of the machine's controller, LUN 0, so the
// target is the disk's address and two disks cannot be given the same one. The serial is the
// disk's name to the guest, as a virtio-blk's is, read there from the unit serial number page
// (VPD page 0x80) rather than from /sys/block/*/serial.
func hotplugDisk(i int, node, serial string) map[string]any {
	return map[string]any{
		"driver":  "scsi-hd",
		"id":      hotDiskID(i),
		"drive":   node,
		"bus":     hotplugBus,
		"scsi-id": i,
		"lun":     0,
		"serial":  serial,
	}
}

// hotDiskID is the id of the device at hotplug target i, so that the target is found from the
// device and the device from the target.
func hotDiskID(i int) string { return fmt.Sprintf("hd%d", i) }

var qomList = qmpdef.Define[qomPath, []qomChild]("qom-list")

type qomPath struct {
	Path string `json:"path"`
}

type qomChild struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// hotDisks are the disks attached while the machine ran: the devices named hd<target>.
func (m *QEMU) hotDisks(ctx context.Context) ([]hotDisk, error) {
	children, err := qmp.Do(ctx, m.c, qomList, qomPath{Path: peripheral})
	if err != nil {
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
		serial, err := readProperty[string](ctx, m, peripheral+c.Name, "serial")
		if err != nil {
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
	if err := m.unplug(ctx, id); err != nil {
		return fmt.Errorf("detaching disk %q: %w", serial, err)
	}
	return m.releaseImages(ctx, serial)
}

// releaseImages deletes a disk's image nodes, the top first since a node cannot go while
// another is backed by it, and lets go of the descriptor sets they were opened from.
//
// The sets are found through the nodes - each file node reads /dev/fdset/<id> - and not by
// their opaque, which is the image's path and says nothing of which disk it is now. QEMU keeps
// a set until it is removed, and so keeps the image open: a detached disk whose sets stayed
// would still be readable, or writable, by the machine it left (spin-stack/spin#1367's review).
func (m *QEMU) releaseImages(ctx context.Context, serial string) error {
	nodes, err := qmp.Do(ctx, m.c, qmp.QueryNamedBlockNodes, qmp.QueryNamedBlockNodesArgs{Flat: true})
	if err != nil {
		return fmt.Errorf("listing disk %q's nodes: %w", serial, err)
	}
	var sets []int
	for j := 0; ; j++ {
		k := slices.IndexFunc(nodes, func(n qmp.BlockNodeInfo) bool { return n.NodeName == diskNode(serial, j)+"-f" })
		if k < 0 {
			break
		}
		if set, ok := qmp.FDSetOf(nodes[k].File); ok {
			sets = append(sets, set)
		}
		for _, n := range []string{diskNode(serial, j), diskNode(serial, j) + "-f"} {
			if _, err := qmp.Do(ctx, m.c, qmp.BlockdevDel, qmp.BlockdevDelArgs{NodeName: n}); err != nil {
				return fmt.Errorf("deleting disk %q's node %s: %w", serial, n, err)
			}
		}
	}
	for _, set := range sets {
		if _, err := qmp.Do(ctx, m.c, qmp.RemoveFD, qmp.RemoveFDArgs{Set: set}); err != nil {
			return fmt.Errorf("letting go of disk %q's descriptor set %d: %w", serial, set, err)
		}
	}
	return nil
}

// DiskLimit is how fast one disk may go, each field a ceiling a second and 0 for none: the zero
// DiskLimit is no limit. ReadBytes and WriteBytes are bytes, Reads and Writes are requests.
//
// QEMU lets a disk run a tenth of a second's worth over its rate before it holds it (a bucket of
// avg/10, throttle_fix_bucket), so a limit holds within the second and needs no burst of its own.
type DiskLimit struct {
	ReadBytes, WriteBytes int64
	Reads, Writes         int64
}

var blockSetIOThrottle = qmpdef.Define[ioThrottle, qmp.None]("block_set_io_throttle")

// ioThrottle is block_set_io_throttle's arguments. Every limit is required, so each is sent each
// time. The totals stay 0: a total and its read or write limit cannot both be set.
type ioThrottle struct {
	ID         string `json:"id"`
	Bytes      int64  `json:"bps"`
	ReadBytes  int64  `json:"bps_rd"`
	WriteBytes int64  `json:"bps_wr"`
	IOs        int64  `json:"iops"`
	Reads      int64  `json:"iops_rd"`
	Writes     int64  `json:"iops_wr"`
}

// SetDiskLimit holds the disk with this serial to l, at once and while the machine runs, and the
// zero DiskLimit lets it go. The guest is not told: its requests wait in QEMU.
//
// The limit is the device's, on its block backend, and not the place's throttle node's. The node's
// group is every place's (placeGroup), so a limit there would hold the machine's disks together,
// where the backend's group is the device's alone, and the backend is the one thing a bind does not
// replace. Nothing is added to the command line, so a boot pays nothing for a limit it may never
// be given.
//
// A limit is the running QEMU's, as io.max is its cgroup's. A checkpoint does not carry it, and a
// machine resumed from one runs without it. spin keeps the policy - who, how much, for how long -
// and sets it again after a resume if it still holds.
func (m *QEMU) SetDiskLimit(ctx context.Context, serial string, l DiskLimit) error {
	id, b, err := m.diskBackend(ctx, serial)
	if err != nil {
		return err
	}
	// The backend's qdev path and not the device's id: a place's id names its virtio-blk-pci,
	// which has no backend of its own ("Device does not have a block device backend").
	args := ioThrottle{ID: b.QDev, ReadBytes: l.ReadBytes, WriteBytes: l.WriteBytes, Reads: l.Reads, Writes: l.Writes}
	if _, err := qmp.Do(ctx, m.c, blockSetIOThrottle, args); err != nil {
		return fmt.Errorf("limiting disk %q to %+v: %w", serial, l, err)
	}
	m.held.set(serial, id, l)
	return nil
}

// DiskLimit is the limit the disk with this serial is held to, as QEMU reports it.
func (m *QEMU) DiskLimit(ctx context.Context, serial string) (DiskLimit, error) {
	_, b, err := m.diskBackend(ctx, serial)
	if err != nil {
		return DiskLimit{}, err
	}
	n := b.Inserted
	return DiskLimit{ReadBytes: n.ReadBytes, WriteBytes: n.WriteBytes, Reads: n.Reads, Writes: n.Writes}, nil
}

// diskBackend is the id of the device the disk with this serial is in, and the device's backend
// as query-block lists it.
func (m *QEMU) diskBackend(ctx context.Context, serial string) (string, qmp.BlockInfo, error) {
	id, err := m.diskDevice(ctx, serial)
	if err != nil {
		return "", qmp.BlockInfo{}, err
	}
	blocks, err := qmp.Do(ctx, m.c, qmp.QueryBlock, qmp.None{})
	if err != nil {
		return "", qmp.BlockInfo{}, fmt.Errorf("finding disk %q's backend: %w", serial, err)
	}
	for _, b := range blocks {
		if b.Inserted != nil && deviceOf(b.QDev) == id {
			return id, b, nil
		}
	}
	return "", qmp.BlockInfo{}, fmt.Errorf("finding disk %q's backend: QEMU lists no disk in device %s", serial, id)
}

// diskDevice is the id of the device the disk with this serial is in: its place's, or the one it
// was attached as while the machine ran.
func (m *QEMU) diskDevice(ctx context.Context, serial string) (string, error) {
	slots, err := m.slots(ctx)
	if err != nil {
		return "", err
	}
	if i := slices.IndexFunc(slots, func(s slot) bool { return s.serial == serial }); i >= 0 {
		return slotDevice(slots[i].index), nil
	}
	hot, err := m.hotDisks(ctx)
	if err != nil {
		return "", err
	}
	if i := slices.IndexFunc(hot, func(h hotDisk) bool { return h.Serial == serial }); i >= 0 {
		return hotDiskID(hot[i].Target), nil
	}
	return "", fmt.Errorf("the machine has no disk %q", serial)
}
