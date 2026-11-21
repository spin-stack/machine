// SPDX-License-Identifier: Apache-2.0

package qmp

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/spin-stack/machine/internal/qmpdef"
)

// The commands spin's storage agent sends, some of them the machine's too: a disk's descriptors
// and block nodes, and the vCPUs stopped and let go around a change to them. They are exported
// because spin cannot define a command (qmpdef is internal). A command only the machine sends is
// defined, unexported, beside the code that sends it.
var (
	// Stop pauses the vCPUs.
	Stop = qmpdef.Define[None, None]("stop")
	// Cont lets the vCPUs go: a machine held at its start boots, and a paused one runs on.
	Cont = qmpdef.Define[None, None]("cont")
	// AddFD hands QEMU a descriptor, which a node or a migration then opens by its set's Path.
	AddFD = qmpdef.Define[AddFDArgs, FDInfo]("add-fd")
	// RemoveFD lets go of a set, or of one descriptor in it. QEMU closes what it took from a set
	// once nothing it opened from the set is open.
	RemoveFD = qmpdef.Define[RemoveFDArgs, None]("remove-fd")
	// QueryFDSets lists the sets QEMU holds.
	QueryFDSets = qmpdef.Define[None, []FDSet]("query-fdsets")
	// BlockdevAdd adds a node.
	BlockdevAdd = qmpdef.Define[BlockNode, None]("blockdev-add")
	// BlockdevDel deletes a node nothing reads.
	BlockdevDel = qmpdef.Define[BlockdevDelArgs, None]("blockdev-del")
	// BlockdevReopen changes nodes' options, each node stated again in full.
	BlockdevReopen = qmpdef.Define[BlockdevReopenArgs, None]("blockdev-reopen")
	// BlockdevSnapshot puts an overlay over a node, in its place under every parent.
	BlockdevSnapshot = qmpdef.Define[BlockdevSnapshotArgs, None]("blockdev-snapshot")
	// ChangeBackingFile rewrites the backing file an image's header names.
	ChangeBackingFile = qmpdef.Define[ChangeBackingFileArgs, None]("change-backing-file")
	// QueryBlock lists the devices that read a node, and the node each reads.
	QueryBlock = qmpdef.Define[None, []BlockInfo]("query-block")
	// QueryNamedBlockNodes lists the nodes.
	QueryNamedBlockNodes = qmpdef.Define[QueryNamedBlockNodesArgs, []BlockNodeInfo]("query-named-block-nodes")
)

// AddFDArgs hands File to QEMU, into a set of its own, or into Set when it is not nil. Opaque is
// what query-fdsets says the descriptor is: the machine and spin's storage name an image's set by
// the image's path, and find which machine has an image open by it.
type AddFDArgs struct {
	File   File   `json:"-"`
	Set    *int   `json:"fdset-id,omitempty"`
	Opaque string `json:"opaque,omitempty"`
}

func (a AddFDArgs) file() File { return a.File }

// FDInfo is the descriptor add-fd handed over: the set it is in, and its number in QEMU.
type FDInfo struct {
	Set int `json:"fdset-id"`
	FD  int `json:"fd"`
}

// fdsetPrefix is how QEMU names a set wherever it takes a filename.
const fdsetPrefix = "/dev/fdset/"

// Path is the filename a node or a migration opens the set by.
func (i FDInfo) Path() string { return fdsetPrefix + strconv.Itoa(i.Set) }

// FDSetOf is the set a filename QEMU reports names, and false for a filename that names none.
func FDSetOf(filename string) (int, bool) {
	rest, ok := strings.CutPrefix(filename, fdsetPrefix)
	set, err := strconv.Atoi(rest)
	return set, ok && err == nil
}

// RemoveFDArgs lets go of the set Set, or of its descriptor FD alone when FD is not nil.
type RemoveFDArgs struct {
	Set int  `json:"fdset-id"`
	FD  *int `json:"fd,omitempty"`
}

