// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// limitAnswers is a machine with the places root and home, home held to the limits in limits, and
// ext attached at hotplug target 2; refuse is what QEMU says to block_set_io_throttle, nothing when
// empty.
func limitAnswers(limits, refuse string) func(c call) string {
	places := machineAnswers("running", "root", "home")
	return func(c call) string {
		switch c.Command {
		case "query-block":
			return ret(`[{"qdev": "/machine/peripheral/disk0/virtio-backend", "inserted": {"node-name": "slot0", "ro": false}},
				{"qdev": "/machine/peripheral/disk1/virtio-backend", "inserted": {"node-name": "slot1", "ro": false, ` + limits + `}},
				{"qdev": "/machine/peripheral/hd2", "inserted": {"node-name": "d-ext-0", "ro": false}},
				{"qdev": "/machine/peripheral/hd5"}]`)
		case "qom-list":
			return ret(`[{"name": "hd2", "type": "child<scsi-hd>"}, {"name": "hd5", "type": "child<scsi-hd>"}]`)
		case "qom-get":
			switch c.Args["path"] {
			case "/machine/peripheral/hd2":
				return ret(`"ext"`)
			case "/machine/peripheral/hd5":
				return ret(`"empty"`)
			}
		case "block_set_io_throttle":
			if refuse != "" {
				return refused(refuse)
			}
		case "query-blockstats":
			return ret(`[{"qdev": "/machine/peripheral/disk0/virtio-backend", "stats": {"rd_bytes": 9, "wr_bytes": 9, "rd_operations": 9, "wr_operations": 9}},
				{"qdev": "/machine/peripheral/disk1/virtio-backend", "stats": {"rd_bytes": 100, "wr_bytes": 200, "rd_operations": 3, "wr_operations": 4}}]`)
		}
		return places(c)
	}
}

// A limit is set on the backend of the disk's device, a place's or a hot disk's, every limit sent,
// and read back from what QEMU lists of that backend.
func TestADiskLimitIsSetOnItsBackendAndReadBack(t *testing.T) {
	f := &fakeQEMU{answer: limitAnswers(`"bps_rd": 1, "bps_wr": 2, "iops_rd": 3, "iops_wr": 4, "bps": 0`, "")}
	m := f.attach(t)
	if err := m.SetDiskLimit(ctx(t), "home", DiskLimit{ReadBytes: 1 << 20, Writes: 50}); err != nil {
		t.Fatal(err)
	}
	if err := m.SetDiskLimit(ctx(t), "ext", DiskLimit{WriteBytes: 7, Reads: 8}); err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{
		{"id": "/machine/peripheral/disk1/virtio-backend", "bps": 0.0, "bps_rd": float64(1 << 20), "bps_wr": 0.0, "iops": 0.0, "iops_rd": 0.0, "iops_wr": 50.0},
		{"id": "/machine/peripheral/hd2", "bps": 0.0, "bps_rd": 0.0, "bps_wr": 7.0, "iops": 0.0, "iops_rd": 8.0, "iops_wr": 0.0},
	}
	if got := f.sent("block_set_io_throttle"); !reflect.DeepEqual(got, want) {
		t.Errorf("block_set_io_throttle\n%v\nwant\n%v", got, want)
	}
	got, err := m.DiskLimit(ctx(t), "home")
	if want := (DiskLimit{ReadBytes: 1, WriteBytes: 2, Reads: 3, Writes: 4}); err != nil || got != want {
		t.Errorf("DiskLimit(home) = %+v, %v, want %+v", got, err, want)
	}
	if got, err := m.DiskLimit(ctx(t), "root"); err != nil || got != (DiskLimit{}) {
		t.Errorf("DiskLimit(root) = %+v, %v, want none", got, err)
	}
}

