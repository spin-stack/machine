// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spin-stack/go-tools/versions"
	"github.com/spin-stack/machine/internal/build"
	xssh "golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

// fioName is the edit that puts fio in a tree's overlays, which the io probe runs.
const fioName = "fio"

// fioEdit is fio, built from the release versions.yaml pins on the guest's Ubuntu, installed in the
// overlay at /usr/local/bin/fio.
func fioEdit(checkout, dir string) (Edit, error) {
	pins, err := versions.Load(filepath.Join(checkout, versions.File))
	if err != nil {
		return Edit{}, err
	}
	e, err := pins.Get("fio")
	if err != nil {
		return Edit{}, err
	}
	source := filepath.Join(dir, fioName, "source")
	if err := fetchPinned(e.URL(), e.Pin, filepath.Join(source, "fio-"+e.Version+".tar.gz")); err != nil {
		return Edit{}, err
	}
	args, err := build.Args(pins, strings.Fields("ubuntu fio")...)
	if err != nil {
		return Edit{}, err
	}
	out := filepath.Join(dir, fioName)
	cmd := exec.Command("docker", slices.Concat([]string{"buildx", "build", "--progress=plain", "--platform", "linux/amd64",
		"--file", filepath.Join(checkout, "internal", "lab", "fio.Dockerfile")}, args,
		[]string{"--output", "type=local,dest=" + out, source})...)
	if b, err := cmd.CombinedOutput(); err != nil {
		return Edit{}, fmt.Errorf("building fio %s: %w: %s", e.Version, err, b)
	}
	bin := filepath.Join(out, "fio")
	sum, err := fileSHA256(bin)
	if err != nil {
		return Edit{}, fmt.Errorf("the fio %s build wrote no binary: %w", e.Version, err)
	}
	return Edit{Setup: `echo "` + sum + `  ` + bin + `" | sha256sum -c --quiet
install -m 0755 ` + bin + ` "$MNT/usr/local/bin/fio"
`}, nil
}

// ioProbe is what nesting costs a running workspace's disk I/O (#563): fio inside the guest
// against a disk attached after the boot, as spin attaches a workspace's disks, one job a row.
//
// The number the cost model needs is the host CPU a GiB moved takes, nested against metal: QEMU's
// own CPU time, which holds the guest's vCPUs (the guest's own time included) and the threads that
// do its disk I/O. Throughput alone can tie while one side burns more CPU to get it.
//
// The disk is a raw file on the host's tmpfs, on both hosts. The bench's scratch is local NVMe and
// bench-metal's is EBS, and QEMU writes through the host's page cache (no cache.direct), so a disk
// on either would have measured the writeback to two different devices beside the virtualization.
// On tmpfs every request ends in the host's memory, and what is left to differ is the path through
// one hypervisor or two. Which device spin's hosts should keep a workspace on is another question.
//
// Each job runs time-based with no ramp: the CPU is read around the whole of fio's run, so the
// bytes it is divided by are the whole run's too.
type ioProbe struct{}

// ioJob is one fio job, named as its row.
type ioJob struct {
	name, rw, bs string
	depth        int
}

// ioJobs are 4 KiB random I/O at one request in flight and at 32, and 1 MiB sequential at 8: the
// small requests a build or a database makes, where the cost is per request, and the large ones a
// copy makes, where it is per byte.
var ioJobs = []ioJob{
	{"randread-4k-qd1", "randread", "4k", 1},
	{"randwrite-4k-qd1", "randwrite", "4k", 1},
	{"randread-4k-qd32", "randread", "4k", 32},
	{"randwrite-4k-qd32", "randwrite", "4k", 32},
	{"read-1m-qd8", "read", "1m", 8},
	{"write-1m-qd8", "write", "1m", 8},
}

func (ioProbe) Name() string { return "io" }
func (ioProbe) About() string {
	return "fio inside the guest against a raw disk on the host's tmpfs, attached after the boot; host CPU is QEMU's, guest CPU is the guest's busy time"
}

func (ioProbe) Options() []Option {
	names := make([]string, len(ioJobs))
	for i, j := range ioJobs {
		names[i] = j.name
	}
	return append(shape,
		Option{"jobs", strings.Join(names, " "), "the fio jobs, a row each"},
		Option{"runtime", "10", "seconds each job runs"},
		Option{"size", "1024", "MiB of the attached disk, every page of it allocated before the boot"})
}

func (ioProbe) Rows(o Options) []string { return o.Fields("jobs") }
func (ioProbe) Phases() int             { return 1 }

func (ioProbe) Needs(o Options) []Need {
	return append(append(bootNeeds, Need{"boot", "vsock-cid"}, Need{"boot", "console"}, Need{"boot", "append"},
		Need{"boot", "qmp"}, Need{"attach", "disk-serial"}), plugNeeds(o)...)
}

