// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// call is one command a VM sent, with its arguments decoded, and whether a descriptor came with it.
type call struct {
	Command string
	Args    map[string]any
	FD      bool
}

// fakeQEMU answers a VM's commands from a function, as QEMU would, and keeps what it was asked:
// the tests here hold a VM to the conversation QEMU needs - which commands, in which order, with
// what - and to what it makes of the answers.
type fakeQEMU struct {
	// mu guards calls: serve appends on its own goroutine while the test reads them.
	mu     sync.Mutex
	calls  []call
	answer func(c call) string
}

// recorded is a copy of the calls so far.
func (f *fakeQEMU) recorded() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// testFingerprint is the machine every fake QEMU says it is (query-name), at QEMU 11.2.0.
const testFingerprint = "f00d"

// ok is QEMU's reply to a command that returns nothing.
const ok = `{"return": {}}`

// ret is a reply returning v, on one line as QEMU writes every message.
func ret(v string) string {
	var b bytes.Buffer
	if err := json.Compact(&b, []byte(`{"return": `+v+`}`)); err != nil {
		panic(fmt.Sprintf("a reply that is not JSON: %s", v))
	}
	return b.String()
}

func refused(desc string) string {
	return `{"error": {"class": "GenericError", "desc": "` + desc + `"}}`
}

// attach serves f on a socket and returns a VM attached to it.
func (f *fakeQEMU) attach(t *testing.T) *QEMU {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "vmt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "q"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := l.AcceptUnix()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		f.serve(c)
	}()
	conn, err := net.DialUnix("unix", nil, l.Addr().(*net.UnixAddr))
	if err != nil {
		t.Fatal(err)
	}
	m, err := Attach(conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close(); <-done })
	return m
}