func TestADiskLimitNamesTheDiskItCannotSet(t *testing.T) {
	for _, tc := range []struct {
		name, serial, refuse, want string
	}{
		{"no such disk", "nope", "", `the machine has no disk "nope"`},
		{"a device with no disk in it", "empty", "", `finding disk "empty"'s backend: QEMU lists no disk in device hd5`},
		{"refused", "home", "bps_rd must be within [0, 1000000000000000]", `limiting disk "home" to {ReadBytes:-1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeQEMU{answer: limitAnswers(`"bps_rd": 0`, tc.refuse)}
			m := f.attach(t)
			err := m.SetDiskLimit(ctx(t), tc.serial, DiskLimit{ReadBytes: -1})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("SetDiskLimit = %v, want %q", err, tc.want)
			}
			if len(m.held.by) != 0 {
				t.Errorf("a limit QEMU did not take is held: %+v", m.held.by)
			}
			if _, err := m.DiskLimit(ctx(t), tc.serial); tc.refuse == "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Errorf("DiskLimit = %v, want %q", err, tc.want)
			}
		})
	}
}

// The probe reads the counters of each disk held, against its limit, by its serial, and asks QEMU
// nothing once none is held.
func TestTheProbeReadsTheHeldDisksCounters(t *testing.T) {
	f := &fakeQEMU{answer: limitAnswers(`"bps_rd": 0`, "")}
	m := f.attach(t)
	var s limitSample
	if err := m.heldSample(ctx(t), &s); err != nil || s.diskMax != nil {
		t.Fatalf("with no disk held, heldSample = %v, %+v", err, s)
	}
	if err := m.SetDiskLimit(ctx(t), "home", DiskLimit{ReadBytes: 10, Writes: 5}); err != nil {
		t.Fatal(err)
	}
	for _, serial := range []string{"root", "ext"} {
		if err := m.SetDiskLimit(ctx(t), serial, DiskLimit{Reads: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.heldSample(ctx(t), &s); err != nil {
		t.Fatal(err)
	}
	// ext's device has no counters in the answer, and so no sample.
	wantMax := map[string]map[string]int64{"home": {"rbps": 10, "wiops": 5}, "root": {"riops": 1}}
	wantStat := map[string]map[string]int64{"home": {"rbytes": 100, "wbytes": 200, "rios": 3, "wios": 4},
		"root": {"rbytes": 9, "wbytes": 9, "rios": 9, "wios": 9}}
	if !reflect.DeepEqual(s.diskMax, wantMax) || !reflect.DeepEqual(s.diskStat, wantStat) {
		t.Errorf("held disks\n%+v %+v\nwant\n%+v %+v", s.diskMax, s.diskStat, wantMax, wantStat)
	}
	for _, serial := range []string{"home", "root", "ext"} {
		if err := m.SetDiskLimit(ctx(t), serial, DiskLimit{}); err != nil {
			t.Fatal(err)
		}
	}
	asked := len(f.recorded())
	s = limitSample{}
	if err := m.heldSample(ctx(t), &s); err != nil || s.diskMax != nil || len(f.recorded()) != asked {
		t.Errorf("with every limit cleared, heldSample = %v, %+v, and asked %v", err, s, f.commands()[asked:])
	}
}

func TestAHeldDisksCountersQEMUDoesNotGiveAreAnError(t *testing.T) {
	answer := limitAnswers(`"bps_rd": 0`, "")
	f := &fakeQEMU{answer: func(c call) string {
		if c.Command == "query-blockstats" {
			return refused("busy")
		}
		return answer(c)
	}}
	m := f.attach(t)
	if err := m.SetDiskLimit(ctx(t), "home", DiskLimit{ReadBytes: 10}); err != nil {
		t.Fatal(err)
	}
	var s limitSample
	if err := m.heldSample(ctx(t), &s); err == nil || !strings.Contains(err.Error(), "reading the held disks' counters") {
		t.Errorf("heldSample = %v, want the counters' error", err)
	}
}

// A held disk is Limited each second its rate reaches 90% of its limit, named by its serial, and
// not in the second it was first held, which has no counters before it to make a rate of.
func TestAHeldDiskIsLimitedAtItsRate(t *testing.T) {
	t0 := time.Unix(1000, 0)
	sample := func(at time.Duration, held bool, rbytes, wios int64) limitSample {
		s := limitSample{at: t0.Add(at)}
		if held {
			s.diskMax = map[string]map[string]int64{"home": {"rbps": 8 << 20, "wiops": 100}}
			s.diskStat = map[string]map[string]int64{"home": {"rbytes": rbytes, "wios": wios}}
		}
		return s
	}
	var lw limitWatch
	lw.next(sample(0, false, 0, 0))
	if got := lw.next(sample(time.Second, true, 1<<40, 1<<40)); got != nil {
		t.Errorf("the second a disk was first held, it was %+v", got)
	}
	got := lw.next(sample(2*time.Second, true, 1<<40+8<<20, 1<<40+80))
	want := []Limited{{At: t0.Add(2 * time.Second), Limit: DiskReadBytes, Device: "home", Set: 8 << 20, Used: 8 << 20, Since: t0.Add(time.Second)}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("held\n%+v\nwant\n%+v", got, want)
	}
	got = lw.next(sample(3*time.Second, true, 1<<40+8<<20, 1<<40+170))
	want = []Limited{{At: t0.Add(3 * time.Second), Limit: DiskWrites, Device: "home", Set: 100, Used: 90, Since: t0.Add(2 * time.Second)}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("held\n%+v\nwant\n%+v", got, want)
	}
}

func TestADiskLimitIsNamedForItsField(t *testing.T) {
	for l, want := range map[Limit]string{DiskReadBytes: "disk limit read bytes", DiskWriteBytes: "disk limit write bytes",
		DiskReads: "disk limit reads", DiskWrites: "disk limit writes"} {
		if got := l.String(); got != want {
			t.Errorf("%d is %q, not %q", int(l), got, want)
		}
	}
}
