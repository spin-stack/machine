// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"cmp"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
	"time"

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
// tmpfs file every ~100 ms: the two guests that decided checkpointDowntime (#415). Each saves
// SPIN_PAUSE_SAVES times (default 10) into a file on TMPDIR's disk, continued after each, and logs
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
	r := newRecorder(t, 4000000)
	for _, tc := range []struct {
		name, fill, writer string
	}{
		{"idle", "dd if=/dev/urandom of=/mnt/fill bs=1M count=1536", ""},
		{"writing", "dd if=/dev/urandom of=/mnt/fill bs=1M count=1024",
			"dd if=/dev/urandom of=/mnt/src bs=1M count=64 2>/dev/null; dd if=/dev/urandom of=/mnt/ws bs=1M count=512 2>/dev/null; " +
				"(i=0; while :; do dd if=/mnt/src of=/mnt/ws bs=1M count=64 seek=$(( (i*64) % 512 )) conv=notrunc 2>/dev/null; i=$((i+1)); sleep 0.1; done) &"},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			if held >= pauseBound {
				t.Errorf("a checkpoint stopped the %s guest for %.2f ms p50, want under %v", tc.name, ms(held), pauseBound)
			}
		})
	}
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

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
