// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Host is what an experiment ran on. A number compares only with one taken on the same kind of
// host: nested KVM cost the kernel 78 ms against 45 on metal with the same CPU (labs 44563496308,
// 44563512999).
type Host struct {
	Name     string `json:"name"`
	Instance string `json:"instance,omitempty"` // the EC2 instance type, when it is one
	// Nested is whether this host is itself a virtual machine, so that the guests measured run
	// under KVM nested in it.
	Nested  bool   `json:"nested"`
	CPU     string `json:"cpu"`
	CPUs    int    `json:"cpus"`
	Kernel  string `json:"kernel"`
	Load    string `json:"load"`
	Scratch string `json:"scratch"`
	// ScratchDevice is the disk Scratch is on, and its model: a restore read its state from gp3
	// at ~300 MB/s on a c8i, ~680 of its ~840 ms (lab 44542990433), so no restore number is read
	// without it.
	ScratchDevice string `json:"scratch_device"`
}

// ReadHost is this host, with scratch the experiment's scratch directory.
// mutate-lane: TestEveryProbeMeasuresATree
func ReadHost(scratch string) Host {
	h := Host{CPUs: runtime.NumCPU(), Scratch: scratch, Instance: instanceType()}
	h.Name, _ = os.Hostname()
	if f, err := os.Open("/proc/cpuinfo"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			k, v, _ := strings.Cut(sc.Text(), ":")
			switch strings.TrimSpace(k) {
			case "model name":
				if h.CPU == "" {
					h.CPU = strings.TrimSpace(v)
				}
			case "flags":
				h.Nested = h.Nested || strings.Contains(" "+v+" ", " hypervisor ")
			}
		}
		_ = f.Close()
	}
	var u unix.Utsname
	if unix.Uname(&u) == nil {
		h.Kernel = unix.ByteSliceToString(u.Release[:])
	}
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		h.Load = strings.Join(strings.Fields(string(b))[:3], " ")
	}
	if out, err := exec.Command("df", "--output=source", scratch).Output(); err == nil {
		lines := strings.Fields(string(out))
		dev := lines[len(lines)-1]
		model, _ := exec.Command("lsblk", "-dno", "MODEL", dev).Output()
		h.ScratchDevice = strings.TrimSpace(dev + " " + strings.TrimSpace(string(model)))
	}
	return h
}

// instanceType asks EC2's metadata service, briefly: off EC2 nothing answers.
// mutate-lane: TestEveryProbeMeasuresATree
func instanceType() string {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	tokReq, _ := http.NewRequestWithContext(ctx, http.MethodPut, "http://169.254.169.254/latest/api/token", nil)
	tokReq.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "60")
	tok, err := http.DefaultClient.Do(tokReq)
	if err != nil {
		return ""
	}
	t, _ := io.ReadAll(tok.Body)
	_ = tok.Body.Close()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://169.254.169.254/latest/meta-data/instance-type", nil)
	req.Header.Set("X-aws-ec2-metadata-token", string(t))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	b, _ := io.ReadAll(resp.Body)
	return strings.TrimSpace(string(b))
}

func isTmpfs(dir string) bool {
	var st unix.Statfs_t
	return unix.Statfs(dir, &st) == nil && st.Type == unix.TMPFS_MAGIC
}

// instanceStore is a directory on the bench runner's local NVMe when it has one, which is where
// spin's hosts keep checkpoints.
//
// RunsOn may have taken the instance store already, through a partition or a RAID over it, so the
// disk itself shows no mount: where any device on it is mounted, that is used. Only a disk with
// nothing on it - no partition, no holder, no filesystem signature - is formatted; the first
// version formatted on "not mounted" alone, and only failing first kept it off a disk RunsOn was
// using (2026-11-19).
// mutate-lane: TestEveryProbeMeasuresATree
func instanceStore() (string, error) {
	out, err := exec.Command("lsblk", "-dnpo", "NAME,MODEL").Output()
	if err != nil {
		return "", nil
	}
	store := ""
	for line := range strings.Lines(string(out)) {
		if strings.Contains(line, "Instance Storage") {
			store = strings.Fields(line)[0]
			break
		}
	}
	if store == "" {
		return "", nil
	}
	devs, _ := exec.Command("lsblk", "-nlpo", "NAME", store).Output()
	mnt := ""
	for dev := range strings.FieldsSeq(string(devs)) {
		// One mount point a line, each its own: lsblk -r joins a device's several with \x0a,
		// which became TMPDIR once. A mount of its own over docker's or containerd's.
		targets, _ := exec.Command("findmnt", "-nro", "TARGET", "-S", dev).Output()
		for t := range strings.FieldsSeq(string(targets)) {
			if mnt == "" && !strings.HasPrefix(t, "/var/lib/") {
				mnt = t
			}
		}
	}
	if mnt == "" {
		sig, _ := exec.Command("sudo", "blkid", "-p", store).Output()
		if len(strings.Fields(string(devs))) == 1 && len(strings.TrimSpace(string(sig))) == 0 {
			mnt = "/mnt/instance-store"
			if err := run("sudo", "mkfs.ext4", "-q", store); err != nil {
				return "", err
			}
			if err := run("sudo", "mkdir", "-p", mnt); err != nil {
				return "", err
			}
			if err := run("sudo", "mount", store, mnt); err != nil {
				return "", err
			}
		}
	}
	if mnt == "" {
		return "", nil
	}
	dir := mnt + "/spin-lab-tmp"
	if err := run("sudo", "install", "-d", "-o", itoa(os.Getuid()), "-g", itoa(os.Getgid()), dir); err != nil {
		return "", err
	}
	return dir, nil
}
