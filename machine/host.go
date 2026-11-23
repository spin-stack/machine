// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
)

// Files are the files QEMU opens by name for s, absolute as s names them: its binary, the
// kernel, the initrd when s has one, and from Firmware the BIOS, linuxboot_dma.bin and
// kvmvapic.bin. Start hands QEMU everything else as a descriptor, so a root that holds these at
// the same paths, and a /dev/null, starts the machine. QEMU opens /dev/null to ask whether the
// host has OFD locks, and without it locks its images with POSIX locks, which a close of any of
// its descriptors of the file drops.
//
// Nothing else is opened by name. A disk, a checkpoint's state and the console are
// descriptors. QEMU reads /proc, /sys and /dev/urandom when they are there (strace of a boot,
// 2026-11-21), and starts and runs without them:
// TestAMachineRunsInARootOfItsFilesWithinItsOverhead runs spin's shape to its ceiling and through
// two checkpoints in a root of these files and nothing more.
//
// The other two files in Firmware are for machines s does not make. efi-virtio.rom is a NIC's
// option ROM, and Args gives each NIC romfile= so that none is loaded. bios.bin and
// bios-256k.bin are SeaBIOS, which a Spec runs only when BIOS names one.
func (s Spec) Files() []string {
	files := []string{s.QEMU, s.Kernel}
	if s.Initrd != "" {
		files = append(files, s.Initrd)
	}
	for _, f := range []string{s.bios(), kernelROM(s.Kernel), vapicROM} {
		files = append(files, filepath.Join(s.Firmware, f))
	}
	return files
}

// vapicROM is the option ROM of the APIC's TPR patching, which QEMU loads for every x86
// machine (apic_common_realize).
const vapicROM = "kvmvapic.bin"

// Overhead is what a machine's QEMU uses beyond its guest's RAM, at most.
type Overhead struct {
	// Memory is the bytes QEMU's cgroup holds beyond the guest's RAM at its ceiling
	// (Memory.CeilingMB, or BaseMB without one), counting nothing the host can reclaim: QEMU's
	// own anonymous memory, and the kernel's memory charged to it, its page tables and KVM's
	// among them. The files QEMU reads, its images' pages in the host's cache, are not in it.
	Memory int64
	// Tasks is the threads QEMU's cgroup holds at most: QEMU's, and the kernel's workers that
	// run in its process (vhost, io_uring, KVM's huge page recovery).
	Tasks int
}

// Overhead is what a machine of s uses beyond its guest's RAM, for a cgroup to bound it by:
// memory.max at the guest's ceiling plus Overhead.Memory and each disk's Disk.Overhead, pids.max
// at Overhead.Tasks. It reads the sizes of the kernel and the initrd, which QEMU keeps a copy of
// in its own memory.
//
// Measured with task boot:overhead (laptop, KVM, v20261121.01, 2026-11-21), spin's shape with a
// NIC and a vsock held 31 MiB beyond its guest's RAM, and 9 tasks, booted and sized to 2048 MiB.
// At its ceiling it held 73 MiB and 27 tasks while the guest read, wrote and trimmed every disk,
// with 16 vCPUs, 8192 MiB filled, and four disks attached beside its two, one of them 64 GiB. While
// it was checkpointed there it held 79 MiB and 34 tasks. Of the 73 MiB, 27 were page tables (the
// host's and KVM's), 16 the bzImage, and 8 the 64 GiB disk's L2 cache. Overhead for that machine,
// its disks included, is 153 MiB and 223 tasks.
func (s Spec) Overhead() (Overhead, error) {
	files := []string{s.Kernel}
	if s.Initrd != "" {
		files = append(files, s.Initrd)
	}
	var loaded int64
	for _, f := range files {
		st, err := os.Stat(f)
		if err != nil {
			return Overhead{}, fmt.Errorf("sizing what QEMU keeps of %s: %w", f, err)
		}
		loaded += st.Size()
	}
	cpus := max(s.BootCPUs, s.MaxCPUs)
	tasks := cpus + qemuThreads + len(s.NICs) + DefaultChannels + 1 + threadPoolMax + ioWorkersMax
	if s.VsockCID != 0 {
		tasks++
	}
	ceiling := int64(max(s.Memory.BaseMB, s.Memory.CeilingMB)) << 20
	return Overhead{
		Memory: qemuMemory + loaded + int64(cpus)*cpuMemory + int64(tasks)*taskMemory + ceiling/pageTableShare,
		Tasks:  tasks,
	}, nil
}

