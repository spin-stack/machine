// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestWhatTheActuatorsCost times the two actuators spin reaches for inside a running workspace
// until its guest shows what they did (docs/machine.md, "Actuators"; #549). A workspace of spin's
// shape, booted to systemd at 2048 MiB and logged into over SSH, is given each in turn,
// SPIN_ACTUATORS_REPS times (default 10):
//
//   - SetMemory from 2048 down to 1024 MiB, idle, until the guest's MemTotal is 1024 MiB less,
//     and then back up to 2048;
//   - the same with the page cache full: 1536 MiB of the root disk read just before;
//   - SetCPUs from 1 to 2 and back, until /sys/devices/system/cpu/online shows it.
//
// Memory is read from MemTotal, not from the blocks /sys/devices/system/memory has online.
// virtio-mem takes memory back in its own blocks, a few MiB each, from inside a 128 MiB memory
// block, and a block stays online until all of it is gone. Read by the blocks online, an unplug
// QEMU had finished stood at 1536 MiB for 2 minutes in 19 of 20 (bench, lab 45113957566,
// 2026-11-23). The ready line also says the MiB of blocks online.
//
// Each is timed from the call to the guest's line saying it saw it, and the call's own return is
// logged beside it: SetMemory returns once QEMU has the request, SetCPUs down once QEMU has let the
// vCPU go. The guest polls every millisecond and sleeps between polls, since a loop that spun
// would take the one vCPU from the work it waits on. A guest that does not show it within
// actuatorWithin is counted as not reached, with the last state it showed and virtio-mem's lines
// in its kernel log.
//
//	SPIN_ACTUATORS_TEST=1    it starts a machine (task boot:actuators)
//	SPIN_ACTUATORS_REPS=N    times each, default 10
func TestWhatTheActuatorsCost(t *testing.T) {
	if os.Getenv("SPIN_ACTUATORS_TEST") == "" {
		t.Skip("set SPIN_ACTUATORS_TEST=1: this starts a machine")
	}
	reps := envInt(t, "SPIN_ACTUATORS_REPS", 10)
	x := newRollback(t)
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Minute)
	defer cancel()
	w := x.r.workspace(t, x.signer, false)
	defer w.Kill()
	top := filepath.Join(x.dir, "top.qcow2")
	mustRun(t, filepath.Join(x.r.out, "bin", "qemu-img"), "create", "-q", "-f", "qcow2", "-b", x.frozen, "-F", "qcow2", top)
	w.boot(ctx, t, top, []string{x.frozen, x.raw})
	w.sh(t, "systemctl is-system-running --wait >/dev/null || true")

	// MemTotal at 2048 MiB, less what the kernel keeps for itself. A block taken whole takes its
	// memmap with it, a few MiB that were never in MemTotal, so the watch allows memSlack.
	var full int
	if _, err := fmt.Sscan(w.sh(t, `awk '$1 == "MemTotal:" { print int($2 / 1024) }' /proc/meminfo`), &full); err != nil {
		t.Fatalf("reading MemTotal: %v", err)
	}
	down, up := fmt.Sprintf("mem le %d", full-1024+memSlack), fmt.Sprintf("mem ge %d", full-memSlack)
	var rows []actuation
	for i := range reps {
		for _, full := range []bool{false, true} {
			name := "SetMemory 2048 → 1024 MiB, idle"
			if full {
				name = "SetMemory 2048 → 1024 MiB, page cache full"
				t.Logf("#%d: %s", i, w.sh(t, `dd if=/dev/vda of=/dev/null bs=1M count=1536 2>/dev/null; awk '$1 == "Buffers:" || $1 == "Cached:" || $1 == "MemFree:" { printf "%s %d MiB ", $1, $2 / 1024 }' /proc/meminfo`))
			}
			rows = append(rows, w.actuate(t, name, down, func() error { return w.SetMemory(ctx, 1024) }))
			rows = append(rows, w.actuate(t, "SetMemory 1024 → 2048 MiB", up, func() error { return w.SetMemory(ctx, 2048) }))
		}
		rows = append(rows, w.actuate(t, "SetCPUs 1 → 2", "cpu eq 0-1", func() error { return w.SetCPUs(ctx, 2) }))
		rows = append(rows, w.actuate(t, "SetCPUs 2 → 1", "cpu eq 0", func() error { return w.SetCPUs(ctx, 1) }))
	}
	t.Log(actuationTable(rows))
}

// memSlack is how far from 1024 MiB less the guest's MemTotal may be and count as there: the memmap
// of eight 128 MiB blocks is 16 MiB.
const memSlack = 16

// actuatorWithin is how long the guest is given to show what an actuator did. virtio-mem's driver
// retries a block it could not empty on a timer that backs off, so an unplug the guest has not
// shown by then is recorded as not reached, with how far it got.
const actuatorWithin = 2 * time.Minute

// actuation is one actuator's call, timed from its start to its return and to the guest's seeing
// it; reached is false when the guest did not show it within actuatorWithin.
type actuation struct {
	name        string
	call, guest time.Duration
	reached     bool
}