// FDSet is one set QEMU holds, and the descriptors in it.
type FDSet struct {
	ID  int `json:"fdset-id"`
	FDs []struct {
		FD     int    `json:"fd"`
		Opaque string `json:"opaque"`
	} `json:"fds"`
}

// BlockNode is a node's options, as blockdev-add takes them and blockdev-reopen states them again:
// one type for each driver this module opens, each writing its own driver, so a node cannot be
// sent as another driver or with an option its driver does not have.
type BlockNode interface{ blockNode() }

func init() { qmpdef.Union[BlockNode](FileNode{}, Qcow2Node{}, RawNode{}, ThrottleNode{}) }

// FileNode is a protocol node over a file, which here is always a descriptor set's Path.
type FileNode struct {
	NodeName string `json:"node-name"`
	Filename string `json:"filename"`
	AIO      string `json:"aio,omitempty"`
	Discard  string `json:"discard,omitempty"`
	ReadOnly bool   `json:"read-only"`
	// DropCache is sent every time, and false unless set, which QEMU's default is not: at a
	// machine's activation after an incoming migration, drop-cache drops the node's file from the
	// host's page cache, for a migration onto another host over shared storage. Here a layer below
	// the top is one file every VM on the host reads, and they all pay for it (the machine's
	// addImages has the measurement).
	DropCache bool   `json:"drop-cache"`
	Locking   string `json:"locking,omitempty"`
}

// Qcow2Node is a qcow2 image over its file's node. Backing is left out, NoBacking, or the node
// under it: a node with Backing left out opens what its image's header names.
type Qcow2Node struct {
	NodeName string `json:"node-name"`
	File     Ref    `json:"file"`
	ReadOnly bool   `json:"read-only"`
	Discard  string `json:"discard,omitempty"`
	Backing  Ref    `json:"backing,omitzero"`
}

// RawNode is a raw image over its file's node: every request passed through.
type RawNode struct {
	NodeName string `json:"node-name"`
	File     Ref    `json:"file"`
	ReadOnly bool   `json:"read-only"`
	Discard  string `json:"discard,omitempty"`
}

// ThrottleNode is a filter over File, limited by its throttle group: the machine's place for a
// disk is one in a group with no limits, so that it is a filter and changes nothing.
type ThrottleNode struct {
	NodeName      string `json:"node-name"`
	File          Ref    `json:"file"`
	ThrottleGroup string `json:"throttle-group"`
	ReadOnly      bool   `json:"read-only"`
	Discard       string `json:"discard,omitempty"`
}

func (FileNode) blockNode()     {}
func (Qcow2Node) blockNode()    {}
func (RawNode) blockNode()      {}
func (ThrottleNode) blockNode() {}

// MarshalJSON writes the node's driver, then its options.
func (n FileNode) MarshalJSON() ([]byte, error) {
	type options FileNode
	return json.Marshal(struct {
		Driver string `json:"driver"`
		options
	}{"file", options(n)})
}

// MarshalJSON writes the node's driver, then its options.
func (n Qcow2Node) MarshalJSON() ([]byte, error) {
	type options Qcow2Node
	return json.Marshal(struct {
		Driver string `json:"driver"`
		options
	}{"qcow2", options(n)})
}

// MarshalJSON writes the node's driver, then its options.
func (n RawNode) MarshalJSON() ([]byte, error) {
	type options RawNode
	return json.Marshal(struct {
		Driver string `json:"driver"`
		options
	}{"raw", options(n)})
}

// MarshalJSON writes the node's driver, then its options.
func (n ThrottleNode) MarshalJSON() ([]byte, error) {
	type options ThrottleNode
	return json.Marshal(struct {
		Driver string `json:"driver"`
		options
	}{"throttle", options(n)})
}

// Ref is a node's child: left out (the zero Ref), none (NoBacking), a node by its name (NodeRef),
// or a node defined in place (Inline). It has a state a pointer does not: a backing of null is not
// a backing left out.
type Ref struct {
	name   string
	inline BlockNode
	none   bool
}

