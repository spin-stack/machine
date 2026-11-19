// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"bufio"
	"cmp"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	xssh "golang.org/x/crypto/ssh"
)

// What one more idle workspace costs its host, in every kind of memory the host spends on it,
// kernel included. #259 measured QEMU's own resident set (~7 MB anonymous beyond the guest's RAM,
// lab run 43564203014) and left out what the kernel allocates for each VM: KVM's page tables -
// SecPageTables read 0, which nobody believed - vhost's threads, the slab. A cost per workspace
// that leaves those out is a lower bound nobody can price.
//
// The host's /proc/meminfo and /proc/slabinfo are read before any machine starts and again after
// N of them have booted, each logged into once over vsock - the state a user finds - and then
// left idle for SETTLE seconds, long enough for the balloon's free page reporting to hand the
// guests' free memory back. The difference, divided by N, is one idle workspace. Each QEMU's
// smaps_rollup gives its share of the pages it holds (Pss), which splits the base image's and
// the kernel image's file pages among the machines that share them.
//
//	SPIN_DENSITY=1  run at all
//	N=<n>           machines (default 8)
//	SETTLE=<s>      seconds idle before the second reading (default 30)
func TestDensity(t *testing.T) {
	if os.Getenv("SPIN_DENSITY") == "" {
		t.Skip("set SPIN_DENSITY=1: boots N VMs with a vsock device and reads the host's memory")
	}
	out := releaseDir(t)
	if !canSudo() {
		t.Skip("this writes a key into each boot's own overlay through qemu-nbd, and reads slabinfo, which need sudo")
	}
	if _, err := os.Stat("/dev/vhost-vsock"); err != nil {
		t.Skip("no /dev/vhost-vsock: run task vsock")
	}
	n := envInt(t, "N", 8)
	settle := time.Duration(envInt(t, "SETTLE", 30)) * time.Second
	rows, signer := firstLoginRows(t)
	v := rows[len(rows)-1] // the image as this branch builds it

	// Overlays are made before the first reading: qemu-nbd and the overlays' own page cache are the
	// harness's, not a workspace's.
	runs := make([]*checkpointRun, n)
	for i := range runs {
		socks, err := os.MkdirTemp("/tmp", "dn")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(socks) })
		r := &checkpointRun{v: v, out: out, signer: signer, dir: t.TempDir(), socks: socks}
		mustRun(t, filepath.Join(out, "bin", "qemu-img"), "create", "-f", "qcow2",
			"-F", "qcow2", "-b", filepath.Join(out, "image", "rootfs.qcow2"), r.overlay())
		editOverlay(t, r.overlay(), v)
		r.cid = uint32(100000 + rand.IntN(1<<20))
		runs[i] = r
	}
	dropCaches(t)
	before := hostMemory(t)

	var pids []int
	for i, r := range runs {
		r.launch(t, filepath.Join(r.socks, "q"))
		deadline := time.Now().Add(60 * time.Second)
		for !vsockAccepts(r.cid, 22) {
			if time.Now().After(deadline) {
				t.Fatalf("machine %d: sshd never accepted on vsock", i)
			}
			time.Sleep(5 * time.Millisecond)
		}
		for {
			if _, err := sshLogin(r.cid, signer, xssh.KeyExchangeMLKEM768X25519); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("machine %d: never logged in", i)
			}
			time.Sleep(10 * time.Millisecond)
		}
		pids = append(pids, qemuPID(t, filepath.Join(r.socks, "q")))
	}
	t.Logf("%d machines up and logged into; idle for %v", n, settle)
	time.Sleep(settle)
	after := hostMemory(t)

	per := func(key string) float64 { return float64(after.meminfo[key]-before.meminfo[key]) / float64(n) / 1024 }
	var b strings.Builder
	fmt.Fprintf(&b, "\none idle workspace (%s vCPUs, %s MiB), the host's memory after %d of them, idle %v, less before, over %d; MiB\n\n",
		v.cpus, v.memory, n, settle, n)
	used := float64((before.meminfo["MemAvailable"]-after.meminfo["MemAvailable"])/int64(n)) / 1024
	fmt.Fprintf(&b, "  %-30s %8.1f\n", "used (MemAvailable)", used)
	for _, k := range []string{"AnonPages", "AnonHugePages", "Shmem", "Cached", "PageTables", "SecPageTables", "KernelStack", "Slab", "SUnreclaim", "SReclaimable", "VmallocUsed", "Percpu"} {
		fmt.Fprintf(&b, "  %-30s %8.2f\n", k, per(k))
	}

	var rss, pss, pssAnon, pssFile, private float64
	for _, pid := range pids {
		r := smapsRollup(t, pid)
		rss += r["Rss"]
		pss += r["Pss"]
		pssAnon += r["Pss_Anon"]
		pssFile += r["Pss_File"]
		private += r["Private_Clean"] + r["Private_Dirty"]
	}
	fmt.Fprintf(&b, "\n  each QEMU, mean over %d (its share of what it maps)\n", n)
	for _, kv := range []struct {
		k string
		v float64
	}{{"Rss", rss}, {"Pss", pss}, {"Pss_Anon", pssAnon}, {"Pss_File", pssFile}, {"Private", private}} {
		fmt.Fprintf(&b, "  %-30s %8.2f\n", kv.k, kv.v/float64(n)/1024)
	}

	type cache struct {
		name  string
		bytes int64
	}
	var caches []cache
	for name, after := range after.slab {
		if d := after - before.slab[name]; d > 0 {
			caches = append(caches, cache{name, d})
		}
	}
	slices.SortFunc(caches, func(a, b cache) int { return cmp.Compare(b.bytes, a.bytes) })
	fmt.Fprintf(&b, "\n  slab caches that grew most, KiB per workspace\n")
	for _, c := range caches[:min(12, len(caches))] {
		fmt.Fprintf(&b, "  %-30s %8.1f\n", c.name, float64(c.bytes)/float64(n)/1024)
	}
	t.Log(b.String())
}

