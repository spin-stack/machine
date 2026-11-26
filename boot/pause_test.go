// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"cmp"
	"context"
	"debug/elf"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/spin-stack/machine/internal/lab"
	"github.com/spin-stack/machine/internal/qmpdef"
	"github.com/spin-stack/machine/machine"
	"github.com/spin-stack/machine/qmp"
)

// pauseBound is the most a checkpoint's median pause (pause.held) may be.
// The machine plans 30 ms (machine's checkpointDowntime), and the bench read 14 ms p50 idle and
// 18 with a writer at 30, against 199 and 231 at QEMU's default of 300 (lab 44940051596,
// docs/memory-and-disk.md). 50 is the plan and 20 ms for what QEMU writes past it and cont: a
// QEMU that stops the guest early again reads four times over it, and a host somewhat slower
// than the bench still reads under it.
const pauseBound = 50 * time.Millisecond

// sanitizedPauseBound is pauseBound for a QEMU built with AddressSanitizer, which sanitize.yml
// times on the KVM fleet. There the busy-disk guest read 29.8-45.7 ms p50 on 8th-generation
// hosts and 41.5-54.7 on 7th-generation ones, 49 runs of 3 saves, and its slowest of ~600 saves
// 60.9 (Sanitize runs 2026-11-23 to 26, #813). The bench's release QEMU read 17.1-23.2 in 18
// runs: ASan's QEMU stops for longer, more so on the slower CPU, and the busy disk adds ~2 ms
// either way. 80 is the slowest median and a third; a QEMU that stops the guest early reads
// ~200 ms without ASan, still over it.
const sanitizedPauseBound = 80 * time.Millisecond

// What machine.QEMU has no call for: how much RAM QEMU still had to write when it stopped the
// guest, asked inside the seal.
type migrationLeft struct {
	RAM struct {
		Remaining int64 `json:"remaining"`
	} `json:"ram"`
}

var queryMigrationLeft = qmpdef.Define[qmp.None, migrationLeft]("query-migrate")

// pause is what one checkpoint cost the guest.
type pause struct {
	// stopped is GuestStopped to GuestRunning, cont included. GuestStopped is when the host heard
	// of the stop, and QEMU stops the guest first: on a laptop whose disk was busy, QEMU's downtime
	// read 8.68 s against 5.81 s from GuestStopped (2026-11-21). So the guest was stopped for at
	// least the larger of stopped and cost.Paused, held.
	stopped time.Duration
	// cost is QEMU's: Paused, its downtime, and Total, the whole save.
	cost machine.Cost
	// left is the RAM QEMU still had to write when it stopped the guest.
	left int64
}

