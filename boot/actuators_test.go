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
//   - SetMemory from 2048 down to 1024 MiB, idle, until /sys/devices/system/memory has 1024 MiB
//     online, and then back up to 2048;
//   - the same with the page cache full: 1536 MiB of the root disk read just before;
//   - SetCPUs from 1 to 2 and back, until /sys/devices/system/cpu/online shows it.
//
// Each is timed from the call to the guest's line saying it saw it, and the call's own return is
// logged beside it: SetMemory returns once QEMU has the request, SetCPUs down once QEMU has let the
// vCPU go. The guest polls every millisecond and sleeps between polls, since a loop that spun
// would take the one vCPU from the work it waits on. A guest that does not show it within
// actuatorWithin fails the check: the actuator did not work.
//
//	SPIN_ACTUATORS_TEST=1    it starts a machine (task boot:actuators)
//	SPIN_ACTUATORS_REPS=N    times each, default 10
func TestWhatTheActuatorsCost(t *testing.T) {
	if os.Getenv("SPIN_ACTUATORS_TEST") == "" {
		t.Skip("set SPIN_ACTUATORS_TEST=1: this starts a machine")
	}
	reps := envInt(t, "SPIN_ACTUATORS_REPS", 10)
	x := newRollback(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	w := x.r.workspace(t, x.signer, false)
	defer w.Kill()
	top := filepath.Join(x.dir, "top.qcow2")
	mustRun(t, filepath.Join(x.r.out, "bin", "qemu-img"), "create", "-q", "-f", "qcow2", "-b", x.frozen, "-F", "qcow2", top)
	w.boot(ctx, t, top, []string{x.frozen, x.raw})
	w.sh(t, "systemctl is-system-running --wait >/dev/null || true")

	var rows []actuation
	for i := range reps {
		for _, full := range []bool{false, true} {
			name := "SetMemory 2048 → 1024 MiB, idle"
			if full {
				name = "SetMemory 2048 → 1024 MiB, page cache full"
				t.Logf("#%d: %s", i, w.sh(t, `dd if=/dev/vda of=/dev/null bs=1M count=1536 2>/dev/null; awk '$1 == "Buffers:" || $1 == "Cached:" || $1 == "MemFree:" { printf "%s %d MiB ", $1, $2 / 1024 }' /proc/meminfo`))
			}
			rows = append(rows, w.actuate(t, name, "mem le 1024", func() error { return w.SetMemory(ctx, 1024) }))
			rows = append(rows, w.actuate(t, "SetMemory 1024 → 2048 MiB", "mem ge 2048", func() error { return w.SetMemory(ctx, 2048) }))
		}
		rows = append(rows, w.actuate(t, "SetCPUs 1 → 2", "cpu eq 0-1", func() error { return w.SetCPUs(ctx, 2) }))
		rows = append(rows, w.actuate(t, "SetCPUs 2 → 1", "cpu eq 0", func() error { return w.SetCPUs(ctx, 1) }))
	}
	t.Log(actuationTable(rows))
}

// actuatorWithin is how long the guest is given to show what an actuator did.
const actuatorWithin = 20 * time.Second

// actuation is one actuator's call, timed from its start to its return and to the guest's seeing it.
type actuation struct {
	name        string
	call, guest time.Duration
}

// actuatorWatch is run by bash in the guest as `actuatorWatch KIND OP WANT`: it prints "ready" and
// what it reads now, then polls until what it reads compares to WANT by OP, and prints "done" and
// what it read. KIND mem is the MiB of memory blocks online, cpu is /sys/devices/system/cpu/online.
// Between polls it waits a millisecond on its input, which the host leaves open and silent.
const actuatorWatch = `kind=$1 op=$2 want=$3
read -r bs < /sys/devices/system/memory/block_size_bytes
bs=$((16#$bs))
state() {
	if [ "$kind" = mem ]; then
		n=0
		for f in /sys/devices/system/memory/memory*/online; do
			read -r v < "$f" && [ "$v" = 1 ] && n=$((n + 1))
		done
		s=$((n * bs / 1048576))
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
echo "ready $s"
until seen; do
	read -r -t 0.001 _ || true
	state
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
	done := awaitLine(t, lines, "done", actuatorWithin, name)
	seen := time.Since(start)
	t.Logf("%s: %s to %s, the call %v, the guest %v", name, ready, done, called.Round(10*time.Microsecond), seen.Round(10*time.Microsecond))
	return actuation{name, called, seen}
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
			call, guest = append(call, r.call), append(guest, r.guest)
		}
		fmt.Fprintf(&b, "%-44s %24s %24s\n", fmt.Sprintf("%s (%d)", n, len(by[n])), spread(call), spread(guest))
	}
	return b.String()
}

func spread(d []time.Duration) string {
	ms := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
	return fmt.Sprintf("%.1f [%.1f, %.1f]", ms(median(d)), ms(slices.Min(d)), ms(slices.Max(d)))
}
