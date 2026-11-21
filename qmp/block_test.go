// SPDX-License-Identifier: Apache-2.0

package qmp

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// Each node writes its own driver first and then its options, and a child is left out, null, a
// node's name or a node in place: the bytes QEMU is sent, -blockdev's included.
func TestANodeIsSentAsItsDriver(t *testing.T) {
	for _, tc := range []struct {
		name string
		node BlockNode
		want string
	}{
		{"a file", FileNode{NodeName: "d-0-f", Filename: "/dev/fdset/3", AIO: "io_uring", Discard: "unmap", ReadOnly: true, Locking: "on"},
			`{"driver":"file","node-name":"d-0-f","filename":"/dev/fdset/3","aio":"io_uring","discard":"unmap","read-only":true,"drop-cache":false,"locking":"on"}`},
		{"a file with only what it must have", FileNode{NodeName: "f", Filename: "/dev/fdset/1"},
			`{"driver":"file","node-name":"f","filename":"/dev/fdset/1","read-only":false,"drop-cache":false}`},
		{"qcow2 backed by a node", Qcow2Node{NodeName: "d-0", File: NodeRef("d-0-f"), Discard: "unmap", Backing: NodeRef("d-1")},
			`{"driver":"qcow2","node-name":"d-0","file":"d-0-f","read-only":false,"discard":"unmap","backing":"d-1"}`},
		{"qcow2 backed by nothing", Qcow2Node{NodeName: "d-1", File: NodeRef("d-1-f"), ReadOnly: true, Backing: NoBacking},
			`{"driver":"qcow2","node-name":"d-1","file":"d-1-f","read-only":true,"backing":null}`},
		{"qcow2 backed by what its header names", Qcow2Node{NodeName: "o", File: NodeRef("o-f")},
			`{"driver":"qcow2","node-name":"o","file":"o-f","read-only":false}`},
		{"qcow2 over a file in place", Qcow2Node{NodeName: "o", File: Inline(FileNode{NodeName: "o-f", Filename: "/dev/fdset/2"}), Backing: NoBacking},
			`{"driver":"qcow2","node-name":"o","file":{"driver":"file","node-name":"o-f","filename":"/dev/fdset/2","read-only":false,"drop-cache":false},"read-only":false,"backing":null}`},
		{"raw", RawNode{NodeName: "d-0", File: NodeRef("d-0-f"), ReadOnly: true, Discard: "unmap"},
			`{"driver":"raw","node-name":"d-0","file":"d-0-f","read-only":true,"discard":"unmap"}`},
		{"a throttle filter", ThrottleNode{NodeName: "slot0", File: NodeRef("slot0-empty"), ThrottleGroup: "places", ReadOnly: true, Discard: "unmap"},
			`{"driver":"throttle","node-name":"slot0","file":"slot0-empty","throttle-group":"places","read-only":true,"discard":"unmap"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.node)
			if err != nil || string(got) != tc.want {
				t.Errorf("%s, %v\nwant %s", got, err, tc.want)
			}
		})
	}
	if got, err := json.Marshal(BlockdevReopenArgs{Options: []BlockNode{RawNode{NodeName: "r", File: NodeRef("f")}}}); err != nil ||
		string(got) != `{"options":[{"driver":"raw","node-name":"r","file":"f","read-only":false}]}` {
		t.Errorf("a reopen of a raw node = %s, %v", got, err)
	}
}

// A set's Path is the filename QEMU reports for a node over it, and FDSetOf reads it back; any
// other filename names no set.
func TestASetIsNamedByItsPath(t *testing.T) {
	if got := (FDInfo{Set: 12, FD: 40}).Path(); got != "/dev/fdset/12" {
		t.Errorf("Path = %q", got)
	}
	for name, want := range map[string]int{"/dev/fdset/12": 12, "/dev/fdset/0": 0} {
		if set, ok := FDSetOf(name); !ok || set != want {
			t.Errorf("FDSetOf(%q) = %d, %v; want %d", name, set, ok, want)
		}
	}
	for _, name := range []string{"/dev/fdset/", "/dev/fdset/x", "12", "/var/lib/disk.qcow2", `json:{"file": {"filename": "/dev/fdset/3"}}`} {
		if set, ok := FDSetOf(name); ok {
			t.Errorf("FDSetOf(%q) = %d, want none", name, set)
		}
	}
}

// A device's node is read with its image, whether qcow2 found it corrupt, and the images under it.
func TestABlockDeviceIsReadWithItsImages(t *testing.T) {
	var got []BlockInfo
	answer := `[{"device": "", "qdev": "/machine/peripheral/disk0/virtio-backend",
		"inserted": {"node-name": "slot0", "drv": "throttle", "file": "json:{}", "ro": true,
			"image": {"filename": "/dev/fdset/3", "format-specific": {"type": "qcow2", "data": {"corrupt": true}},
				"backing-image": {"filename": "/dev/fdset/4"}}}},
		{"device": "cd", "qdev": "cd0"}]`
	if err := json.Unmarshal([]byte(answer), &got); err != nil {
		t.Fatal(err)
	}
	want := []BlockInfo{
		{QDev: "/machine/peripheral/disk0/virtio-backend", Inserted: &BlockNodeInfo{NodeName: "slot0", Driver: "throttle", File: "json:{}", ReadOnly: true,
			Image: ImageInfo{Filename: "/dev/fdset/3", Corrupt: true, Backing: &ImageInfo{Filename: "/dev/fdset/4"}}}},
		{Device: "cd", QDev: "cd0"},
	}
	if !reflect.DeepEqual(got, want) {
		g, _ := json.Marshal(got)
		t.Errorf("read %s", g)
	}
	var img ImageInfo
	if err := json.Unmarshal([]byte(`{"filename": 3}`), &img); err == nil {
		t.Error("an image whose filename is a number was read")
	}
}

// add-fd sends its File beside the line, and answers the set the descriptor went into.
func TestAddFDSendsItsFile(t *testing.T) {
	c, m := start(t, Observer{})
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	set := 7
	got := do(bounded(t), c, AddFD, AddFDArgs{File: f, Set: &set, Opaque: "/var/lib/disk"})
	s, ok := m.next()
	if !ok || s.file != File(f) || string(s.Arguments) != `{"fdset-id":7,"opaque":"/var/lib/disk"}` {
		t.Fatalf("sent %s with %v", s.line, s.file)
	}
	if err := m.answer(s, `{"fdset-id": 7, "fd": 40}`); err != nil {
		t.Fatal(err)
	}
	if r := await(t, got); r.err != nil || r.r != (FDInfo{Set: 7, FD: 40}) {
		t.Errorf("add-fd = %+v, %v", r.r, r.err)
	}
}