// How long a checkpoint stops the guest, at spin's shape plugged to 2 GiB with 1.5 GiB of tmpfs
// filled from /dev/urandom, idle, and with 1 GiB filled and a writer rewriting 64 MiB of a 512 MiB
// tmpfs file every ~100 ms: the two guests that decided checkpointDowntime (#415). The third is
// the writer beside a busy disk, its guest writing its own disk too: what made a stop wait on the
// host's disk to flush the overlay (#426). The fourth is the third on a host short of memory, as a
// runner of many workspaces can be: no swap, all but shortMemoryHeadroom of it taken
// (shortOfMemory). Each saves
// SPIN_PAUSE_SAVES times (default 10) into a file on the scratch disk, continued after each, and logs
// each save's pause, QEMU's downtime, what was left at the stop and the save's total. The median
// pause is held under pauseBound. A QEMU bump that changes when migration stops the guest shows
// here (compat.yml runs it on every change to QEMU and before every release).
//
//	SPIN_PAUSE_TEST=1   it starts machines (task boot:checkpoint-pause)
func TestACheckpointPausesTheGuestBriefly(t *testing.T) {
	if os.Getenv("SPIN_PAUSE_TEST") == "" {
		t.Skip("set SPIN_PAUSE_TEST=1: this starts machines")
	}
	saves := envInt(t, "SPIN_PAUSE_SAVES", 10)
	// The overlays, the states and the busy disk's files on the disk a probe's go on: the bench
	// runner's /tmp is an overlayfs, and its instance store is where spin keeps them.
	scratch, err := lab.Scratch(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", scratch)
	r := newRecorder(t, 4000000)
	bound := pauseBound
	if addressSanitized(t, filepath.Join(r.out, "bin", "qemu-system-x86_64")) {
		bound = sanitizedPauseBound
	}
	writer := "dd if=/dev/urandom of=/mnt/src bs=1M count=64 2>/dev/null; dd if=/dev/urandom of=/mnt/ws bs=1M count=512 2>/dev/null; " +
		"(i=0; while :; do dd if=/mnt/src of=/mnt/ws bs=1M count=64 seek=$(( (i*64) % 512 )) conv=notrunc 2>/dev/null; i=$((i+1)); sleep 0.1; done) &"
	for _, tc := range []struct {
		name, fill, writer string
		// busy is a host writer on the state's file system throughout the saves (busyDisk).
		busy bool
		// short is the host short of memory throughout the saves (shortOfMemory).
		short bool
	}{
		{"idle", "dd if=/dev/urandom of=/mnt/fill bs=1M count=1536", "", false, false},
		{"writing", "dd if=/dev/urandom of=/mnt/fill bs=1M count=1024", writer, false, false},
		// Its guest also writes 1 MiB of its own disk every ~100 ms past its page cache and
		// without a sync, so a stop always has the guest's writes still to flush: with
		// conv=fsync instead, the guest had flushed them itself and the stop found nothing to.
		{"writing beside a busy disk", "dd if=/dev/urandom of=/mnt/fill bs=1M count=1024", writer +
			" (while :; do dd if=/dev/urandom of=/written bs=64k count=16 oflag=direct conv=notrunc 2>/dev/null; sleep 0.1; done) &", true, false},
		{"writing beside a busy disk short of memory", "dd if=/dev/urandom of=/mnt/fill bs=1M count=1024", writer +
			" (while :; do dd if=/dev/urandom of=/written bs=64k count=16 oflag=direct conv=notrunc 2>/dev/null; sleep 0.1; done) &", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.short {
				withoutSwap(t)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			s, mac := r.next(t, "/bin/sh")
			raw := filepath.Join(r.dir, fmt.Sprintf("p%d.sock", r.n))
			s.Monitors = []machine.Monitor{{Socket: raw}}
			g := startGuest(t, s, mac, nil)
			g.boot(t, r.overlay(t, t.TempDir()), r.base())
			g.type_(t, "mount -t proc proc /proc; echo UP-$((1+1))")
			g.await(t, regexp.MustCompile(`UP-2\b`), 60*time.Second)
			if err := g.SetMemory(ctx, 2048); err != nil {
				t.Fatal(err)
			}
			for size := (machine.MemorySize{}); size.Now != 2048; time.Sleep(50 * time.Millisecond) {
				var err error
				if size, err = g.Memory(ctx); err != nil {
					t.Fatal(err)
				}
			}
			g.type_(t, "mount -t tmpfs -o size=3g tmpfs /mnt && "+tc.fill+" 2>/dev/null; "+tc.writer+" echo FILLED-$((3+3))")
			g.await(t, regexp.MustCompile(`FILLED-6\b`), 3*time.Minute)
			time.Sleep(3 * time.Second)

			c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: raw, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			q, err := qmp.New(ctx, qmp.Unix(c), qmp.Observer{})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = q.Close() }()

			if tc.busy {
				busyDisk(t)
			}
			if tc.short {
				shortOfMemory(t)
			}
			var pauses []pause
			for range saves {
				p := checkpointOnce(ctx, t, g, q)
				t.Logf("stopped %6.2f ms, QEMU's downtime %v, left at the stop %d MiB, total %v, RAM %d MiB",
					ms(p.stopped), p.cost.Paused, p.left>>20, p.cost.Total, p.cost.RAM>>20)
				pauses = append(pauses, p)
				// The page cache writes the last state back before the next save, as on the bench.
				time.Sleep(2 * time.Second)
			}
			held := p50(pauses, pause.held)
			t.Logf("%s, %d saves, p50: held %.2f ms, stopped %.2f ms, QEMU's downtime %v, left at the stop %d MiB, total %v",
				tc.name, saves, ms(held), ms(p50(pauses, func(p pause) time.Duration { return p.stopped })),
				p50(pauses, func(p pause) time.Duration { return p.cost.Paused }),
				p50(pauses, func(p pause) int64 { return p.left })>>20, p50(pauses, func(p pause) time.Duration { return p.cost.Total }))
			// The host short of memory is measured, not held: compat's kvm host, 118 GiB of it taken,
			// stopped that guest 56 ms p50 against 25 on the 32 GiB bench, waiting on the kernel's
			// dirty-page throttling, which nothing tried shortened (compat 44976542939, #444).
			if held >= bound && !tc.short {
				t.Errorf("a checkpoint stopped the %s guest for %.2f ms p50, want under %v", tc.name, ms(held), bound)
			}
		})
	}
}