type hostMem struct {
	meminfo map[string]int64 // KiB
	slab    map[string]int64 // bytes: active objects × object size
}

func hostMemory(t *testing.T) hostMem {
	t.Helper()
	m := hostMem{meminfo: map[string]int64{}, slab: map[string]int64{}}
	f, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(f), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		n, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
		m.meminfo[k] = n
	}
	// slabinfo is root's to read.
	s, err := exec.Command("sudo", "cat", "/proc/slabinfo").Output()
	if err != nil {
		t.Fatalf("reading /proc/slabinfo: %v", err)
	}
	sc := bufio.NewScanner(strings.NewReader(string(s)))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 || strings.HasPrefix(fields[0], "#") || fields[0] == "slabinfo" {
			continue
		}
		active, _ := strconv.ParseInt(fields[1], 10, 64)
		size, _ := strconv.ParseInt(fields[3], 10, 64)
		m.slab[fields[0]] += active * size
	}
	if len(m.slab) == 0 {
		t.Fatal("/proc/slabinfo listed no caches")
	}
	return m
}

// dropCaches writes back and drops the host's clean page cache, so what the machines' boots read
// is counted in the second reading and not already there in the first.
func dropCaches(t *testing.T) {
	t.Helper()
	if b, err := exec.Command("sudo", "sh", "-c", "sync && echo 3 > /proc/sys/vm/drop_caches").CombinedOutput(); err != nil {
		t.Fatalf("dropping the page cache: %v: %s", err, b)
	}
}

// qemuPID is the QEMU listening on the QMP socket at qmp: spin-machine execs QEMU, so it is the
// process whose command line names that socket.
func qemuPID(t *testing.T, qmp string) int {
	t.Helper()
	b, err := exec.Command("pgrep", "-f", "qemu-system-x86_64.*"+qmp).Output()
	if err != nil {
		t.Fatalf("finding the QEMU of %s: %v", qmp, err)
	}
	pid, err := strconv.Atoi(strings.Fields(string(b))[0])
	if err != nil {
		t.Fatalf("finding the QEMU of %s: %q", qmp, b)
	}
	return pid
}

// smapsRollup is a process's /proc/<pid>/smaps_rollup, in KiB by field.
func smapsRollup(t *testing.T, pid int) map[string]float64 {
	t.Helper()
	b, err := exec.Command("sudo", "cat", fmt.Sprintf("/proc/%d/smaps_rollup", pid)).Output()
	if err != nil {
		t.Fatalf("reading QEMU %d's smaps_rollup: %v", pid, err)
	}
	m := map[string]float64{}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		n, _ := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 64)
		m[k] = n
	}
	return m
}