func (ioProbe) Metrics() []Metric {
	return []Metric{
		{"iops", "IOPS", "fio's requests a second"},
		{"mibps", "MiB/s", "fio's bytes a second"},
		{"clat_p50", "µs", "a request's completion latency, median"},
		{"clat_p99", "µs", "a request's completion latency, 99th percentile"},
		{"host_cpu", "ms/GiB", "QEMU's CPU time, every thread, for each GiB moved"},
		{"guest_cpu", "ms/GiB", "the guest's busy time, all vCPUs, for each GiB moved"},
	}
}

// mutate-lane: TestEveryProbeMeasuresATree
func (ioProbe) Measure(_ context.Context, m *Measurement) error {
	i := slices.IndexFunc(ioJobs, func(j ioJob) bool { return j.name == m.Row })
	if i < 0 {
		return fmt.Errorf("io has no job %q", m.Row)
	}
	job := ioJobs[i]
	disk, remove, err := tmpfsDisk("io.raw", int64(m.Options.Int("size"))<<20)
	if err != nil {
		return err
	}
	defer remove()
	mc, cid, _, err := m.bootLoggedIn("io.qcow2")
	if mc != nil {
		defer mc.Stop()
	}
	if err != nil {
		return err
	}
	dev, err := m.attachRaw(cid, "io.qcow2", disk, "io")
	if err != nil {
		return err
	}
	if _, _, err := m.Key.Run(cid, xssh.KeyExchangeMLKEM768X25519, "test -x /usr/local/bin/fio"); err != nil {
		return Refuse("the tree has no fio: make it with --edit fio")
	}
	fio := fmt.Sprintf("/usr/local/bin/fio --output-format=json --name=%s --filename=%s --direct=1 --ioengine=io_uring --rw=%s --bs=%s --iodepth=%d --time_based --runtime=%d",
		job.name, dev, job.rw, job.bs, job.depth, m.Options.Int("runtime"))
	host0, err := qemuCPU(mc.PID())
	if err != nil {
		return err
	}
	_, out, err := m.Key.Run(cid, xssh.KeyExchangeMLKEM768X25519, "head -1 /proc/stat; "+fio+"; head -1 /proc/stat")
	if err != nil {
		return Miss("fio's %s: %v", job.name, err)
	}
	host1, err := qemuCPU(mc.PID())
	if err != nil {
		return err
	}
	guest, report, err := guestBusyAround(out)
	if err != nil {
		return Miss("fio's %s: %v", job.name, err)
	}
	r, err := parseFio(report)
	if err != nil {
		return Miss("fio's %s: %v", job.name, err)
	}
	gib := float64(r.bytes) / (1 << 30)
	if gib == 0 {
		return Miss("fio's %s moved nothing", job.name)
	}
	m.Record("iops", r.iops)
	m.Record("mibps", float64(r.bytes)/(1<<20)/r.seconds)
	m.Record("clat_p50", r.p50)
	m.Record("clat_p99", r.p99)
	m.Record("host_cpu", ms(host1-host0)/gib)
	m.Record("guest_cpu", ms(guest)/gib)
	m.Logf("%s: %.0f IOPS, host %.0f ms/GiB, guest %.0f ms/GiB", job.name, r.iops, ms(host1-host0)/gib, ms(guest)/gib)
	return nil
}

// tmpfsDir is a directory on the host's tmpfs for a measurement's disks, and what removes it, which
// the caller runs once the machine that has them open is stopped: left to the experiment's end, a
// run's disks would fill the host's memory.
func tmpfsDir() (string, func(), error) {
	dir, err := os.MkdirTemp("/dev/shm", "lab-disk")
	if err != nil {
		return "", nil, fmt.Errorf("making a directory on /dev/shm for a disk: %w", err)
	}
	return dir, removeAll(dir), nil
}

// tmpfsDisk is a raw disk of size bytes on the host's tmpfs, every page allocated, and what removes
// it (tmpfsDir).
func tmpfsDisk(name string, size int64) (string, func(), error) {
	dir, remove, err := tmpfsDir()
	if err != nil {
		return "", nil, err
	}
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err == nil {
		err = unix.Fallocate(int(f.Fd()), 0, 0, size)
		err = errors.Join(err, f.Close())
	}
	if err != nil {
		remove()
		return "", nil, fmt.Errorf("allocating %d MiB at %s: %w", size>>20, path, err)
	}
	return path, remove, nil
}