// addressSanitized says whether the QEMU at path calls into ASan's runtime. The release's QEMU is
// a static PIE, whose dynamic symbols are none.
func addressSanitized(t *testing.T, path string) bool {
	t.Helper()
	f, err := elf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	syms, err := f.DynamicSymbols()
	if err != nil {
		t.Fatalf("reading %s's symbols: %v", path, err)
	}
	return slices.ContainsFunc(syms, func(s elf.Symbol) bool { return s.Name == "__asan_init" })
}

// held is how long the guest was stopped at least.
func (p pause) held() time.Duration { return max(p.stopped, p.cost.Paused) }

// checkpointOnce checkpoints g into a new file, continues it, and says what that cost the guest.
func checkpointOnce(ctx context.Context, t *testing.T, g *guest, q *qmp.Client) pause {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close(); _ = os.Remove(f.Name()) }()
	told := &timeline{}
	var p pause
	_, p.cost, err = g.Checkpoint(ctx, f, machine.SaveOptions{Phases: told.tell}, func(ctx context.Context) error {
		left, err := qmp.Do(ctx, q, queryMigrationLeft, qmp.None{})
		p.left = left.RAM.Remaining
		return err
	})
	if err != nil {
		t.Fatalf("checkpointing: %v\n%s", err, tail([]byte(g.said()), 2000))
	}
	if err := g.Continue(ctx); err != nil {
		t.Fatal(err)
	}
	// As spin's runner does once the guest runs: what a save leaves in the page cache is written
	// before the next, which otherwise waits on it.
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	var stopped, running time.Time
	for _, ph := range told.told {
		switch ph := ph.(type) {
		case machine.GuestStopped:
			stopped = ph.At
		case machine.GuestRunning:
			running = ph.At
		}
	}
	if stopped.IsZero() || running.IsZero() {
		t.Fatalf("the checkpoint told no stop or no run:\n%s", told)
	}
	p.stopped = running.Sub(stopped)
	return p
}

// p50 is the median of what of says of each pause, the upper one of an even count.
func p50[T cmp.Ordered](pauses []pause, of func(pause) T) T {
	v := make([]T, len(pauses))
	for i, p := range pauses {
		v[i] = of(p)
	}
	slices.Sort(v)
	return v[len(v)/2]
}

