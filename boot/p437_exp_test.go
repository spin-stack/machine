// SPDX-License-Identifier: Apache-2.0

package boot_test

// Experiment for #437 only: never merged.

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/spin-stack/machine/machine"
)

// hogMemory takes all of MemAvailable but headroom MiB, anonymous and populated, until the test
// ends: a runner short of memory, with no swap.
func hogMemory(t *testing.T, headroom int64) {
	t.Helper()
	avail := memAvailable(t)
	size := avail - headroom<<20
	if size <= 0 {
		t.Logf("hog: MemAvailable %d MiB, under headroom", avail>>20)
		return
	}
	var maps [][]byte
	const chunk = 256 << 20
	for got := int64(0); got < size; got += chunk {
		b, err := unix.Mmap(-1, 0, chunk, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_PRIVATE|unix.MAP_ANONYMOUS|unix.MAP_POPULATE)
		if err != nil {
			t.Fatalf("hog: %v", err)
		}
		_ = unix.Madvise(b, unix.MADV_NOHUGEPAGE)
		for i := 0; i < len(b); i += 4096 {
			b[i] = 1
		}
		maps = append(maps, b)
	}
	t.Logf("hog: took %d MiB of %d MiB available, now %d MiB available", size>>20, avail>>20, memAvailable(t)>>20)
	t.Cleanup(func() {
		for _, b := range maps {
			_ = unix.Munmap(b)
		}
	})
}

func memAvailable(t *testing.T) int64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	s := bufio.NewScanner(f)
	for s.Scan() {
		if v, ok := strings.CutPrefix(s.Text(), "MemAvailable:"); ok {
			kb, _ := strconv.ParseInt(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "kB")), 10, 64)
			return kb << 10
		}
	}
	t.Fatal("no MemAvailable")
	return 0
}

// traced wraps s's QEMU so that it traces its migration and stop (-trace, wall clock), and its
// threads' off-CPU time is recorded with kernel stacks (bpftrace, through sudo), into
// SPIN_PAUSE_TRACE/name.
func traced(t *testing.T, s *machine.Spec, name string) {
	t.Helper()
	dir := os.Getenv("SPIN_PAUSE_TRACE")
	if dir == "" {
		return
	}
	dir = filepath.Join(dir, strings.ReplaceAll(name, " ", "_"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	events := filepath.Join(dir, "events")
	if err := os.WriteFile(events, []byte(strings.Join([]string{
		"runstate_set", "vm_stop_flush_all", "migrate_set_state", "migration_completion_vm_stop",
		"migration_bitmap_sync_start", "migration_bitmap_sync_end", "migration_thread_low_pending",
		"migration_precopy_complete", "savevm_state_setup", "savevm_state_cleanup", "ram_save_complete",
		"qcow2_cache_flush", "multifd_send_sync_main", "multifd_send_terminate_threads",
	}, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bt := filepath.Join(dir, "offcpu.bt")
	if err := os.WriteFile(bt, []byte(offcpuScript), 0o644); err != nil {
		t.Fatal(err)
	}
	sampler := filepath.Join(dir, "sampler.py")
	if err := os.WriteFile(sampler, []byte(samplerScript), 0o644); err != nil {
		t.Fatal(err)
	}
	wrap := filepath.Join(dir, "qemu")
	script := fmt.Sprintf(`#!/bin/sh
sudo -n bpftrace %s $$ > %s/offcpu.$$ 2>&1 &
python3 -I %s $$ %s/sample.$$ &
sleep 2
exec %s -msg timestamp=on -trace events=%s -D %s/trace.$$ "$@"
`, bt, dir, sampler, dir, s.QEMU, events, dir)
	if err := os.WriteFile(wrap, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("sudo", "-n", "bpftrace", "--version").CombinedOutput(); err != nil {
		t.Logf("no bpftrace: %v %s", err, out)
	}
	s.QEMU = wrap
}

// Every off-CPU interval of 5 ms or more of the traced QEMU's threads, with the kernel stack it
// slept in and the wall clock it woke at.
const offcpuScript = `
tracepoint:sched:sched_switch {
	if (pid == $1 && args.prev_state != 0) {
		@start[tid] = nsecs;
		@stack[tid] = kstack(24);
		@state[tid] = args.prev_state;
	}
	$t = @start[args.next_pid];
	if ($t != 0) {
		$d = (nsecs - $t) / 1000;
		if ($d >= 5000) {
			printf("OFF %s %d %s %d us state %d%s\n", strftime("%H:%M:%S.%f", nsecs), args.next_pid, args.next_comm, $d, @state[args.next_pid], @stack[args.next_pid]);
		}
		delete(@start[args.next_pid]);
		delete(@stack[args.next_pid]);
		delete(@state[args.next_pid]);
	}
}
`

const samplerScript = `
import os, sys, time
pid, out = sys.argv[1], sys.argv[2]
f = open(out, "w", buffering=1 << 16)
last = {}
KEYS = ("pswpin", "pswpout", "pgmajfault", "allocstall_normal", "allocstall_movable", "compact_stall",
        "pgsteal_direct", "pgsteal_kswapd", "workingset_refault_file", "workingset_refault_anon", "thp_fault_fallback", "thp_fault_alloc")
def vm():
    d = {}
    for l in open("/proc/vmstat"):
        k, v = l.split()
        if k in KEYS:
            d[k] = int(v)
    return d
lastvm, tick = None, 0
while os.path.exists(f"/proc/{pid}"):
    now = time.time()
    try:
        tids = os.listdir(f"/proc/{pid}/task")
    except FileNotFoundError:
        break
    for tid in tids:
        try:
            st = open(f"/proc/{pid}/task/{tid}/stat").read()
            comm = st[st.index("(") + 1:st.rindex(")")]
            fl = st[st.rindex(")") + 2:].split()
            state, majflt = fl[0], int(fl[9])
        except (FileNotFoundError, ProcessLookupError, ValueError, OSError):
            continue
        p = last.get(tid)
        dmaj = majflt - p if p is not None else 0
        if state == "D" or dmaj:
            f.write(f"{now:.6f} T {tid} {comm.replace(' ', '_')} {state} maj+{dmaj}\n")
        last[tid] = majflt
    tick += 1
    if tick % 25 == 0:
        v = vm()
        try:
            mem = {l.split(":")[0]: l.split()[1] for l in open(f"/proc/{pid}/status") if l.startswith(("VmSwap", "VmRSS"))}
            avail = [l.split()[1] for l in open("/proc/meminfo") if l.startswith("MemAvailable")][0]
        except Exception:
            mem, avail = {}, "?"
        if lastvm:
            f.write(f"{now:.6f} V rss={mem.get('VmRSS')} swap={mem.get('VmSwap')} avail={avail} " + " ".join(f"{k}+{v[k]-lastvm.get(k, 0)}" for k in v) + "\n")
        lastvm = v
    time.sleep(0.002)
`