// actuatorWatch is run by bash in the guest as `actuatorWatch KIND OP WANT`: it prints "ready" and
// what it reads now, then polls until what it reads compares to WANT by OP, printing "at" and what
// it reads each time that changes, and "done" and what it read at the end. KIND mem is the guest's MemTotal in
// MiB, cpu is /sys/devices/system/cpu/online.
// Between polls it waits a millisecond on its input, which the host leaves open and silent.
const actuatorWatch = `kind=$1 op=$2 want=$3
read -r bs < /sys/devices/system/memory/block_size_bytes
bs=$((16#$bs))
state() {
	if [ "$kind" = mem ]; then
		while read -r k v _; do
			[ "$k" = MemTotal: ] && break
		done < /proc/meminfo
		s=$((v / 1024))
	else
		read -r s < /sys/devices/system/cpu/online
	fi
}
seen() {
	case $op in
	eq) [ "$s" = "$want" ] ;;
	*) [ "$s" -"$op" "$want" ] ;;
	esac
}
state
blocks() {
	n=0
	for f in /sys/devices/system/memory/memory*/online; do
		read -r v < "$f" && [ "$v" = 1 ] && n=$((n + 1))
	done
	echo "$((n * bs / 1048576)) MiB in blocks online"
}
echo "ready $s, $(blocks)"
last=$s
until seen; do
	read -r -t 0.001 _ || true
	state
	[ "$s" = "$last" ] || { echo "at $s"; last=$s; }
done
echo "done $s"
`

// actuate starts the guest's watch for what (KIND OP WANT), runs call once it is ready, and says
// how long the call took and how long until the guest saw its effect.
func (w *rollbackGuest) actuate(t *testing.T, name, what string, call func() error) actuation {
	t.Helper()
	s, err := w.ssh.NewSession()
	if err != nil {
		t.Fatalf("opening a session for %s: %v", name, err)
	}
	defer func() { _ = s.Close() }()
	in, err := s.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close() }()
	out, err := s.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start("bash -c " + shellQuote(actuatorWatch) + " watch " + what); err != nil {
		t.Fatalf("watching for %s: %v", name, err)
	}
	lines := make(chan string, 2)
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	ready := awaitLine(t, lines, "ready", 10*time.Second, name)
	start := time.Now()
	if err := call(); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	called := time.Since(start)
	last, lastAt := ready, time.Duration(0)
	timeout := time.After(actuatorWithin)
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("%s: the guest's watch ended before it saw %s", name, what)
			}
			if at, found := strings.CutPrefix(l, "at "); found {
				last, lastAt = at, time.Since(start)
			}
			if done, found := strings.CutPrefix(l, "done "); found {
				seen := time.Since(start)
				t.Logf("%s: %s to %s, the call %v, the guest %v", name, ready, done, called.Round(10*time.Microsecond), seen.Round(10*time.Microsecond))
				return actuation{name, called, seen, true}
			}
		case <-timeout:
			t.Logf("%s: not within %v; from %s the guest got to %s at %v. virtio-mem in its kernel log:\n%s", name, actuatorWithin, ready, last,
				lastAt.Round(10*time.Microsecond), w.sh(t, "dmesg | grep -i virtio_mem | tail -5"))
			return actuation{name, called, 0, false}
		}
	}
}

// awaitLine is the first line from lines that starts with prefix, failing the test when none comes
// within the time given.
func awaitLine(t *testing.T, lines <-chan string, prefix string, within time.Duration, name string) string {
	t.Helper()
	timeout := time.After(within)
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("%s: the guest's watch ended before it said %q", name, prefix)
			}
			if after, found := strings.CutPrefix(l, prefix+" "); found {
				return after
			}
		case <-timeout:
			t.Fatalf("%s: the guest did not say %q within %v", name, prefix, within)
		}
	}
}

// shellQuote quotes s as one word for the guest's shell.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// actuationTable is each actuator's p50, minimum and maximum, the call's and the guest's, in the
// order they were first run.
func actuationTable(rows []actuation) string {
	var names []string
	by := map[string][]actuation{}
	for _, r := range rows {
		if _, ok := by[r.name]; !ok {
			names = append(names, r.name)
		}
		by[r.name] = append(by[r.name], r)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\nms, p50 [min, max] over the repetitions\n%-44s %24s %24s\n", "actuator", "the call returned", "the guest saw it")
	for _, n := range names {
		var call, guest []time.Duration
		for _, r := range by[n] {
			call = append(call, r.call)
			if r.reached {
				guest = append(guest, r.guest)
			}
		}
		seen := "none within " + actuatorWithin.String()
		if len(guest) > 0 {
			seen = spread(guest)
		}
		fmt.Fprintf(&b, "%-44s %24s %24s, %d of %d\n", n, spread(call), seen, len(guest), len(by[n]))
	}
	return b.String()
}

func spread(d []time.Duration) string {
	ms := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
	return fmt.Sprintf("%.1f [%.1f, %.1f]", ms(median(d)), ms(slices.Min(d)), ms(slices.Max(d)))
}