func (f *fakeQEMU) serve(c *net.UnixConn) {
	w := bufio.NewWriter(c)
	_, _ = w.WriteString(`{"QMP": {"version": {}, "capabilities": []}}` + "\n")
	_ = w.Flush()
	buf := make([]byte, 1<<16)
	oob := make([]byte, syscall.CmsgSpace(4))
	var pending []byte
	for {
		n, oobn, _, _, err := c.ReadMsgUnix(buf, oob)
		if err != nil {
			return
		}
		fd := false
		if oobn > 0 {
			msgs, _ := syscall.ParseSocketControlMessage(oob[:oobn])
			for _, m := range msgs {
				fds, _ := syscall.ParseUnixRights(&m)
				for _, d := range fds {
					fd = true
					_ = syscall.Close(d)
				}
			}
		}
		pending = append(pending, buf[:n]...)
		for {
			i := strings.IndexByte(string(pending), '\n')
			if i < 0 {
				break
			}
			var req struct {
				Execute   string         `json:"execute"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(pending[:i], &req)
			pending = pending[i+1:]
			cl := call{Command: req.Execute, Args: req.Arguments, FD: fd}
			fd = false
			reply := ok
			switch cl.Command {
			case "query-name":
				reply = ret(`{"name": "` + fingerprintName + testFingerprint + `"}`)
			case "query-version":
				reply = ret(`{"qemu": {"major": 11, "minor": 2, "micro": 0}}`)
			}
			if cl.Command != "qmp_capabilities" && cl.Command != "query-name" && cl.Command != "query-version" {
				f.mu.Lock()
				f.calls = append(f.calls, cl)
				f.mu.Unlock()
				reply = f.answer(cl)
			}
			_, _ = w.WriteString(reply + "\n")
			_ = w.Flush()
		}
	}
}

// commands is the commands asked, in order.
func (f *fakeQEMU) commands() []string {
	var out []string
	for _, c := range f.recorded() {
		out = append(out, c.Command)
	}
	return out
}

// sent is the arguments of every call of command, in order.
func (f *fakeQEMU) sent(command string) []map[string]any {
	var out []map[string]any
	for _, c := range f.recorded() {
		if c.Command == command {
			out = append(out, c.Args)
		}
	}
	return out
}

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return c
}

// image is an open file for a Disk, which the fake reads nothing from.
func image(t *testing.T, format string) Image {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "img")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return Image{File: f, Format: format}
}

// machineAnswers is a machine held for a boot with one writable place for a disk, root.
func machineAnswers(status string, places ...string) func(c call) string {
	return func(c call) string {
		switch c.Command {
		case "query-status":
			return ret(`{"status": "` + status + `"}`)
		case "query-block":
			var b []string
			for i, p := range places {
				ro := strings.HasSuffix(p, ":ro")
				b = append(b, fmt.Sprintf(`{"qdev": "/machine/peripheral/disk%d/virtio-backend", "inserted": {"node-name": "slot%d", "ro": %t}}`, i, i, ro))
			}
			// A device with nothing in it and a node that is not a place are not places.
			b = append(b, `{"qdev": "cd"}`, `{"qdev": "x", "inserted": {"node-name": "other", "ro": false}}`)
			return ret("[" + strings.Join(b, ",") + "]")
		case "qom-get":
			var i int
			if _, err := fmt.Sscanf(c.Args["path"].(string), "/machine/peripheral/disk%d", &i); err == nil {
				return ret(`"` + strings.TrimSuffix(places[i], ":ro") + `"`)
			}
		case "add-fd":
			return ret(`{"fdset-id": 7, "fd": 20}`)
		}
		return ok
	}
}

// A boot binds each disk to the place with its serial, the base image first, then lets the vCPUs go.
func TestBootBindsEachDiskToItsPlaceThenRuns(t *testing.T) {
	f := &fakeQEMU{answer: machineAnswers("prelaunch", "root", "home:ro")}
	m := f.attach(t)
	root := Disk{Serial: "root", Locking: true, DirectOverBacking: true, Images: []Image{image(t, "qcow2"), image(t, "raw")}}
	home := Disk{Serial: "home", Readonly: true, Images: []Image{image(t, "qcow2")}}
	if err := m.Boot(ctx(t), root, home); err != nil {
		t.Fatal(err)
	}
	want := []string{"query-status", "query-block", "qom-get", "qom-get",
		"add-fd", "blockdev-add", "blockdev-add", "add-fd", "add-fd", "blockdev-add", "blockdev-add", "blockdev-reopen", "blockdev-del",
		"add-fd", "blockdev-add", "blockdev-add", "blockdev-reopen", "blockdev-del",
		"cont"}
	if got := f.commands(); !reflect.DeepEqual(got, want) {
		t.Fatalf("commands\n%v\nwant\n%v", got, want)
	}
	// Each set is named by its image's path, and the writable top's has a read-only copy too
	// (spin-machine#297): spin's storage finds an open image by its path, and keeps a sealed
	// layer open through that copy.
	wantSets := []map[string]any{
		{"opaque": root.Images[1].File.Name()},
		{"opaque": root.Images[0].File.Name()},
		{"opaque": root.Images[0].File.Name(), "fdset-id": float64(7)},
		{"opaque": home.Images[0].File.Name()},
	}
	if got := f.sent("add-fd"); !reflect.DeepEqual(got, wantSets) {
		t.Errorf("add-fd\n%v\nwant\n%v", got, wantSets)
	}
	for _, c := range f.recorded() {
		if c.FD != (c.Command == "add-fd") {
			t.Errorf("%s sent a descriptor: %t", c.Command, c.FD)
		}
	}
	nodes := f.sent("blockdev-add")
	wantNodes := []map[string]any{
		// The base: read-only, cached on the host, with no backing option at all, being raw.
		{"driver": "file", "node-name": "d-root-1-f", "filename": "/dev/fdset/7", "aio": "io_uring", "discard": "unmap",
			"read-only": true, "drop-cache": false, "cache": map[string]any{"direct": false}},
		{"driver": "raw", "node-name": "d-root-1", "file": "d-root-1-f", "read-only": true, "discard": "unmap"},
		// The top: written, locked, around the host's cache, and backed by the base.
		{"driver": "file", "node-name": "d-root-0-f", "filename": "/dev/fdset/7", "aio": "io_uring", "discard": "unmap",
			"read-only": false, "drop-cache": false, "locking": "on", "cache": map[string]any{"direct": true}},
		{"driver": "qcow2", "node-name": "d-root-0", "file": "d-root-0-f", "read-only": false, "discard": "unmap", "backing": "d-root-1"},
		// A read-only disk of one qcow2 image follows no backing its header names.
		{"driver": "file", "node-name": "d-home-0-f", "filename": "/dev/fdset/7", "aio": "io_uring", "discard": "unmap",
			"read-only": true, "drop-cache": false},
		{"driver": "qcow2", "node-name": "d-home-0", "file": "d-home-0-f", "read-only": true, "discard": "unmap", "backing": nil},
	}
	if !reflect.DeepEqual(nodes, wantNodes) {
		t.Errorf("nodes\n%v\nwant\n%v", nodes, wantNodes)
	}
	if TopNode("root") != "d-root-0" {
		t.Errorf("TopNode(root) = %s, want the node the place was reopened onto", TopNode("root"))
	}
	reopens := f.sent("blockdev-reopen")
	wantReopens := []map[string]any{
		{"options": []any{map[string]any{"driver": "throttle", "node-name": "slot0", "file": "d-root-0", "throttle-group": "places", "read-only": false, "discard": "unmap"}}},
		{"options": []any{map[string]any{"driver": "throttle", "node-name": "slot1", "file": "d-home-0", "throttle-group": "places", "read-only": true, "discard": "unmap"}}},
	}
	if !reflect.DeepEqual(reopens, wantReopens) {
		t.Errorf("reopens\n%v\nwant\n%v", reopens, wantReopens)
	}
	if got := f.sent("blockdev-del"); !reflect.DeepEqual(got, []map[string]any{{"node-name": "slot0-empty"}, {"node-name": "slot1-empty"}}) {
		t.Errorf("deleted %v, want the two placeholders", got)
	}
}

// What a boot refuses, before the guest runs.
func TestBootRefuses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
		disk   func(t *testing.T) Disk
		answer func(c call) string
		want   string
	}{
		{name: "a machine held for a checkpoint", status: "inmigrate", want: "is inmigrate"},
		{name: "a disk with no place", disk: func(t *testing.T) Disk {
			return Disk{Serial: "other", Images: []Image{image(t, "qcow2")}}
		}, want: "no place for a disk"},
		{name: "a read-only disk in a writable place", disk: func(t *testing.T) Disk {
			return Disk{Serial: "root", Readonly: true, Images: []Image{image(t, "qcow2")}}
		}, want: "read-only true, and its place is read-only false"},
		{name: "a serial QEMU would take as options", disk: func(t *testing.T) Disk {
			return Disk{Serial: "a,b", Images: []Image{image(t, "qcow2")}}
		}, want: "is not 1-20"},
		{name: "no image", disk: func(*testing.T) Disk { return Disk{Serial: "root"} }, want: "no image"},
		{name: "an image with no file", disk: func(*testing.T) Disk {
			return Disk{Serial: "root", Images: []Image{{Format: "qcow2"}}}
		}, want: "image 0 has no file"},
		{name: "a format that is not one", disk: func(t *testing.T) Disk {
			return Disk{Serial: "root", Images: []Image{image(t, "vmdk")}}
		}, want: `format "vmdk"`},
		{name: "a raw image with images under it", disk: func(t *testing.T) Disk {
			return Disk{Serial: "root", Images: []Image{image(t, "raw"), image(t, "qcow2")}}
		}, want: "image 0 is raw"},
		{name: "direct over no backing", disk: func(t *testing.T) Disk {
			return Disk{Serial: "root", DirectOverBacking: true, Images: []Image{image(t, "qcow2")}}
		}, want: "no image under the top"},
		{name: "a node QEMU refuses", disk: func(t *testing.T) Disk {
			return Disk{Serial: "root", Images: []Image{image(t, "qcow2")}}
		}, answer: func(c call) string {
			if c.Command == "blockdev-add" {
				return refused("no")
			}
			return machineAnswers("prelaunch", "root")(c)
		}, want: "adding image 0"},
		{name: "a reopen QEMU refuses", disk: func(t *testing.T) Disk {
			return Disk{Serial: "root", Images: []Image{image(t, "qcow2")}}
		}, answer: func(c call) string {
			if c.Command == "blockdev-reopen" {
				return refused("no")
			}
			return machineAnswers("prelaunch", "root")(c)
		}, want: "putting it in its place"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			answer := tc.answer
			if answer == nil {
				answer = machineAnswers(cmpOr(tc.status, "prelaunch"), "root")
			}
			f := &fakeQEMU{answer: answer}
			m := f.attach(t)
			var disks []Disk
			if tc.disk != nil {
				disks = append(disks, tc.disk(t))
			}
			err := m.Boot(ctx(t), disks...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Boot = %v, want an error saying %q", err, tc.want)
			}
			for _, c := range f.commands() {
				if c == "cont" {
					t.Error("the guest was let go")
				}
			}
		})
	}
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// migrations answers query-migrate with each status in turn, then the last one forever.
func migrations(then func(c call) string, statuses ...string) func(c call) string {
	i := 0
	return func(c call) string {
		if c.Command != "query-migrate" {
			return then(c)
		}
		st := statuses[min(i, len(statuses)-1)]
		i++
		if st == "failed" {
			return ret(`{"status": "failed", "error-desc": "No space left on device"}`)
		}
		return ret(`{"status": "` + st + `"}`)
	}
}

// cpuAnswers answers what CPU asks of a machine shown the host's CPU, or nothing for any
// other command.
func cpuAnswers(c call) (string, bool) {
	switch c.Command {
	case "query-cpus-fast":
		return ret(`[{"qom-path": "/machine/unattached/device[0]"}, {"qom-path": "/machine/peripheral/vcpu1"}]`), true
	case "query-cpu-model-expansion":
		return ret(`{"model": {"name": "host", "props": {"vendor": "GenuineIntel", "sse2": true, "avx512f": true, "vmx": false, "family": 6}}}`), true
	case "qom-get":
		if c.Args["property"] == "type" {
			return ret(`"host-x86_64-cpu"`), true
		}
	}
	return "", false
}

// A checkpoint records the vCPUs and disks added since exec, writes mapped-ram, seals at
// pre-switchover, and says its format.
func TestCheckpointSealsAtSwitchoverAndSaysItsFormat(t *testing.T) {
	sealed := 0
	base := func(c call) string {
		if r, ok := cpuAnswers(c); ok {
			return r
		}
		switch c.Command {
		case "query-hotpluggable-cpus":
			return ret(`[{"type": "host-x86_64-cpu", "qom-path": "/machine/peripheral/vcpu1", "props": {"core-id": 1}},
				{"type": "host-x86_64-cpu", "qom-path": "/machine/unattached/device[0]", "props": {"core-id": 0}}]`)
		case "qom-list":
			return ret(`[{"name": "hd3", "type": "child<scsi-hd>"}, {"name": "disk0", "type": "child<virtio-blk-pci>"}, {"name": "hdx", "type": "child<scsi-hd>"}]`)
		case "qom-get":
			return ret(`"ext"`)
		case "add-fd":
			return ret(`{"fdset-id": 3}`)
		}
		return ok
	}
	f := &fakeQEMU{answer: migrations(base, "active", "pre-switchover", "completed")}
	m := f.attach(t)
	to := image(t, "raw").File
	saved, err := m.Checkpoint(ctx(t), to, func(context.Context) error { sealed++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if sealed != 1 {
		t.Errorf("sealed %d times, want once", sealed)
	}
	if saved.Format != FormatMappedRAM {
		t.Errorf("format %q, want %q", saved.Format, FormatMappedRAM)
	}
	if want := (CPU{Vendor: "GenuineIntel", Model: "host", Features: []string{"avx512f", "sse2"}}); !reflect.DeepEqual(saved.CPU, want) {
		t.Errorf("CPU %+v, want %+v", saved.CPU, want)
	}
	if got := f.sent("query-cpu-model-expansion")[0]; !reflect.DeepEqual(got, map[string]any{"type": "full",
		"model": map[string]any{"name": "host", "props": map[string]any{"migratable": true}}}) {
		t.Errorf("expanded %v, want host as the machine runs it, migratable", got)
	}
	var sm savedMachine
	if err := json.Unmarshal(saved.Machine, &sm); err != nil {
		t.Fatal(err)
	}
	wantSM := savedMachine{
		CPUs:  []addedCPU{{ID: "vcpu1", Driver: "host-x86_64-cpu", Props: map[string]any{"core-id": float64(1)}}},
		Disks: []hotDisk{{Serial: "ext", Target: 3}},
	}
	if !reflect.DeepEqual(sm, wantSM) {
		t.Errorf("saved %+v, want %+v", sm, wantSM)
	}
	want := []string{"query-hotpluggable-cpus", "qom-list", "qom-get", "query-cpus-fast", "qom-get", "query-cpu-model-expansion",
		"migrate-set-capabilities", "migrate-set-parameters",
		"add-fd", "migrate", "remove-fd", "query-migrate", "query-migrate", "migrate-continue", "query-migrate"}
	if got := f.commands(); !reflect.DeepEqual(got, want) {
		t.Errorf("commands\n%v\nwant\n%v", got, want)
	}
	caps := f.sent("migrate-set-capabilities")[0]["capabilities"]
	wantCaps := []any{
		map[string]any{"capability": "mapped-ram", "state": true},
		map[string]any{"capability": "pause-before-switchover", "state": true},
		map[string]any{"capability": "postcopy-ram", "state": false},
	}
	if !reflect.DeepEqual(caps, wantCaps) {
		t.Errorf("capabilities %v, want %v", caps, wantCaps)
	}
	if got := f.sent("migrate-set-parameters")[0]["max-bandwidth"]; got != float64(checkpointBandwidth) {
		t.Errorf("max-bandwidth %v, want %d", got, checkpointBandwidth)
	}
	if got := f.sent("migrate")[0]["uri"]; got != "file:/dev/fdset/3" {
		t.Errorf("migrate to %v", got)
	}
	if got := f.sent("migrate-continue")[0]["state"]; got != "pre-switchover" {
		t.Errorf("continued from %v", got)
	}
}

// A checkpoint that cannot complete cancels the migration, which would otherwise hold the
// guest paused at pre-switchover with nobody to continue it.
func TestACheckpointThatFailsIsCancelled(t *testing.T) {
	for _, tc := range []struct {
		name     string
		statuses []string
		seal     error
		want     string
	}{
		{"a seal that fails", []string{"pre-switchover"}, errors.New("disk busy"), "sealing: disk busy"},
		{"a migration QEMU could not finish", []string{"active", "failed"}, nil, "failed: No space left on device"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeQEMU{answer: migrations(func(c call) string {
				if r, ok := cpuAnswers(c); ok {
					return r
				}
				switch c.Command {
				case "query-hotpluggable-cpus", "qom-list":
					return ret(`[]`)
				case "add-fd":
					return ret(`{"fdset-id": 3}`)
				}
				return ok
			}, tc.statuses...)}
			m := f.attach(t)
			_, err := m.Checkpoint(ctx(t), image(t, "raw").File, func(context.Context) error { return tc.seal })
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Checkpoint = %v, want %q", err, tc.want)
			}
			cmds := f.commands()
			if cmds[len(cmds)-1] != "migrate_cancel" {
				t.Errorf("last command %s, want migrate_cancel", cmds[len(cmds)-1])
			}
		})
	}
}

// A resume rebuilds what the checkpoint says the machine had, asks for what the format needs,
// and returns when the guest runs: with Fast, before all of it is read.
func TestResume(t *testing.T) {
	sm, _ := json.Marshal(savedMachine{
		CPUs:  []addedCPU{{ID: "vcpu1", Driver: "host-x86_64-cpu", Props: map[string]any{"core-id": 1}}},
		Disks: []hotDisk{{Serial: "ext", Target: 2}},
	})
	mapped := Saved{Format: FormatMappedRAM, Machine: sm}
	for _, tc := range []struct {
		name     string
		saved    Saved
		load     Load
		statuses []string
		after    string // the run state once loaded
		caps     map[string]bool
		polls    int
		cont     bool
	}{
		{"fast, at postcopy-active", mapped, Load{Fast: true}, []string{"setup", "postcopy-active"}, "running",
			map[string]bool{"mapped-ram": true, "postcopy-ram": true, "pause-before-switchover": false}, 2, false},
		{"in full, past postcopy-active, which it never asked for", mapped, Load{}, []string{"active", "postcopy-active", "completed"}, "running",
			map[string]bool{"mapped-ram": true, "postcopy-ram": false, "pause-before-switchover": false}, 3, false},
		{"a stream checkpoint, saved stopped", Saved{Format: FormatStream}, Load{}, []string{"completed"}, "paused",
			map[string]bool{"mapped-ram": false, "pause-before-switchover": false}, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status := "inmigrate"
			places := machineAnswers("inmigrate", "root")
			f := &fakeQEMU{}
			f.answer = migrations(func(c call) string {
				if c.Command == "query-status" {
					st := status
					if strings.Contains(strings.Join(f.commands(), " "), "migrate-incoming") {
						st = tc.after
					}
					return ret(`{"status": "` + st + `"}`)
				}
				if c.Command == "qom-list" {
					return ret(`[]`)
				}
				if r, ok := cpuAnswers(c); ok {
					return r
				}
				return places(c)
			}, tc.statuses...)
			m := f.attach(t)
			disks := []Disk{{Serial: "root", Images: []Image{image(t, "qcow2")}}}
			if len(tc.saved.Machine) > 0 {
				disks = append(disks, Disk{Serial: "ext", Readonly: true, Images: []Image{image(t, "qcow2")}})
			}
			if err := m.Resume(ctx(t), image(t, "raw").File, tc.saved, tc.load, disks...); err != nil {
				t.Fatal(err)
			}
			caps := map[string]bool{}
			for _, c := range f.sent("migrate-set-capabilities")[0]["capabilities"].([]any) {
				c := c.(map[string]any)
				caps[c["capability"].(string)] = c["state"].(bool)
			}
			if !reflect.DeepEqual(caps, tc.caps) {
				t.Errorf("capabilities %v, want %v", caps, tc.caps)
			}
			if got := len(f.sent("query-migrate")); got != tc.polls {
				t.Errorf("asked for the migration's state %d times, want %d", got, tc.polls)
			}
			if got := len(f.sent("cont")) == 1; got != tc.cont {
				t.Errorf("continued: %t, want %t", got, tc.cont)
			}
			if len(tc.saved.Machine) > 0 {
				dev := f.sent("device_add")
				if len(dev) != 2 || dev[0]["scsi-id"] != float64(2) || dev[0]["serial"] != "ext" || dev[1]["id"] != "vcpu1" || dev[1]["core-id"] != float64(1) {
					t.Errorf("device_add %v, want the disk at target 2 and then vcpu1", dev)
				}
			}
			cmds := strings.Join(f.commands(), " ")
			if strings.Index(cmds, "device_add") > strings.Index(cmds, "migrate-incoming") {
				t.Error("a device was added after the load started: the stream names each device, and one that is not there fails it")
			}
		})
	}
}

// What a resume refuses before reading a byte of the state.
func TestResumeRefuses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		saved  Saved
		load   Load
		status string
		want   string
	}{
		{"a stream checkpoint, fast", Saved{Format: FormatStream}, Load{Fast: true}, "inmigrate", "loads only in full"},
		{"a format it does not read", Saved{Format: "firecracker-snapshot"}, Load{}, "inmigrate", `format "firecracker-snapshot"`},
		{"no format", Saved{}, Load{}, "inmigrate", `format ""`},
		{"a machine held for a boot", Saved{Format: FormatMappedRAM}, Load{}, "prelaunch", "is prelaunch"},
		{"a record it cannot read", Saved{Format: FormatMappedRAM, Machine: json.RawMessage(`[`)}, Load{}, "inmigrate", "what the checkpoint says"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeQEMU{answer: machineAnswers(tc.status, "root")}
			m := f.attach(t)
			err := m.Resume(ctx(t), image(t, "raw").File, tc.saved, tc.load)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Resume = %v, want %q", err, tc.want)
			}
			for _, c := range f.commands() {
				if c == "migrate-incoming" {
					t.Error("the state was read")
				}
			}
		})
	}
}

// Loaded waits for the last page, and a load that failed after the guest ran is an error.
func TestLoaded(t *testing.T) {
	f := &fakeQEMU{answer: migrations(machineAnswers("running"), "postcopy-active", "postcopy-active", "completed")}
	if err := f.attach(t).Loaded(ctx(t)); err != nil || len(f.recorded()) != 3 {
		t.Errorf("Loaded = %v after %d polls, want nil after 3", err, len(f.recorded()))
	}
	f = &fakeQEMU{answer: migrations(machineAnswers("running"), "postcopy-active", "failed")}
	if err := f.attach(t).Loaded(ctx(t)); err == nil {
		t.Error("a failed load was reported loaded")
	}
}

// cpus answers query-hotpluggable-cpus for a machine of four slots with n plugged, QEMU's way:
// the highest first, boot vCPUs unattached and added ones under peripheral.
func cpus(boot, added int) func(c call) string {
	return func(c call) string {
		if c.Command != "query-hotpluggable-cpus" {
			return ok
		}
		var s []string
		for i := 3; i >= 0; i-- {
			path := ""
			switch {
			case i < boot:
				path = fmt.Sprintf(`"qom-path": "/machine/unattached/device[%d]",`, i)
			case i < boot+added:
				path = fmt.Sprintf(`"qom-path": "/machine/peripheral/vcpu%d",`, i)
			}
			s = append(s, fmt.Sprintf(`{"type": "host-x86_64-cpu", %s "props": {"core-id": %d, "socket-id": 0}}`, path, i))
		}
		return ret("[" + strings.Join(s, ",") + "]")
	}
}

func TestSetCPUs(t *testing.T) {
	for _, tc := range []struct {
		name        string
		boot, added int
		n           int
		adds, dels  []string
		wantErr     string
	}{
		{name: "up from the boot vCPUs, into the first free slots", boot: 1, n: 3, adds: []string{"vcpu1", "vcpu2"}},
		{name: "down, the last added first", boot: 1, added: 2, n: 2, dels: []string{"vcpu2"}},
		{name: "already there", boot: 2, n: 2},
		{name: "below the boot vCPUs", boot: 2, n: 1, wantErr: "the rest are the ones the machine was started with"},
		{name: "above the ceiling", boot: 1, n: 5, wantErr: "slots for 1 to 4"},
		{name: "none", boot: 1, n: 0, wantErr: "slots for 1 to 4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeQEMU{}
			f.answer = func(c call) string {
				if c.Command == "query-status" {
					// The guest let it go: DEVICE_DELETED arrives before the reply.
					d := f.sent("device_del")
					return `{"event": "DEVICE_DELETED", "data": {"device": "` + d[len(d)-1]["id"].(string) + `"}}` + "\n" + ret(`{"status": "running"}`)
				}
				return cpus(tc.boot, tc.added)(c)
			}
			err := f.attach(t).SetCPUs(ctx(t), tc.n)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("SetCPUs(%d) = %v, want %q", tc.n, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var adds, dels []string
			for _, a := range f.sent("device_add") {
				adds = append(adds, a["id"].(string))
				if a["driver"] != "host-x86_64-cpu" || a["socket-id"] != float64(0) {
					t.Errorf("added %v, want the slot's type and topology", a)
				}
			}
			for _, d := range f.sent("device_del") {
				dels = append(dels, d["id"].(string))
			}
			if !reflect.DeepEqual(adds, tc.adds) || !reflect.DeepEqual(dels, tc.dels) {
				t.Errorf("added %v and removed %v, want %v and %v", adds, dels, tc.adds, tc.dels)
			}
		})
	}
}

func memoryAnswers(c call) string {
	switch c.Command {
	case "query-memory-size-summary":
		return ret(`{"base-memory": 536870912, "plugged-memory": 268435456}`)
	case "qom-get":
		return ret(`1073741824`)
	}
	return ok
}

// Memory is asked for in all, and virtio-mem given what is above the boot memory.
func TestSetMemory(t *testing.T) {
	f := &fakeQEMU{answer: memoryAnswers}
	m := f.attach(t)
	if err := m.SetMemory(ctx(t), 1024); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"path": "/machine/peripheral/vmem0", "property": "requested-size", "value": float64(512 << 20)}
	if got := f.sent("qom-set"); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Errorf("qom-set %v, want %v", got, want)
	}
	if got := f.sent("qom-get")[0]; got["path"] != "/objects/mem.growth" || got["property"] != "size" {
		t.Errorf("read the ceiling from %v, want the growth backend's size", got)
	}
	for _, mb := range []int{511, 1537} {
		if err := m.SetMemory(ctx(t), mb); err == nil || !strings.Contains(err.Error(), "512 MiB to 1536 MiB") {
			t.Errorf("SetMemory(%d) = %v, want a refusal naming 512 to 1536 MiB", mb, err)
		}
	}
	if err := m.SetMemory(ctx(t), 512); err != nil {
		t.Errorf("SetMemory to the boot memory = %v", err)
	}
	if err := m.SetMemory(ctx(t), 1536); err != nil {
		t.Errorf("SetMemory to the ceiling = %v", err)
	}
	if mb, err := m.Memory(ctx(t)); err != nil || mb != 768 {
		t.Errorf("Memory = %d, %v, want 768", mb, err)
	}
}

// A detach waits for QEMU to report the device gone, then lets its nodes go top first and its
// descriptors after.
func TestDetachDisk(t *testing.T) {
	f := &fakeQEMU{}
	f.answer = func(c call) string {
		switch c.Command {
		case "qom-list":
			return ret(`[{"name": "hd4", "type": "child<scsi-hd>"}, {"name": "hd0", "type": "child<scsi-hd>"}]`)
		case "qom-get":
			if strings.HasSuffix(c.Args["path"].(string), "hd4") {
				return ret(`"ext"`)
			}
			return ret(`"other"`)
		case "query-status":
			return `{"event": "DEVICE_DELETED", "data": {"device": "hd4"}}` + "\n" + ret(`{"status": "running"}`)
		case "query-named-block-nodes":
			// The sets are named by the image's path, which says nothing of the disk: they are
			// found through the disk's file nodes.
			return ret(`[{"node-name": "d-ext-0", "file": "json:{}"}, {"node-name": "d-ext-0-f", "file": "/dev/fdset/1"},
				{"node-name": "d-ext-1", "file": "json:{}"}, {"node-name": "d-ext-1-f", "file": "/dev/fdset/4"},
				{"node-name": "d-other-0-f", "file": "/dev/fdset/2"}]`)
		}
		return ok
	}
	m := f.attach(t)
	if err := m.DetachDisk(ctx(t), "ext"); err != nil {
		t.Fatal(err)
	}
	if got := f.sent("device_del"); !reflect.DeepEqual(got, []map[string]any{{"id": "hd4"}}) {
		t.Errorf("device_del %v, want hd4", got)
	}
	var dels []string
	for _, d := range f.sent("blockdev-del") {
		dels = append(dels, d["node-name"].(string))
	}
	if want := []string{"d-ext-0", "d-ext-0-f", "d-ext-1", "d-ext-1-f"}; !reflect.DeepEqual(dels, want) {
		t.Errorf("deleted %v, want %v", dels, want)
	}
	var sets []float64
	for _, r := range f.sent("remove-fd") {
		sets = append(sets, r["fdset-id"].(float64))
	}
	if !reflect.DeepEqual(sets, []float64{1, 4}) {
		t.Errorf("removed descriptor sets %v, want 1 and 4", sets)
	}
	if err := m.DetachDisk(ctx(t), "nothing"); err == nil {
		t.Error("detaching a disk the machine does not have succeeded")
	}
}

// An attach takes the first target free.
func TestAttachDiskTakesTheFirstFreeTarget(t *testing.T) {
	f := &fakeQEMU{}
	f.answer = func(c call) string {
		switch c.Command {
		case "qom-list":
			return ret(`[{"name": "hd0", "type": "child<scsi-hd>"}, {"name": "hd2", "type": "child<scsi-hd>"}]`)
		case "qom-get":
			return ret(`"x"`)
		case "add-fd":
			return ret(`{"fdset-id": 1}`)
		}
		return ok
	}
	m := f.attach(t)
	if err := m.AttachDisk(ctx(t), Disk{Serial: "ext", Readonly: true, Images: []Image{image(t, "qcow2")}}); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"driver": "scsi-hd", "id": "hd1", "drive": "d-ext-0", "bus": "scsi0.0", "scsi-id": float64(1), "lun": float64(0), "serial": "ext"}
	if got := f.sent("device_add"); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Errorf("device_add %v, want %v", got, want)
	}
	if err := m.attachAt(ctx(t), MaxHotplugDisks, Disk{Serial: "y", Images: []Image{image(t, "qcow2")}}); err == nil {
		t.Error("a target past the controller's was taken")
	}
	if err := m.AttachDisk(ctx(t), Disk{Serial: "bad serial"}); err == nil {
		t.Error("a disk validate refuses was attached")
	}
}

// Quit's reply may never come: QEMU closes the monitor first.
func TestQuitThatClosesTheMonitorHasQuit(t *testing.T) {
	f := &fakeQEMU{answer: func(c call) string { return "" }}
	m := f.attach(t)
	// The fake answers an empty line; a real QEMU closes the socket, which the next read sees.
	_ = m.q.c.CloseRead()
	if err := m.Quit(ctx(t)); err != nil {
		t.Errorf("Quit = %v", err)
	}
}

func TestContinue(t *testing.T) {
	f := &fakeQEMU{answer: func(c call) string { return refused("no") }}
	if err := f.attach(t).Continue(ctx(t)); err == nil || !strings.Contains(err.Error(), "continuing the machine: cont: GenericError: no") {
		t.Errorf("Continue = %v, want QEMU's refusal", err)
	}
}

func TestCanShowCPU(t *testing.T) {
	host := func(vendor string, features ...string) CPU {
		return CPU{Vendor: vendor, Model: "host", Features: features}
	}
	for _, tc := range []struct {
		name        string
		saved, here CPU
		want        string // empty when it may
	}{
		{"the same CPU", host("GenuineIntel", "avx2", "sse2"), host("GenuineIntel", "avx2", "sse2"), ""},
		{"more features here", host("GenuineIntel", "sse2"), host("GenuineIntel", "avx2", "avx512f", "sse2"), ""},
		{"a feature the guest was shown is missing", host("GenuineIntel", "avx512f", "sse2"), host("GenuineIntel", "avx2", "sse2"), "cannot show: avx512f"},
		{"another vendor", host("GenuineIntel"), host("AuthenticAMD"), "GenuineIntel CPU, and this machine shows a AuthenticAMD"},
		// enforce=on: a host that could not show all of a named model would not have started it.
		{"a named model by its name", CPU{Vendor: "GenuineIntel", Model: "Skylake-Server-v4", Features: []string{"avx512f"}},
			CPU{Vendor: "GenuineIntel", Model: "Skylake-Server-v4"}, ""},
		{"another named model", CPU{Vendor: "GenuineIntel", Model: "Skylake-Server-v4"}, CPU{Vendor: "GenuineIntel", Model: "Icelake-Server-v6"}, "model Skylake-Server-v4"},
		{"a named model into host", CPU{Vendor: "GenuineIntel", Model: "Skylake-Server-v4"}, host("GenuineIntel"), "model Skylake-Server-v4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := canShowCPU(tc.saved, tc.here)
			if (tc.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.want)) {
				t.Errorf("canShowCPU = %v, want %q", err, tc.want)
			}
		})
	}
}

// CanResume is the one rule a placement and a resume ask: format, machine, VMM version, CPU.
func TestCanResume(t *testing.T) {
	cpu := CPU{Vendor: "GenuineIntel", Model: "host", Features: []string{"avx2", "sse2"}}
	here := Target{Formats: []string{FormatMappedRAM, FormatStream}, Fingerprint: "abc", VMM: "qemu 11.2.0", CPU: cpu}
	saved := Saved{Format: FormatMappedRAM, Fingerprint: "abc", VMM: "qemu 11.2.0", CPU: cpu}
	for _, tc := range []struct {
		name string
		edit func(*Saved)
		want string // empty when it may
	}{
		{"the same everything", func(*Saved) {}, ""},
		{"written by an older QEMU", func(s *Saved) { s.VMM = "qemu 11.1.3" }, ""},
		{"an older micro, a newer major", func(s *Saved) { s.VMM = "qemu 10.9.9" }, ""},
		{"written by a newer QEMU", func(s *Saved) { s.VMM = "qemu 11.2.1" }, "newer than this machine's qemu 11.2.0"},
		{"written by another program", func(s *Saved) { s.VMM = "cloud-hypervisor 48.0" }, "written by cloud-hypervisor"},
		{"a VMM that is not a version", func(s *Saved) { s.VMM = "qemu" }, "not a program and a version"},
		{"a version with no program", func(s *Saved) { s.VMM = " 11.2.0" }, "not a program and a version"},
		{"another machine", func(s *Saved) { s.Fingerprint = "def" }, "machine def, and this is machine abc"},
		{"a format this machine does not read", func(s *Saved) { s.Format = "firecracker" }, `format "firecracker"`},
		{"a CPU feature this machine cannot show", func(s *Saved) { s.CPU.Features = []string{"amx-tile"} }, "cannot show: amx-tile"},
		{"a record from before any of it was kept", func(s *Saved) { *s = Saved{Format: FormatStream} }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := saved
			s.CPU.Features = slices.Clone(cpu.Features)
			tc.edit(&s)
			err := CanResume(s, here)
			if (tc.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.want)) {
				t.Errorf("CanResume = %v, want %q", err, tc.want)
			}
		})
	}
}

// A resume refuses a checkpoint whose guest was shown a CPU this machine cannot show, before
// reading a byte of it; one recorded before the CPU was is not checked.
func TestResumeChecksTheCPU(t *testing.T) {
	for _, tc := range []struct {
		name string
		cpu  CPU
		want string
	}{
		{"a feature this host lacks", CPU{Vendor: "GenuineIntel", Model: "host", Features: []string{"amx-tile"}}, "cannot show: amx-tile"},
		{"no record", CPU{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeQEMU{answer: migrations(func(c call) string {
				if r, ok := cpuAnswers(c); ok {
					return r
				}
				return machineAnswers("inmigrate")(c)
			}, "completed")}
			err := f.attach(t).Resume(ctx(t), image(t, "raw").File, Saved{Format: FormatMappedRAM, CPU: tc.cpu}, Load{})
			if (tc.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("Resume = %v, want %q", err, tc.want)
			}
			read := false
			for _, c := range f.commands() {
				read = read || c == "migrate-incoming"
			}
			if read != (tc.want == "") {
				t.Errorf("the state was read: %t", read)
			}
		})
	}
}

// What CPU makes of a machine shown a named model, and of answers that are not one.
func TestCPU(t *testing.T) {
	named := func(c call) string {
		switch c.Command {
		case "query-cpus-fast":
			return ret(`[{"qom-path": "/machine/unattached/device[0]"}]`)
		case "qom-get":
			return ret(`"Skylake-Server-v4-x86_64-cpu"`)
		case "query-cpu-model-expansion":
			return ret(`{"model": {"name": "Skylake-Server-v4", "props": {"vendor": "GenuineIntel", "avx512f": true}}}`)
		}
		return ok
	}
	f := &fakeQEMU{answer: named}
	got, err := f.attach(t).CPU(ctx(t))
	if want := (CPU{Vendor: "GenuineIntel", Model: "Skylake-Server-v4", Features: []string{"avx512f"}}); err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("CPU = %+v, %v, want %+v", got, err, want)
	}
	// A named model is expanded as it is: migratable is host's and max's.
	if got := f.sent("query-cpu-model-expansion")[0]["model"]; !reflect.DeepEqual(got, map[string]any{"name": "Skylake-Server-v4"}) {
		t.Errorf("expanded %v", got)
	}
	for _, tc := range []struct{ name, cpus, typ, want string }{
		{"no vCPU", `[]`, `"x"`, "no vCPU"},
		{"not an x86_64 CPU", `[{"qom-path": "/p"}]`, `"cortex-a72-arm-cpu"`, "not an x86_64 CPU model"},
	} {
		f := &fakeQEMU{answer: func(c call) string {
			if c.Command == "query-cpus-fast" {
				return ret(tc.cpus)
			}
			return ret(tc.typ)
		}}
		if _, err := f.attach(t).CPU(ctx(t)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: CPU = %v, want %q", tc.name, err, tc.want)
		}
	}
}

// The models a host runs: the named ones QEMU can run here in full, safe to migrate, under their
// own name and not deprecated, each with its static expansion's feature count.
func TestModels(t *testing.T) {
	f := &fakeQEMU{answer: func(c call) string {
		switch c.Command {
		case "query-cpu-definitions":
			return ret(`[
				{"name": "Skylake-Server-v4", "unavailable-features": [], "migration-safe": true},
				{"name": "Icelake-Server-v6", "unavailable-features": [], "migration-safe": true},
				{"name": "SapphireRapids-v3", "unavailable-features": ["amx-tile"], "migration-safe": true},
				{"name": "Skylake-Server", "unavailable-features": [], "migration-safe": true, "alias-of": "Skylake-Server-v1"},
				{"name": "Penryn-v1", "unavailable-features": [], "migration-safe": false},
				{"name": "Cascadelake-Server-v1", "unavailable-features": [], "migration-safe": true, "deprecated": true},
				{"name": "host", "unavailable-features": [], "migration-safe": true},
				{"name": "max", "unavailable-features": [], "migration-safe": true},
				{"name": "base", "unavailable-features": [], "migration-safe": true}]`)
		case "query-cpu-model-expansion":
			if c.Args["model"].(map[string]any)["name"] == "Icelake-Server-v6" {
				return ret(`{"model": {"name": "base", "props": {"a": true, "b": true, "c": true, "d": false}}}`)
			}
			return ret(`{"model": {"name": "base", "props": {"a": true, "family": 6}}}`)
		}
		return ok
	}}
	got, err := f.attach(t).Models(ctx(t))
	if want := []Model{{"Icelake-Server-v6", 3}, {"Skylake-Server-v4", 1}}; err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("Models = %v, %v, want %v", got, err, want)
	}
	for _, e := range f.sent("query-cpu-model-expansion") {
		if e["type"] != "static" {
			t.Errorf("expanded %v, want a static expansion", e)
		}
	}
}

func TestBaseline(t *testing.T) {
	sky, ice, spr := Model{"Skylake-Server-v4", 100}, Model{"Icelake-Server-v6", 120}, Model{"SapphireRapids-v3", 140}
	epyc := Model{"EPYC-Genoa-v1", 130}
	for _, tc := range []struct {
		name  string
		hosts [][]Model
		want  Model
		err   string
	}{
		{"the newest every host runs", [][]Model{{sky, ice, spr}, {sky, ice}, {ice, sky}}, ice, ""},
		{"one host", [][]Model{{sky, spr}}, spr, ""},
		{"as many features, the later name", [][]Model{{{"A-v1", 5}, {"A-v2", 5}}}, Model{"A-v2", 5}, ""},
		{"fewer features, a later name", [][]Model{{ice, sky}}, ice, ""},
		{"two vendors share no model", [][]Model{{sky, ice}, {epyc}}, Model{}, "more than one vendor"},
		{"no hosts", nil, Model{}, "no hosts"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Baseline(tc.hosts)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("Baseline = %v, %v, want %q", got, err, tc.err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Errorf("Baseline = %v, %v, want %v", got, err, tc.want)
			}
		})
	}
}

// An attach to a machine with no disk attached takes target 0; a target outside the
// controller's is refused, named.
func TestAttachDiskTargets(t *testing.T) {
	f := &fakeQEMU{answer: func(c call) string {
		switch c.Command {
		case "qom-list":
			return ret(`[]`)
		case "add-fd":
			return ret(`{"fdset-id": 1}`)
		}
		return ok
	}}
	m := f.attach(t)
	if err := m.AttachDisk(ctx(t), Disk{Serial: "ext", Images: []Image{image(t, "qcow2")}}); err != nil {
		t.Fatal(err)
	}
	if got := f.sent("device_add")[0]["scsi-id"]; got != float64(0) {
		t.Errorf("target %v, want 0", got)
	}
	err := m.attachAt(ctx(t), -1, Disk{Serial: "y", Images: []Image{image(t, "qcow2")}})
	if err == nil || !strings.Contains(err.Error(), "target -1, and the controller addresses 0 to 255") {
		t.Errorf("attachAt(-1) = %v", err)
	}
}

// A resume whose disk added while the machine ran is refused at its target fails there.
func TestResumeFailsWhereAHotDiskIsRefused(t *testing.T) {
	sm, _ := json.Marshal(savedMachine{Disks: []hotDisk{{Serial: "ext", Target: 2}}})
	f := &fakeQEMU{answer: func(c call) string {
		if c.Command == "device_add" {
			return refused("no")
		}
		if r, ok := cpuAnswers(c); ok {
			return r
		}
		return machineAnswers("inmigrate", "root")(c)
	}}
	err := f.attach(t).Resume(ctx(t), image(t, "raw").File, Saved{Format: FormatMappedRAM, Machine: sm}, Load{},
		Disk{Serial: "ext", Images: []Image{image(t, "qcow2")}})
	if err == nil || !strings.Contains(err.Error(), "attaching it at target 2") {
		t.Errorf("Resume = %v, want the refused disk's error", err)
	}
}

// QEMU's refusal to let go of the state's descriptor set is the migration's error when the
// migration itself was accepted, and not when it was refused: the first failure is what is said.
func TestMigrateSaysTheFirstFailure(t *testing.T) {
	for _, tc := range []struct {
		name             string
		migrate, removed string
		want             string
	}{
		{"the set kept", ok, refused("busy"), "remove-fd: GenericError: busy"},
		{"the migration refused", refused("no"), refused("busy"), "migrate: GenericError: no"},
		{"both accepted", ok, ok, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeQEMU{answer: func(c call) string {
				switch c.Command {
				case "add-fd":
					return ret(`{"fdset-id": 1}`)
				case "migrate":
					return tc.migrate
				case "remove-fd":
					return tc.removed
				}
				return ok
			}}
			err := f.attach(t).migrate(ctx(t), "migrate", image(t, "raw").File, os.O_WRONLY, "state")
			if (tc.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.want)) {
				t.Errorf("migrate = %v, want %q", err, tc.want)
			}
		})
	}
}

// A monitor whose first line is not QEMU's greeting is not attached to.
func TestAttachRefusesAMonitorThatDoesNotGreet(t *testing.T) {
	for _, first := range []string{`{"return": {}}`, `not json`} {
		dir, err := os.MkdirTemp("/tmp", "vmt")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		l, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "q"), Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			c, err := l.Accept()
			if err == nil {
				_, _ = c.Write([]byte(first + "\n"))
				time.Sleep(100 * time.Millisecond)
				_ = c.Close()
			}
		}()
		conn, err := net.DialUnix("unix", nil, l.Addr().(*net.UnixAddr))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Attach(conn); err == nil || !strings.Contains(err.Error(), "not QEMU's greeting") {
			t.Errorf("Attach after %q = %v", first, err)
		}
		_ = l.Close()
	}
}

// A DEVICE_DELETED waited on is used up: a second wait for the same id waits for another.
func TestADeletionIsWaitedOnOnce(t *testing.T) {
	f := &fakeQEMU{answer: func(c call) string { return ret(`{"status": "running"}`) }}
	m := f.attach(t)
	m.q.deleted = []string{"a", "b"}
	if err := m.q.waitDeleted(ctx(t), "a"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m.q.deleted, []string{"b"}) {
		t.Errorf("deletions left %v, want only b", m.q.deleted)
	}
	short, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := m.q.waitDeleted(short, "a"); err == nil {
		t.Error("a deletion was waited on twice")
	}
}

// A checkpoint's vCPU is added again only as a vCPU in a slot: its record is what the saved
// machine's QEMU answered, and device_add attaches whatever it is given.
func TestACheckpointsVCPUIsOnlyAVCPU(t *testing.T) {
	ok := addedCPU{ID: "vcpu1", Driver: "host" + cpuSuffix, Props: map[string]any{"socket-id": 0.0, "core-id": 1.0, "thread-id": 0.0}}
	if args, err := ok.deviceArgs(); err != nil || args["driver"] != "host"+cpuSuffix || args["id"] != "vcpu1" || args["core-id"] != 1.0 {
		t.Errorf("deviceArgs(%+v) = %v, %v", ok, args, err)
	}
	for _, tc := range []struct {
		name string
		cpu  addedCPU
	}{
		{"another device", addedCPU{ID: "x", Driver: "virtio-net-pci"}},
		{"no id", addedCPU{Driver: "host" + cpuSuffix}},
		{"a property no slot has", addedCPU{ID: "x", Driver: "host" + cpuSuffix, Props: map[string]any{"romfile": 0.0}}},
		{"a coordinate that is not a number", addedCPU{ID: "x", Driver: "host" + cpuSuffix, Props: map[string]any{"core-id": "1"}}},
		{"a property that names the driver", addedCPU{ID: "x", Driver: "host" + cpuSuffix, Props: map[string]any{"driver": "virtio-net-pci"}}},
		{"a property that names the id", addedCPU{ID: "x", Driver: "host" + cpuSuffix, Props: map[string]any{"id": "y"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if args, err := tc.cpu.deviceArgs(); err == nil {
				t.Errorf("deviceArgs(%+v) = %v, want refused", tc.cpu, args)
			}
		})
	}
}

// A disk QEMU refuses to attach leaves nothing behind: its nodes go, and the set its image was
// handed over in, so the image is not held open and the serial can be attached again.
func TestAnAttachQEMURefusesIsUndone(t *testing.T) {
	f := &fakeQEMU{}
	f.answer = func(c call) string {
		switch c.Command {
		case "qom-list":
			return ret(`[]`)
		case "add-fd":
			return ret(`{"fdset-id": 9, "fd": 20}`)
		case "device_add":
			return refused("no room")
		case "query-named-block-nodes":
			// The file node first: QEMU lists nodes in no order a release promises.
			return ret(`[{"node-name": "d-ext-0-f", "file": "/dev/fdset/9"}, {"node-name": "d-ext-0", "file": "json:{}"}]`)
		}
		return ok
	}
	err := f.attach(t).AttachDisk(ctx(t), Disk{Serial: "ext", Readonly: true, Images: []Image{image(t, "raw")}})
	if err == nil || !strings.Contains(err.Error(), "no room") {
		t.Fatalf("AttachDisk = %v, want QEMU's refusal", err)
	}
	var dels []string
	for _, d := range f.sent("blockdev-del") {
		dels = append(dels, d["node-name"].(string))
	}
	if want := []string{"d-ext-0", "d-ext-0-f"}; !reflect.DeepEqual(dels, want) {
		t.Errorf("deleted %v, want %v", dels, want)
	}
	if got := f.sent("remove-fd"); !reflect.DeepEqual(got, []map[string]any{{"fdset-id": 9.0}}) {
		t.Errorf("removed %v, want set 9", got)
	}
}
