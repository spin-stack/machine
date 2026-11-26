// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/spin-stack/machine/machine"
)

// A disk limit on a real guest, at spin's shape with two disks beside its root, a and b: the
// guest's direct reads of a slow to its ReadBytes and its direct writes to its Writes while b's
// run free beside them, both recover once it is cleared, and QEMU reads each limit back as set. A
// disk attached while the machine runs, c, is held the same way. A checkpoint does not carry a
// limit: the machine resumed from one reads none and runs a free, and a limit set again holds it
// (machine.QEMU.SetDiskLimit).
//
//	SPIN_DISK_LIMIT_TEST=1   it starts machines (task boot:disk-limit)
func TestADiskLimitHoldsOneDiskAndLetsItGo(t *testing.T) {
	if os.Getenv("SPIN_DISK_LIMIT_TEST") == "" {
		t.Skip("set SPIN_DISK_LIMIT_TEST=1: this starts machines")
	}
	r := newRecorder(t, 3900000)
	dir := t.TempDir()
	overlay := r.overlay(t, dir)
	data := map[string]string{}
	for _, serial := range []string{"a", "b", "c"} {
		data[serial] = filepath.Join(dir, serial+".raw")
		f, err := os.Create(data[serial])
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(64 << 20); err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
	}
	disks := func(t *testing.T) []machine.Disk {
		t.Helper()
		root := rootDisk(t, overlay, r.base())
		out := []machine.Disk{root}
		for _, serial := range []string{"a", "b"} {
			d, err := machine.OpenDisk(machine.DiskPath{Serial: serial, Path: data[serial], Format: "raw"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = d.Close() })
			out = append(out, d)
		}
		return out
	}
	spec := func(t *testing.T) (machine.Spec, string) {
		s, mac := r.next(t, "/bin/sh")
		s.Places = []machine.Place{{Serial: "root"}, {Serial: "a"}, {Serial: "b"}}
		return s, mac
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	s, mac := spec(t)
	g := startGuest(t, s, mac, nil)
	if err := g.Boot(ctx, disks(t)...); err != nil {
		t.Fatalf("booting: %v\n%s", err, g.said())
	}
	g.type_(t, "mount -t proc proc /proc; mount -t sysfs sys /sys; echo UP-$((1+1))")
	g.await(t, regexp.MustCompile(`UP-2\b`), 60*time.Second)
	// The places are on the root complex in their order, so the guest names them vda, vdb, vdc.
	g.type_(t, `echo "SERIALS $(cat /sys/block/vdb/serial) $(cat /sys/block/vdc/serial)"`)
	g.await(t, serialsSaid, 10*time.Second)

	const rate = 8 << 20
	const writes = 50
	read := ddRead(32)
	write := ddWrite(150)
	limit := func(g *guest, serial string, l machine.DiskLimit) {
		t.Helper()
		start := time.Now()
		if err := g.SetDiskLimit(ctx, serial, l); err != nil {
			t.Fatal(err)
		}
		t.Logf("SetDiskLimit(%s, %+v) took %s", serial, l, time.Since(start))
		got, err := g.DiskLimit(ctx, serial)
		if err != nil || got != l {
			t.Fatalf("disk %s's limit reads back %+v, %v, want %+v", serial, got, err, l)
		}
	}

	free := g.side(t, read)
	t.Logf("free: a %.3f s, b %.3f s", free.a, free.b)

	limit(g, "a", machine.DiskLimit{ReadBytes: rate})
	if got, err := g.DiskLimit(ctx, "b"); err != nil || got != (machine.DiskLimit{}) {
		t.Errorf("disk b's limit reads %+v, %v, want none", got, err)
	}
	held := g.side(t, read)
	t.Logf("a read at %d MiB/s: a %.3f s, b %.3f s", rate>>20, held.a, held.b)
	// 32 MiB at 8 MiB/s, less the tenth of a second's worth QEMU lets through at once (avg/10).
	within(t, "a's 32 MiB at 8 MiB/s", held.a, 3.9)
	fast(t, "b's 32 MiB beside it", held.b)

	limit(g, "a", machine.DiskLimit{Writes: writes})
	wrote := g.side(t, write)
	t.Logf("a written at %d IOPS: a %.3f s, b %.3f s", writes, wrote.a, wrote.b)
	// 150 writes of 4 KiB at 50 a second, less the 5 let through at once.
	within(t, "a's 150 writes at 50/s", wrote.a, 2.9)
	fast(t, "b's 150 writes beside them", wrote.b)

	limit(g, "a", machine.DiskLimit{})
	cleared := g.side(t, read)
	t.Logf("cleared: a %.3f s, b %.3f s", cleared.a, cleared.b)
	fast(t, "a's 32 MiB once cleared", cleared.a)

	c, err := machine.OpenDisk(machine.DiskPath{Serial: "c", Path: data["c"], Format: "raw"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := g.AttachDisk(ctx, c); err != nil {
		t.Fatal(err)
	}
	g.type_(t, "while [ ! -e /dev/sda ]; do sleep 0.1; done; echo HOT-$((3+4))")
	g.await(t, regexp.MustCompile(`HOT-7\b`), 30*time.Second)
	limit(g, "c", machine.DiskLimit{ReadBytes: rate})
	hot := g.dd(t, read, "/dev/sda")[0]
	t.Logf("c, attached, read at %d MiB/s: %.3f s", rate>>20, hot)
	within(t, "c's 32 MiB at 8 MiB/s", hot, 3.9)
	if err := g.DetachDisk(ctx, "c"); err != nil {
		t.Fatal(err)
	}

	limit(g, "a", machine.DiskLimit{ReadBytes: rate})
	state, err := os.Create(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	saved, _, err := g.Checkpoint(ctx, state, machine.SaveOptions{}, func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("checkpointing: %v\n%s", err, g.said())
	}
	g.Kill()

	s, mac = spec(t)
	s.Restore = true
	h := startGuest(t, s, mac, nil)
	here, err := h.Target(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Resume(ctx, state, saved, here, machine.LoadOptions{}, disks(t)...); err != nil {
		t.Fatalf("resuming: %v\n%s", err, h.said())
	}
	if got, err := h.DiskLimit(ctx, "a"); err != nil || got != (machine.DiskLimit{}) {
		t.Errorf("disk a's limit after a resume reads %+v, %v, want none: a checkpoint carries no limit", got, err)
	}
	resumed := h.side(t, read)
	t.Logf("resumed: a %.3f s, b %.3f s", resumed.a, resumed.b)
	fast(t, "a's 32 MiB after the resume", resumed.a)
	limit(h, "a", machine.DiskLimit{ReadBytes: rate})
	again := h.side(t, read)
	t.Logf("set again: a %.3f s, b %.3f s", again.a, again.b)
	within(t, "a's 32 MiB at 8 MiB/s, set again after the resume", again.a, 3.9)
}

// ddRead is a guest command that reads n MiB of a disk past the guest's cache, ddWrite one that
// writes n 4 KiB blocks to it the same way, each with %s for the disk.
func ddRead(n int) string {
	return fmt.Sprintf("dd if=%%s of=/dev/null bs=1M count=%d iflag=direct", n)
}
func ddWrite(n int) string {
	return fmt.Sprintf("dd if=/dev/zero of=%%s bs=4k count=%d oflag=direct", n)
}

// sides is how long a command took on disk a and on disk b, in seconds, as dd said.
type sides struct{ a, b float64 }

// side runs the dd command on a (vdb) and b (vdc) at once.
func (g *guest) side(t *testing.T, dd string) sides {
	t.Helper()
	took := g.dd(t, dd, "/dev/vdb", "/dev/vdc")
	return sides{took[0], took[1]}
}

// dd runs the dd command on each disk at once and reads how long each took, in seconds, from dd's
// own summary, which it writes to a file on /dev, the one writable filesystem the shell has.
func (g *guest) dd(t *testing.T, dd string, disks ...string) []float64 {
	t.Helper()
	g.n++
	line := ""
	for i, d := range disks {
		line += fmt.Sprintf("%s 2>/dev/dd%d & ", fmt.Sprintf(dd, d), i)
	}
	line += "wait"
	for i := range disks {
		line += fmt.Sprintf("; echo S%d-%d $(tail -1 /dev/dd%d)", g.n, i, i)
	}
	g.type_(t, line)
	var took []float64
	for i := range disks {
		re := regexp.MustCompile(fmt.Sprintf(`S%d-%d \d+ bytes .* copied, ([0-9.]+) s`, g.n, i))
		g.await(t, re, 30*time.Second)
		s, err := strconv.ParseFloat(re.FindStringSubmatch(g.said())[1], 64)
		if err != nil {
			t.Fatal(err)
		}
		took = append(took, s)
	}
	return took
}

// within is a run held to a limit taking want seconds, within 15%.
func within(t *testing.T, what string, got, want float64) {
	t.Helper()
	if got < want*0.85 || got > want*1.15 {
		t.Errorf("%s took %.2f s, want %.1f s within 15%%", what, got, want)
	}
}

// fast is a run no limit holds: under a quarter of the shortest held one.
func fast(t *testing.T, what string, got float64) {
	t.Helper()
	if got > 0.7 {
		t.Errorf("%s took %.2f s, want one no limit holds, under 0.7 s", what, got)
	}
}

// serialsSaid is the guest naming vdb's and vdc's serials, a and b, in a line that has ended: until
// it ends, a serial is what was printed of it so far (#788).
var serialsSaid = regexp.MustCompile(`SERIALS a b\r?\n`)

// The serials line cut after a serial's first letter is not the guest naming a and b (#788).
func TestTheSerialsLineIsReadOnlyWhenItEnds(t *testing.T) {
	if serialsSaid.MatchString("SERIALS a b") {
		t.Error("a line still being printed, which may yet name bc, reads as a and b")
	}
	if !serialsSaid.MatchString("SERIALS a b\r\n# ") {
		t.Error("the whole line does not read as a and b")
	}
	if serialsSaid.MatchString("SERIALS a bc\r\n") {
		t.Error("serials a and bc read as a and b")
	}
}
