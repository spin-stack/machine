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

	"github.com/spin-stack/machine/qmp"
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
	// wmu guards w, which serve answers on and send sends events on.
	wmu sync.Mutex
	w   *bufio.Writer
}

// send writes lines to the client, as QEMU sends an event of its own accord.
func (f *fakeQEMU) send(lines string) {
	f.wmu.Lock()
	defer f.wmu.Unlock()
	_, _ = f.w.WriteString(lines)
	_ = f.w.Flush()
}

// recorded is a copy of the calls so far.
func (f *fakeQEMU) recorded() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

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
	m, err := connect(conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close(); <-done })
	return m
}

// connect is the machine on a monitor's connection, through a client past its handshake.
func connect(conn *net.UnixConn) (*QEMU, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := qmp.New(ctx, qmp.Unix(conn), qmp.Observer{})
	if err != nil {
		return nil, err
	}
	return Attach(c), nil
}

// withID is reply answering the command id: its last line is the answer, and any before it are
// events QEMU sent first.
func withID(reply string, id json.RawMessage) string {
	end := strings.LastIndexByte(reply, '}')
	return reply[:end] + `, "id": ` + string(id) + reply[end:]
}

// serve answers each command with what f.answer says, or, for an answer of nothing, closes the
// monitor without one, as QEMU does when it quits.
func (f *fakeQEMU) serve(c *net.UnixConn) {
	f.wmu.Lock()
	f.w = bufio.NewWriter(c)
	f.wmu.Unlock()
	f.send(`{"QMP": {"version": {"qemu": {"major": 11, "minor": 2, "micro": 0}, "package": ""}, "capabilities": []}}` + "\n")
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
				Execute   string          `json:"execute"`
				Arguments map[string]any  `json:"arguments"`
				ID        json.RawMessage `json:"id"`
			}
			_ = json.Unmarshal(pending[:i], &req)
			pending = pending[i+1:]
			cl := call{Command: req.Execute, Args: req.Arguments, FD: fd}
			fd = false
			reply := ok
			if cl.Command != "qmp_capabilities" {
				f.mu.Lock()
				f.calls = append(f.calls, cl)
				f.mu.Unlock()
				reply = f.answer(cl)
			}
			if reply == "" {
				return
			}
			f.send(withID(reply, req.ID) + "\n")
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
	root := Disk{Serial: "root", Locking: true, Images: []Image{image(t, "qcow2"), image(t, "raw")}}
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
			"read-only": true, "drop-cache": false},
		{"driver": "raw", "node-name": "d-root-1", "file": "d-root-1-f", "read-only": true, "discard": "unmap"},
		// The top: written, locked, and backed by the base.
		{"driver": "file", "node-name": "d-root-0-f", "filename": "/dev/fdset/7", "aio": "io_uring", "discard": "unmap",
			"read-only": false, "drop-cache": false, "locking": "on"},
		{"driver": "qcow2", "node-name": "d-root-0", "file": "d-root-0-f", "read-only": false, "discard": "unmap", "backing": "d-root-1"},
		// A read-only disk of one qcow2 image follows no backing its header names.
		{"driver": "file", "node-name": "d-home-0-f", "filename": "/dev/fdset/7", "aio": "io_uring", "discard": "unmap",
			"read-only": true, "drop-cache": false},
		{"driver": "qcow2", "node-name": "d-home-0", "file": "d-home-0-f", "read-only": true, "discard": "unmap", "backing": nil},
	}
	if !reflect.DeepEqual(nodes, wantNodes) {
		t.Errorf("nodes\n%v\nwant\n%v", nodes, wantNodes)
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

// migrationEvents is QEMU's MIGRATION for each of statuses, one line each.
func migrationEvents(statuses ...string) string {
	var b strings.Builder
	for _, st := range statuses {
		b.WriteString(`{"event": "MIGRATION", "data": {"status": "` + st + `"}, "timestamp": {"seconds": 1, "microseconds": 2}}` + "\n")
	}
	return b.String()
}

// migrations sends a MIGRATION for each status in turn, as QEMU does with the events capability
// on: those up to pre-switchover before the answer to migrate or migrate-incoming, the rest before
// the answer to migrate-continue. query-migrate answers the last status sent, and a failed one
// with its reason.
func migrations(then func(c call) string, statuses ...string) func(c call) string {
	sent := 0
	upTo := func(stop string) string {
		from := sent
		for sent < len(statuses) {
			sent++
			if statuses[sent-1] == stop {
				break
			}
		}
		return migrationEvents(statuses[from:sent]...)
	}
	return func(c call) string {
		switch c.Command {
		case "migrate", "migrate-incoming":
			return upTo("pre-switchover") + then(c)
		case "migrate-continue":
			return upTo("") + then(c)
		case "query-migrate":
			if sent > 0 && statuses[sent-1] == "failed" {
				return ret(`{"status": "failed", "error-desc": "No space left on device"}`)
			}
			if sent > 0 && statuses[sent-1] == "completed" {
				return ret(`{"status": "completed", "downtime": 12, "total-time": 345, "ram": {"transferred": 104857600}}`)
			}
			if sent > 0 {
				return ret(`{"status": "` + statuses[sent-1] + `"}`)
			}
			return ret(`{}`)
		}
		return then(c)
	}
}

// A checkpoint records the vCPUs and disks added since exec before the migration and the machine
// it was taken on while it runs, writes mapped-ram, seals at pre-switchover, and says its format.
func TestCheckpointSealsAtSwitchoverAndSaysItsFormat(t *testing.T) {
	sealed := 0
	base := func(c call) string {
		if r, ok := recordAnswers(c); ok {
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
	saved, cost, err := m.Checkpoint(ctx(t), to, SaveOptions{}, func(context.Context) error { sealed++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if want := (Cost{Paused: 12 * time.Millisecond, Total: 345 * time.Millisecond, RAM: 100 << 20}); cost != want {
		t.Errorf("cost %+v, want %+v: what the completed migration reports", cost, want)
	}
	if sealed != 1 {
		t.Errorf("sealed %d times, want once", sealed)
	}
	if saved.Format != FormatMappedRAM {
		t.Errorf("format %q, want %q", saved.Format, FormatMappedRAM)
	}
	if want := fakeTarget(t); !reflect.DeepEqual(saved.From, want) {
		t.Errorf("taken on %+v, want %+v", saved.From, want)
	}
	var again added
	if err := json.Unmarshal(saved.Added, &again); err != nil {
		t.Fatal(err)
	}
	wantAdded := added{
		CPUs:  []addedCPU{{ID: "vcpu1", Driver: "host-x86_64-cpu", Props: map[string]any{"core-id": float64(1)}}},
		Disks: []hotDisk{{Serial: "ext", Target: 3}},
	}
	if !reflect.DeepEqual(again, wantAdded) {
		t.Errorf("added %+v, want %+v", again, wantAdded)
	}
	want := []string{"query-hotpluggable-cpus", "qom-list", "qom-get",
		"migrate-set-capabilities", "migrate-set-parameters", "add-fd", "migrate",
		"query-cpus-fast", "qom-list-get", "query-cpu-model-expansion", "query-accelerators", "qom-list-get", "qom-list-get", "x-query-ramblock",
		"migrate-continue", "remove-fd", "query-migrate", "query-status"}
	if got := f.commands(); !reflect.DeepEqual(got, want) {
		t.Errorf("commands\n%v\nwant\n%v", got, want)
	}
	caps := f.sent("migrate-set-capabilities")[0]["capabilities"]
	wantCaps := []any{
		map[string]any{"capability": "events", "state": true},
		map[string]any{"capability": "mapped-ram", "state": true},
		map[string]any{"capability": "multifd", "state": DefaultChannels > 1},
		map[string]any{"capability": "pause-before-switchover", "state": true},
		map[string]any{"capability": "postcopy-ram", "state": false},
	}
	if !reflect.DeepEqual(caps, wantCaps) {
		t.Errorf("capabilities %v, want %v", caps, wantCaps)
	}
	if got := f.sent("migrate-set-parameters")[0]["max-bandwidth"]; got != float64(checkpointBandwidth) {
		t.Errorf("max-bandwidth %v, want %d", got, checkpointBandwidth)
	}
	if got := f.sent("migrate-set-parameters")[0]["downtime-limit"]; got != float64(checkpointDowntime) {
		t.Errorf("downtime-limit %v, want %d", got, checkpointDowntime)
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
		{"a migration that fails after the seal", []string{"pre-switchover", "failed"}, nil, "failed: No space left on device"},
		{"a migration cancelled under it", []string{"active", "cancelled"}, nil, "the migration cancelled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeQEMU{answer: migrations(func(c call) string {
				if r, ok := recordAnswers(c); ok {
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
			_, _, err := m.Checkpoint(ctx(t), image(t, "raw").File, SaveOptions{}, func(context.Context) error { return tc.seal })
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

// A checkpoint returns with the machine postmigrate, so that Hold and Continue find it so, though
// QEMU says completed before it leaves finish-migrate and refuses cont until it has (#421).
func TestACheckpointReturnsOnceQEMUHasLeftFinishMigrate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		after func(m *QEMU) []func(context.Context) error
	}{
		{"continued", func(m *QEMU) []func(context.Context) error { return []func(context.Context) error{m.Continue} }},
		{"held, then continued", func(m *QEMU) []func(context.Context) error {
			return []func(context.Context) error{m.Hold, m.Continue}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var completedAt time.Time
			finishing := func() bool { return completedAt.IsZero() || time.Since(completedAt) < 50*time.Millisecond }
			f := &fakeQEMU{answer: migrations(func(c call) string {
				if r, ok := recordAnswers(c); ok {
					return r
				}
				switch c.Command {
				case "query-hotpluggable-cpus", "qom-list":
					return ret(`[]`)
				case "add-fd":
					return ret(`{"fdset-id": 3}`)
				case "migrate-continue":
					completedAt = time.Now()
				case "query-status":
					if finishing() {
						return ret(`{"status": "finish-migrate"}`)
					}
					return ret(`{"status": "postmigrate"}`)
				case "cont":
					if finishing() {
						return refused("Migration is not finalized yet")
					}
				}
				return ok
			}, "active", "pre-switchover", "completed")}
			m := f.attach(t)
			if _, _, err := m.Checkpoint(ctx(t), image(t, "raw").File, SaveOptions{}, func(context.Context) error { return nil }); err != nil {
				t.Fatal(err)
			}
			if st, err := m.Status(ctx(t)); err != nil || st != Postmigrate {
				t.Fatalf("Status after Checkpoint = %q, %v, want %q", st, err, Postmigrate)
			}
			for _, do := range tc.after(m) {
				if err := do(ctx(t)); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

// A checkpoint whose QEMU never leaves finish-migrate waits as long as its context, and says what
// it waited for.
func TestACheckpointWaitsForFinishMigrateNoLongerThanItsContext(t *testing.T) {
	f := &fakeQEMU{answer: migrations(func(c call) string {
		if r, ok := recordAnswers(c); ok {
			return r
		}
		switch c.Command {
		case "query-hotpluggable-cpus", "qom-list":
			return ret(`[]`)
		case "add-fd":
			return ret(`{"fdset-id": 3}`)
		case "query-status":
			return ret(`{"status": "finish-migrate"}`)
		}
		return ok
	}, "completed")}
	c, cancel := context.WithTimeout(ctx(t), 100*time.Millisecond)
	defer cancel()
	_, _, err := f.attach(t).Checkpoint(c, image(t, "raw").File, SaveOptions{}, func(context.Context) error { return nil })
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "leave finish-migrate") {
		t.Fatalf("Checkpoint = %v, want the wait for finish-migrate to end with its context", err)
	}
	if asked := len(f.sent("query-status")); asked < 2 {
		t.Errorf("asked the run state %d times, want it asked again until the context ended", asked)
	}
}

// A checkpoint written by more than one channel asks QEMU for multifd and that many channels; one
// written by one leaves the migration's own thread to write it, and one naming none is written by
// DefaultChannels.
func TestACheckpointByChannelsAsksForMultifd(t *testing.T) {
	for _, channels := range []int{0, 1, 2, 4} {
		want := channels
		if channels == 0 {
			want = DefaultChannels
		}
		f := &fakeQEMU{answer: migrations(func(c call) string {
			if r, ok := recordAnswers(c); ok {
				return r
			}
			switch c.Command {
			case "query-hotpluggable-cpus", "qom-list":
				return ret(`[]`)
			case "add-fd":
				return ret(`{"fdset-id": 3}`)
			}
			return ok
		}, "completed")}
		if _, _, err := f.attach(t).Checkpoint(ctx(t), image(t, "raw").File, SaveOptions{Channels: channels}, func(context.Context) error { return nil }); err != nil {
			t.Fatal(err)
		}
		multifd := false
		for _, c := range f.sent("migrate-set-capabilities")[0]["capabilities"].([]any) {
			if c := c.(map[string]any); c["capability"] == "multifd" {
				multifd = c["state"].(bool)
			}
		}
		count, set := f.sent("migrate-set-parameters")[0]["multifd-channels"]
		if multifd != (want > 1) || set != (want > 1) || set && count != float64(want) {
			t.Errorf("%d channels: multifd %t, multifd-channels %v", channels, multifd, count)
		}
	}
}

// A record QEMU refuses while the migration runs is asked for again once the state is written,
// and a checkpoint whose record QEMU never gives fails with what it could not read, its machine
// left stopped.
func TestACheckpointRecordsItsMachineOnceTheStateIsWritten(t *testing.T) {
	for _, tc := range []struct {
		name  string
		after bool // QEMU answers the record once the migration has completed
	}{
		{"refused during the migration", true},
		{"refused throughout", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			written := false
			f := &fakeQEMU{}
			f.answer = migrations(func(c call) string {
				if c.Command == "query-cpus-fast" && (!tc.after || !written) {
					return refused("not during a migration")
				}
				if r, ok := recordAnswers(c); ok {
					return r
				}
				switch c.Command {
				case "query-hotpluggable-cpus", "qom-list":
					return ret(`[]`)
				case "add-fd":
					return ret(`{"fdset-id": 3}`)
				case "remove-fd":
					written = true
				}
				return ok
			}, "active", "completed")
			saved, _, err := f.attach(t).Checkpoint(ctx(t), image(t, "raw").File, SaveOptions{}, func(context.Context) error { return nil })
			if tc.after {
				if want := fakeTarget(t); err != nil || !reflect.DeepEqual(saved.From, want) {
					t.Fatalf("Checkpoint = %+v, %v, want one taken on %+v", saved.From, err, want)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "listing the machine's vCPUs") {
				t.Fatalf("Checkpoint = %v, want the record it could not read", err)
			}
			if slices.Contains(f.commands(), "migrate_cancel") {
				t.Error("a written state was cancelled")
			}
		})
	}
}

// A resume rebuilds what the checkpoint says the machine had, asks for what the format needs,
// and returns when the guest runs: with Fast, before all of it is read.
func TestResume(t *testing.T) {
	again, _ := json.Marshal(added{
		CPUs:  []addedCPU{{ID: "vcpu1", Driver: "host-x86_64-cpu", Props: map[string]any{"core-id": 1}}},
		Disks: []hotDisk{{Serial: "ext", Target: 2}},
	})
	from := fakeTarget(t)
	mapped := Saved{Format: FormatMappedRAM, From: from, Added: again}
	for _, tc := range []struct {
		name     string
		saved    Saved
		load     LoadOptions
		channels int // the threads that read it
		statuses []string
		after    string // the run state once loaded
		caps     map[string]bool
		cont     bool
	}{
		{"fast, at postcopy-active", mapped, LoadOptions{Fast: true}, 1, []string{"setup", "postcopy-active"}, "running",
			map[string]bool{"mapped-ram": true, "postcopy-ram": true, "pause-before-switchover": false, "multifd": false, "events": true}, false},
		{"in full, past postcopy-active, which it never asked for", mapped, LoadOptions{}, DefaultChannels, []string{"active", "postcopy-active", "completed"}, "running",
			map[string]bool{"mapped-ram": true, "postcopy-ram": false, "pause-before-switchover": false, "multifd": DefaultChannels > 1, "events": true}, false},
		{"in full, by one channel, named", mapped, LoadOptions{Channels: 1}, 1, []string{"active", "completed"}, "running",
			map[string]bool{"mapped-ram": true, "postcopy-ram": false, "pause-before-switchover": false, "multifd": false, "events": true}, false},
		{"in full, by two channels", mapped, LoadOptions{Channels: 2}, 2, []string{"active", "completed"}, "running",
			map[string]bool{"mapped-ram": true, "postcopy-ram": false, "pause-before-switchover": false, "multifd": true, "events": true}, false},
		{"a stream checkpoint, saved stopped", Saved{Format: FormatStream, From: from}, LoadOptions{}, 1, []string{"completed"}, "paused",
			map[string]bool{"mapped-ram": false, "pause-before-switchover": false, "events": true}, true},
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
				return places(c)
			}, tc.statuses...)
			m := f.attach(t)
			disks := []Disk{{Serial: "root", Images: []Image{image(t, "qcow2")}}}
			if len(tc.saved.Added) > 0 {
				disks = append(disks, Disk{Serial: "ext", Readonly: true, Images: []Image{image(t, "qcow2")}})
			}
			if err := m.Resume(ctx(t), image(t, "raw").File, tc.saved, from, tc.load, disks...); err != nil {
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
			if params := f.sent("migrate-set-parameters"); tc.channels > 1 && (len(params) != 1 || params[0]["multifd-channels"] != float64(tc.channels)) ||
				tc.channels <= 1 && len(params) != 0 {
				t.Errorf("parameters %v, want multifd-channels only for more than one channel", params)
			}
			if got := len(f.sent("query-migrate")); got != 0 {
				t.Errorf("asked for the migration's state %d times: the load is waited on by its events", got)
			}
			if got := len(f.sent("cont")) == 1; got != tc.cont {
				t.Errorf("continued: %t, want %t", got, tc.cont)
			}
			if len(tc.saved.Added) > 0 {
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

// What a resume of a checkpoint of this machine refuses before reading a byte of the state.
func TestResumeRefuses(t *testing.T) {
	here := fakeTarget(t)
	// A Target that says it reads a format QEMU's Resume does not: the caller's, trusted.
	other := fakeTarget(t)
	other.Formats = append(other.Formats, "firecracker-snapshot")
	for _, tc := range []struct {
		name   string
		saved  Saved
		here   Target
		load   LoadOptions
		status string
		want   string
	}{
		{"a stream checkpoint, fast", Saved{Format: FormatStream, From: here}, here, LoadOptions{Fast: true}, "inmigrate", "loads only in full"},
		{"a stream checkpoint, by channels", Saved{Format: FormatStream, From: here}, here, LoadOptions{Channels: 2}, "inmigrate", "which one thread reads"},
		{"a format QEMU does not read", Saved{Format: "firecracker-snapshot", From: here}, other, LoadOptions{}, "inmigrate", `format "firecracker-snapshot"`},
		{"a machine held for a boot", Saved{Format: FormatMappedRAM, From: here}, here, LoadOptions{}, "prelaunch", "is prelaunch"},
		{"a record it cannot read", Saved{Format: FormatMappedRAM, From: here, Added: json.RawMessage(`[`)}, here, LoadOptions{}, "inmigrate", "what the checkpoint says"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeQEMU{answer: machineAnswers(tc.status, "root")}
			m := f.attach(t)
			err := m.Resume(ctx(t), image(t, "raw").File, tc.saved, tc.here, tc.load)
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

// A load that fails is the resume's error, without asking a QEMU that exits on it why.
func TestAFailedLoadFailsTheResume(t *testing.T) {
	from := fakeTarget(t)
	f := &fakeQEMU{answer: migrations(machineAnswers("inmigrate", "root"), "setup", "active", "failed")}
	err := f.attach(t).Resume(ctx(t), image(t, "raw").File, Saved{Format: FormatMappedRAM, From: from}, from, LoadOptions{},
		Disk{Serial: "root", Images: []Image{image(t, "qcow2")}})
	if err == nil || !strings.Contains(err.Error(), "the load failed") {
		t.Errorf("Resume = %v, want the load's failure", err)
	}
	if slices.Contains(f.commands(), "query-migrate") {
		t.Error("asked why of a QEMU that exits on a failed load")
	}
}

// Loaded waits for the last page, which may have come before it was called, and a load that failed
// after the guest ran is an error.
func TestLoaded(t *testing.T) {
	for _, tc := range []struct {
		name         string
		before, then []string // the MIGRATIONs sent before Loaded, and while it waits
		want         string
	}{
		{"completed while it waits", []string{"setup", "postcopy-active"}, []string{"completed"}, ""},
		{"completed before", []string{"postcopy-active", "completed"}, nil, ""},
		{"failed while it waits", []string{"postcopy-active"}, []string{"failed"}, "the load failed"},
		{"failed before", []string{"postcopy-active", "failed"}, nil, "the load failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeQEMU{answer: func(c call) string { return migrationEvents(tc.before...) + machineAnswers("running")(c) }}
			m := f.attach(t)
			if _, err := m.Status(ctx(t)); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- m.Loaded(ctx(t)) }()
			f.send(migrationEvents(tc.then...))
			err := <-done
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Errorf("Loaded = %v, want %q", err, tc.want)
			}
			if got := f.commands(); len(got) != 1 {
				t.Errorf("asked %v, want nothing after the status: the load is waited on by its events", got)
			}
		})
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
				if c.Command == "device_del" {
					// The guest let it go at once: DEVICE_DELETED arrives before the answer.
					return deleted(c.Args["id"].(string)) + "\n" + ok
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

// memoryAnswers is a machine of 512 MiB that may grow by 1 GiB in 2 MiB blocks, with 256 MiB
// plugged.
func memoryAnswers(c call) string {
	switch c.Command {
	case "query-memory-size-summary":
		return ret(`{"base-memory": 536870912, "plugged-memory": 268435456}`)
	case "qom-list-get":
		return ret(`[{"properties": [{"name": "size", "type": "size", "value": 268435456}, {"name": "block-size", "type": "size", "value": 2097152}]},
			{"properties": [{"name": "size", "type": "uint64", "value": 1073741824}]}]`)
	}
	return ok
}

// Memory is the boot memory and what virtio-mem plugged, its ceiling the growth backend's size
// above the boot memory, and its step virtio-mem's block.
func TestMemory(t *testing.T) {
	f := &fakeQEMU{answer: memoryAnswers}
	size, err := f.attach(t).Memory(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if want := (MemorySize{Now: 768, Base: 512, Ceiling: 1536, Block: 2}); size != want {
		t.Errorf("Memory = %+v, want %+v", size, want)
	}
	if got := f.sent("qom-list-get"); !reflect.DeepEqual(got, []map[string]any{{"paths": []any{"/machine/peripheral/vmem0", "/objects/mem.growth"}}}) {
		t.Errorf("read the growth from %v, want virtio-mem and its backend", got)
	}
}

// A machine started with no ceiling above its memory has no virtio-mem: its ceiling is its memory,
// and it has no step. Any other refusal is an error.
func TestMemoryOfAMachineThatMayNotGrow(t *testing.T) {
	answer := func(refusal string) func(c call) string {
		return func(c call) string {
			if c.Command == "qom-list-get" {
				return refusal
			}
			return memoryAnswers(c)
		}
	}
	f := &fakeQEMU{answer: answer(`{"error": {"class": "DeviceNotFound", "desc": "Device '/machine/peripheral/vmem0' not found"}}`)}
	size, err := f.attach(t).Memory(ctx(t))
	if want := (MemorySize{Now: 768, Base: 512, Ceiling: 512}); err != nil || size != want {
		t.Errorf("Memory = %+v, %v, want %+v", size, err, want)
	}
	f = &fakeQEMU{answer: answer(refused("no"))}
	if _, err := f.attach(t).Memory(ctx(t)); err == nil || !strings.Contains(err.Error(), "how far the machine's memory may grow") {
		t.Errorf("Memory = %v, want QEMU's refusal", err)
	}
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
		case "device_del":
			return deleted("hd4") + "\n" + ok
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

// deleted is DEVICE_DELETED for the device id.
func deleted(id string) string {
	return `{"event": "DEVICE_DELETED", "data": {"device": "` + id + `", "path": "/machine/peripheral/` + id + `"}}`
}

// A removal waits for its own device's DEVICE_DELETED: one QEMU sent before device_del is an
// earlier device's with the same id, and one for another device is not this one's.
func TestARemovalWaitsForItsOwnDeletion(t *testing.T) {
	f := &fakeQEMU{answer: func(c call) string {
		switch c.Command {
		case "qom-list":
			return deleted("hd4") + "\n" + ret(`[{"name": "hd4", "type": "child<scsi-hd>"}]`)
		case "qom-get":
			return ret(`"ext"`)
		case "device_del":
			return deleted("hd40") + "\n" + ok
		}
		return ok
	}}
	short, cancel := context.WithTimeout(ctx(t), 50*time.Millisecond)
	defer cancel()
	if err := f.attach(t).DetachDisk(short, "ext"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("DetachDisk = %v, want it to wait out its deadline", err)
	}
	if f.sent("query-named-block-nodes") != nil {
		t.Error("the disk's nodes were let go before QEMU reported it gone")
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
	if err := m.attachAt(ctx(t), maxHotplugDisks, Disk{Serial: "y", Images: []Image{image(t, "qcow2")}}); err == nil {
		t.Error("a target past the controller's was taken")
	}
	if err := m.AttachDisk(ctx(t), Disk{Serial: "bad serial"}); err == nil {
		t.Error("a disk validate refuses was attached")
	}
}

// Quit's reply may never come: QEMU closes the monitor first, which is the quit having worked. A
// quit QEMU refuses is an error.
func TestQuitThatClosesTheMonitorHasQuit(t *testing.T) {
	f := &fakeQEMU{answer: func(c call) string { return "" }}
	if err := f.attach(t).Quit(ctx(t)); err != nil {
		t.Errorf("Quit = %v", err)
	}
	f = &fakeQEMU{answer: func(c call) string { return refused("no") }}
	if err := f.attach(t).Quit(ctx(t)); err == nil || !strings.Contains(err.Error(), "quitting: quit: GenericError: no") {
		t.Errorf("a refused Quit = %v, want QEMU's refusal", err)
	}
}

func TestContinue(t *testing.T) {
	f := &fakeQEMU{answer: func(c call) string { return refused("no") }}
	if err := f.attach(t).Continue(ctx(t)); err == nil || !strings.Contains(err.Error(), "continuing the machine: cont: GenericError: no") {
		t.Errorf("Continue = %v, want QEMU's refusal", err)
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
	got, err := f.attach(t).CPUModels(ctx(t))
	if want := []CPUModel{{"Icelake-Server-v6", 3}, {"Skylake-Server-v4", 1}}; err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("Models = %v, %v, want %v", got, err, want)
	}
	for _, e := range f.sent("query-cpu-model-expansion") {
		if e["type"] != "static" {
			t.Errorf("expanded %v, want a static expansion", e)
		}
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
	again, _ := json.Marshal(added{Disks: []hotDisk{{Serial: "ext", Target: 2}}})
	from := fakeTarget(t)
	f := &fakeQEMU{answer: func(c call) string {
		if c.Command == "device_add" {
			return refused("no")
		}
		return machineAnswers("inmigrate", "root")(c)
	}}
	err := f.attach(t).Resume(ctx(t), image(t, "raw").File, Saved{Format: FormatMappedRAM, From: from, Added: again}, from, LoadOptions{},
		Disk{Serial: "ext", Images: []Image{image(t, "qcow2")}})
	if err == nil || !strings.Contains(err.Error(), "attaching it at target 2") {
		t.Errorf("Resume = %v, want the refused disk's error", err)
	}
}

// QEMU's refusal to let go of the state's descriptor set is the migration's error when the
// migration itself was accepted and ran, and not when it was refused or failed: the first failure
// is what is said. The set goes only once the migration is over, whatever became of it: a multifd
// channel opens the file from the set after the command has returned.
func TestMigrateSaysTheFirstFailure(t *testing.T) {
	for _, tc := range []struct {
		name             string
		migrate, removed string
		done             error
		want             string
	}{
		{"the set kept", ok, refused("busy"), nil, "remove-fd: GenericError: busy"},
		{"the migration refused", refused("no"), refused("busy"), nil, "migrate: GenericError: no"},
		{"the migration failed", ok, refused("busy"), errors.New("No space left on device"), "No space left on device"},
		{"both accepted", ok, ok, nil, ""},
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
			waited := false
			err := f.attach(t).migrate(ctx(t), migrateOut, image(t, "raw").File, os.O_WRONLY, func() error {
				waited = true
				if f.commands()[len(f.commands())-1] != "migrate" {
					t.Error("the set was removed before the migration was over")
				}
				return tc.done
			})
			if (tc.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.want)) {
				t.Errorf("migrate = %v, want %q", err, tc.want)
			}
			if waited == (tc.migrate != ok) {
				t.Errorf("waited for the migration: %t, when QEMU answered %s", waited, tc.migrate)
			}
			if cmds := f.commands(); cmds[len(cmds)-1] != "remove-fd" {
				t.Errorf("the last command was %s, want the set removed", cmds[len(cmds)-1])
			}
		})
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