// busyWriters is how many write beside a busy disk's guest. One, on the bench's instance store,
// never made a stop wait on its flush; with four, the stop's fdatasync took 13-79 ms (lab
// 44958247487, 2026-11-21).
const busyWriters = 4

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// busyDisk writes beside the test on TMPDIR's file system, where the overlay and the states are,
// until the test ends: what other workspaces do to a runner's disk. Each of busyWriters rewrites a
// 2 GiB file in 4 MiB writes through the page cache and syncs it every 256 MiB, so the disk always
// has writeback queued and ext4's journal a commit waiting on it.
func busyDisk(t *testing.T) {
	t.Helper()
	writers := busyWriters
	done := make(chan struct{})
	stopped := make(chan error, writers)
	for w := range writers {
		f, err := os.Create(filepath.Join(t.TempDir(), fmt.Sprintf("busy%d", w)))
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			defer func() { _ = f.Close() }()
			block := make([]byte, 4<<20)
			for i := 0; ; i++ {
				select {
				case <-done:
					stopped <- nil
					return
				default:
				}
				block[0] = byte(i)
				if _, err := f.WriteAt(block, int64(i%512)*int64(len(block))); err != nil {
					stopped <- fmt.Errorf("writing beside the test: %w", err)
					return
				}
				if i%64 == 63 {
					if err := f.Sync(); err != nil {
						stopped <- fmt.Errorf("syncing beside the test: %w", err)
						return
					}
				}
			}
		}()
	}
	t.Cleanup(func() {
		close(done)
		for range writers {
			if err := <-stopped; err != nil {
				t.Error(err)
			}
		}
	})
}

// shortMemoryHeadroom is what shortOfMemory leaves of the host's available memory: what the
// kernel then reclaims from, the page cache that the state, the overlay and the busy disk's
// writers go through, and QEMU's own file pages. With 1 GiB, the busy disk's dirty pages left the
// bench nothing to reclaim in time and the kernel killed the test (lab 44971162018, 2026-11-22).
const shortMemoryHeadroom = 4 << 30

// shortOfMemory takes the host's available memory but shortMemoryHeadroom until the test ends,
// anonymous and touched, as other workspaces' guests hold a runner's. spin's runners have no
// swap, and each machine's group has memory.swap.max 0 (spin-stack/spin's
// internal/vm/host/cgroup), so it is reclaimed from the page cache, never swapped, as on the
// bench, which has no swap either.
func shortOfMemory(t *testing.T) {
	t.Helper()
	size := memAvailable(t) - shortMemoryHeadroom
	if size <= 0 {
		t.Fatalf("the host has under %d MiB available to take", shortMemoryHeadroom>>20)
	}
	b, err := unix.Mmap(-1, 0, int(size), unix.PROT_READ|unix.PROT_WRITE, unix.MAP_PRIVATE|unix.MAP_ANONYMOUS|unix.MAP_POPULATE)
	if err != nil {
		t.Fatalf("taking %d MiB of the host's memory: %v", size>>20, err)
	}
	t.Cleanup(func() { _ = unix.Munmap(b) })
	t.Logf("took %d MiB of the host's memory, %d MiB left available", size>>20, memAvailable(t)>>20)
}

// withoutSwap skips a test on a host with swap: memory taken there pushes its other processes into
// swap, and the stop measured is that host's, which no runner is (#437).
func withoutSwap(t *testing.T) {
	t.Helper()
	swaps, err := os.ReadFile("/proc/swaps")
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(strings.TrimSpace(string(swaps)), "\n"); len(lines) > 1 {
		t.Skipf("this host swaps to %s, and a runner has no swap", strings.Fields(lines[1])[0])
	}
}

// memAvailable is the kernel's estimate of what the host can still give without swapping.
func memAvailable(t *testing.T) int64 {
	t.Helper()
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "MemAvailable:"); ok {
			kb, err := strconv.ParseInt(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "kB")), 10, 64)
			if err != nil {
				t.Fatalf("reading MemAvailable %q: %v", v, err)
			}
			return kb << 10
		}
	}
	t.Fatal("/proc/meminfo says no MemAvailable")
	return 0
}