// attachRaw gives the machine booted as name (bootLoggedIn's, sized through its monitor) the raw
// disk at path, as spin attaches a workspace's disks, and returns the device the guest made of it.
// mutate-lane: TestEveryProbeMeasuresATree
func (m *Measurement) attachRaw(cid uint32, name, path, serial string) (string, error) {
	if m.Options.Int("plugged") == 0 {
		return "", fmt.Errorf("a disk is attached over the monitor that sizes the guest, and plugged=0 opens none")
	}
	if err := m.SpinMachine("attach", "--qmp", m.qmpPath(name), "--release", m.Tree.Dir, "--disk", path,
		"--disk-format", "raw", "--disk-serial", serial); err != nil {
		return "", Miss("attaching %s: %v", path, err)
	}
	// The disk sits on the hotplug controller, a SCSI disk to the guest, and the guest makes its
	// device asynchronously after QEMU has it.
	wait := fmt.Sprintf(`for i in $(seq 200); do d=$(lsblk -dnpo NAME,SERIAL | awk '$2 == "%s" { print $1 }'); test -n "$d" && break; sleep 0.05; done; echo "$d"`, serial)
	_, out, err := m.Key.Run(cid, xssh.KeyExchangeMLKEM768X25519, wait)
	dev := strings.TrimSpace(out)
	if err != nil || !strings.HasPrefix(dev, "/dev/") {
		return "", Miss("the guest made no device of the disk %s: %q %v", serial, dev, err)
	}
	return dev, nil
}

// qemuCPU is the CPU time process pid has had, every thread's, the exited ones' included:
// /proc/PID/stat's utime and stime. A vCPU thread's time running the guest is in its utime.
func qemuCPU(pid int) (time.Duration, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, fmt.Errorf("reading QEMU's CPU time: %w", err)
	}
	return statCPU(string(b))
}

// statCPU is utime plus stime of a /proc/PID/stat, read after the command's closing parenthesis,
// since a command may hold spaces. Both are in USER_HZ ticks, 100 a second on Linux's ABI.
func statCPU(stat string) (time.Duration, error) {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return 0, fmt.Errorf("a /proc/PID/stat with no command: %q", stat)
	}
	f := strings.Fields(stat[i+1:])
	if len(f) < 13 {
		return 0, fmt.Errorf("a /proc/PID/stat with no utime and stime: %q", stat)
	}
	u, err1 := strconv.ParseInt(f[11], 10, 64)
	s, err2 := strconv.ParseInt(f[12], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, fmt.Errorf("a /proc/PID/stat whose utime and stime are not numbers: %q", stat)
	}
	return time.Duration(u+s) * 10 * time.Millisecond, nil
}

// guestBusyAround reads a command's output that began and ended with the guest's /proc/stat cpu
// line, and returns the busy time between them and what came between. Busy is user, nice, system,
// irq and softirq: steal is time the vCPU did not run at all, and idle and iowait are not work.
func guestBusyAround(out string) (time.Duration, string, error) {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 3 {
		return 0, "", fmt.Errorf("no /proc/stat lines around the output: %q", out)
	}
	before, err := busyTicks(lines[0])
	if err != nil {
		return 0, "", err
	}
	after, err := busyTicks(lines[len(lines)-1])
	if err != nil {
		return 0, "", err
	}
	return time.Duration(after-before) * 10 * time.Millisecond, strings.Join(lines[1:len(lines)-1], "\n"), nil
}

func busyTicks(line string) (int64, error) {
	f := strings.Fields(line)
	if len(f) < 9 || f[0] != "cpu" {
		return 0, fmt.Errorf("not /proc/stat's cpu line: %q", line)
	}
	var busy int64
	for _, i := range []int{1, 2, 3, 6, 7} {
		n, err := strconv.ParseInt(f[i], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("/proc/stat's cpu line has %q for a count: %q", f[i], line)
		}
		busy += n
	}
	return busy, nil
}

// fioResult is one job's numbers from fio's JSON report, the read side's or the write side's.
type fioResult struct {
	iops, p50, p99, seconds float64
	bytes                   int64
}

func parseFio(report string) (fioResult, error) {
	type side struct {
		IOBytes int64   `json:"io_bytes"`
		IOPS    float64 `json:"iops"`
		Runtime float64 `json:"runtime"` // ms
		Clat    struct {
			Percentile map[string]float64 `json:"percentile"`
		} `json:"clat_ns"`
	}
	var r struct {
		Jobs []struct {
			Error int  `json:"error"`
			Read  side `json:"read"`
			Write side `json:"write"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal([]byte(report), &r); err != nil {
		return fioResult{}, fmt.Errorf("reading fio's report: %w: %.200q", err, report)
	}
	if len(r.Jobs) != 1 || r.Jobs[0].Error != 0 {
		return fioResult{}, fmt.Errorf("fio's report has %d jobs, or an error: %.200q", len(r.Jobs), report)
	}
	s := r.Jobs[0].Read
	if s.IOBytes == 0 {
		s = r.Jobs[0].Write
	}
	if s.Runtime == 0 {
		return fioResult{}, fmt.Errorf("fio's report has no runtime: %.200q", report)
	}
	return fioResult{iops: s.IOPS, bytes: s.IOBytes, seconds: s.Runtime / 1000,
		p50: s.Clat.Percentile["50.000000"] / 1000, p99: s.Clat.Percentile["99.000000"] / 1000}, nil
}