// Overhead is the memory QEMU holds for d's images beyond what Spec.Overhead counts: each qcow2
// image's L2 and refcount caches at QEMU's defaults (read_cache_sizes in block/qcow2.c), which
// grow with the image. The L2 cache holds the image's whole L2 table, up to 32 MiB: one 8-byte
// entry (16 with extended L2) per cluster, 8 MiB for 64 GiB of 64 KiB clusters. The refcount
// cache holds four clusters. A raw image has neither. Overhead reads each qcow2 image's header
// through its File.
func (d Disk) Overhead() (int64, error) {
	var total int64
	for _, img := range d.Images {
		if img.Format != "qcow2" {
			continue
		}
		head := make([]byte, v3HeaderLength)
		n, err := img.File.ReadAt(head, 0)
		if n < v2HeaderLength {
			return 0, fmt.Errorf("sizing what QEMU caches of %s: %d bytes of qcow2 header: %w", img.File.Name(), n, err)
		}
		bits := binary.BigEndian.Uint32(head[20:])
		if bits < 9 || bits > 21 {
			return 0, fmt.Errorf("sizing what QEMU caches of %s: clusters of 2^%d bytes, not 2^9 to 2^21", img.File.Name(), bits)
		}
		cluster := int64(1) << bits
		size := int64(binary.BigEndian.Uint64(head[24:])) // #nosec G115 -- a size the guest sees; QEMU bounds it far below 2^63
		entry := int64(8)
		if binary.BigEndian.Uint32(head[4:]) == 3 {
			if n < v3HeaderLength {
				return 0, fmt.Errorf("sizing what QEMU caches of %s: %d bytes of a version 3 header: %w", img.File.Name(), n, err)
			}
			if binary.BigEndian.Uint64(head[72:])&extendedL2 != 0 {
				entry = 16
			}
		}
		l2 := (size + cluster - 1) / cluster * entry
		l2 = min(max((l2+cluster-1)/cluster*cluster, 2*cluster), l2CacheMax) // mutate-exempt: l2 is a multiple of 8 and a cluster of 512 or more, so rounding up by cluster-1 or cluster-2 is the same
		total += l2 + 4*cluster
	}
	return total, nil
}

const (
	// extendedL2 is the incompatible feature bit of an image with 16-byte L2 entries.
	extendedL2 = 1 << 4
	// l2CacheMax is QEMU's DEFAULT_L2_CACHE_MAX_SIZE on Linux (block/qcow2.h).
	l2CacheMax = 32 << 20
)

// What Spec.Overhead counts. The threads are those of a QEMU of this machine by name, and the
// most that QEMU and the kernel let each kind grow to; the memory is what the lane measured, with
// room.
const (
	// qemuThreads are QEMU's own that every machine has: its main loop, call_rcu, the monitors'
	// I/O thread (mon_iothread), and KVM's huge page recovery (kvm-nx-lpage-recovery), which the
	// kernel runs in QEMU's process. Each vCPU has a thread, each vhost device (the vsock and each
	// NIC) a worker, and a checkpoint or a load one thread per channel and one to drive them: a
	// caller that asks for more channels than DefaultChannels adds the difference.
	qemuThreads = 4
	// threadPoolMax is QEMU's thread pool for the block requests io_uring does not take, a
	// discard among them (THREAD_POOL_MAX_THREADS_DEFAULT, include/block/thread-pool.h). A
	// thread it cannot start aborts QEMU.
	threadPoolMax = 64
	// ioWorkersMax is the io_uring workers of the main loop's ring, which the kernel starts for
	// requests that would block, in QEMU's process: at most the ring's 128 entries
	// (FDMON_IO_URING_ENTRIES, util/fdmon-io_uring.c), and at most 4 per host CPU
	// (io_init_wq_offload). The lane saw 3.
	ioWorkersMax = 128

	// qemuMemory is QEMU's own memory beyond what the terms below count: at the ceiling the lane
	// measured about 20 MiB of it, and 26 while it was checkpointed.
	qemuMemory = 64 << 20
	// cpuMemory is a vCPU's: QEMU's state of it and KVM's. 15 vCPUs plugged with four disks
	// came to 5 MiB.
	cpuMemory = 1 << 20
	// taskMemory is a thread's stacks, its kernel stack and what QEMU touched of its own.
	taskMemory = 64 << 10
	// pageTableShare is the guest's RAM over the page tables that map it, at 4 KiB pages: 8 bytes
	// a page in the host's, as many in KVM's. 8 GiB filled took 16 MiB and 11-16, with transparent
	// huge pages on madvise; huge pages take fewer.
	pageTableShare = 256
)