// NoBacking is a node's backing when it is to have none, whatever its image's header names: a
// header is a path the guest can write, and a node opens no file it was not handed.
var NoBacking = Ref{none: true}

// NodeRef is the node named name.
func NodeRef(name string) Ref { return Ref{name: name} }

// Inline is the node n, added with the node it is a child of and deleted with it.
func Inline(n BlockNode) Ref { return Ref{inline: n} }

// IsZero is whether r was left out, which omitzero leaves off the wire.
func (r Ref) IsZero() bool { return r.name == "" && r.inline == nil && !r.none }

// MarshalJSON writes r as QEMU takes a child: null, a node's name, or its options.
func (r Ref) MarshalJSON() ([]byte, error) {
	switch {
	case r.none:
		return []byte("null"), nil
	case r.inline != nil:
		return json.Marshal(r.inline)
	}
	return json.Marshal(r.name)
}

// BlockdevDelArgs names the node to delete.
type BlockdevDelArgs struct {
	NodeName string `json:"node-name"`
}

// BlockdevReopenArgs are the nodes to reopen, each with every option it is to have.
type BlockdevReopenArgs struct {
	Options []BlockNode `json:"options"`
}

// BlockdevSnapshotArgs puts the node Overlay, which has no backing yet, over the node Node.
type BlockdevSnapshotArgs struct {
	Node    string `json:"node"`
	Overlay string `json:"overlay"`
}

// ChangeBackingFileArgs has the image node ImageNodeName, in the chain under the device or node
// Device, name BackingFile as its backing file.
type ChangeBackingFileArgs struct {
	Device        string `json:"device"`
	ImageNodeName string `json:"image-node-name"`
	BackingFile   string `json:"backing-file"`
}

// QueryNamedBlockNodesArgs asks for every node, Flat leaving out each node's image's backing
// images, which the nodes under it say again.
type QueryNamedBlockNodesArgs struct {
	Flat bool `json:"flat"`
}

// BlockInfo is one of query-block's devices, and the node it reads, nil when it reads none: an
// empty tray.
type BlockInfo struct {
	Device   string         `json:"device"`
	QDev     string         `json:"qdev"`
	Inserted *BlockNodeInfo `json:"inserted"`
}

// BlockNodeInfo is a node as QEMU reports it: each of query-named-block-nodes', and what a
// query-block device reads.
type BlockNodeInfo struct {
	NodeName string `json:"node-name"`
	Driver   string `json:"drv"`
	// File is the node's filename: a set's Path for a node over a descriptor, or json:{...} for a
	// node whose children were given as options.
	File     string    `json:"file"`
	ReadOnly bool      `json:"ro"`
	Image    ImageInfo `json:"image"`
	// ReadBytes, WriteBytes, Reads and Writes are the IO limit of the device that reads the node,
	// a second, set by block_set_io_throttle: 0 for none, and for a node no device reads.
	ReadBytes  int64 `json:"bps_rd"`
	WriteBytes int64 `json:"bps_wr"`
	Reads      int64 `json:"iops_rd"`
	Writes     int64 `json:"iops_wr"`
}

// ImageInfo is a node's image as QEMU reads it, and the image under it.
type ImageInfo struct {
	Filename string
	// Corrupt is qcow2 having found the image's metadata inconsistent, which leaves it unwritable.
	Corrupt bool
	Backing *ImageInfo
}

// UnmarshalJSON reads QEMU's ImageInfo, in which Corrupt is a qcow2 image's format-specific data.
func (i *ImageInfo) UnmarshalJSON(b []byte) error {
	var info struct {
		Filename       string     `json:"filename"`
		Backing        *ImageInfo `json:"backing-image"`
		FormatSpecific struct {
			Data struct {
				Corrupt bool `json:"corrupt"`
			} `json:"data"`
		} `json:"format-specific"`
	}
	if err := json.Unmarshal(b, &info); err != nil {
		return err
	}
	*i = ImageInfo{Filename: info.Filename, Corrupt: info.FormatSpecific.Data.Corrupt, Backing: info.Backing}
	return nil
}
