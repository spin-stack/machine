// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// PrefaultRAM is a memfd of sizeMB, every page of it already allocated and zeroed in huge
// pages, for Memory.FD: boot memory whose cost was paid before QEMU was started.
//
// A cold boot touches guest RAM QEMU has only reserved, and each first touch of a 2 MiB
// range makes the host zero a huge page while the vCPU or QEMU waits: ~73 of them a boot,
// ~180 µs each (boot:exits, lab run 43321809471, 2026-11-12) - kernel segments copied at
// QEMU's reset, then the guest's own early allocations. Done here, by whoever starts the
// machine and as far ahead of the boot as it likes, none of that is in the boot.
//
// The whole of it and not a prefix: memblock allocates top-down from the end of RAM and
// the kernel sits at 16 MiB, so a boot touches both ends of its memory and the middle is
// the buddy allocator's choice. What the guest never uses is given back by the balloon's
// free page reporting, which punches holes in the file as it would unmap anonymous memory.
//
// The descriptor is not close-on-exec: it is for a QEMU that boot execs into, which maps
// it through /proc/self/fd. Huge pages are asserted, not hoped for: a memfd in 4 KiB pages
// is a guest whose every TLB miss walks 4 KiB host pages for as long as it runs, which a
// faster first boot does not pay for. Shmem hands out huge pages only when
// /sys/kernel/mm/transparent_hugepage/shmem_enabled allows it (always, within_size, or
// advise, which this asks for); the host default is never.
func PrefaultRAM(sizeMB int) (*os.File, error) {
	size := int64(sizeMB) << 20
	fd, err := unix.MemfdCreate("pc.ram", 0)
	if err != nil {
		return nil, fmt.Errorf("creating a memfd for %d MiB of boot memory: %w", sizeMB, err)
	}
	f := os.NewFile(uintptr(fd), "memfd:pc.ram")
	if err := prefault(fd, size); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func prefault(fd int, size int64) error {
	if err := unix.Ftruncate(fd, size); err != nil {
		return fmt.Errorf("sizing the boot memory's memfd to %d bytes: %w", size, err)
	}
	mem, err := unix.Mmap(fd, 0, int(size), unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return fmt.Errorf("mapping the boot memory's memfd: %w", err)
	}
	defer func() { _ = unix.Munmap(mem) }()
	if err := unix.Madvise(mem, unix.MADV_HUGEPAGE); err != nil {
		return fmt.Errorf("asking for huge pages in the boot memory: %w", err)
	}
	// MADV_POPULATE_WRITE (Linux 5.14) allocates what a write would, without writing.
	if err := unix.Madvise(mem, unix.MADV_POPULATE_WRITE); err != nil {
		return fmt.Errorf("populating the boot memory: %w", err)
	}
	huge, err := shmemPmdMapped(uintptr(unsafe.Pointer(&mem[0])))
	if err != nil {
		return err
	}
	if huge != size {
		policy, _ := os.ReadFile("/sys/kernel/mm/transparent_hugepage/shmem_enabled")
		return fmt.Errorf("%d of the boot memory's %d MiB is in huge pages, and a guest in 4 KiB pages "+
			"is slower for as long as it runs (shmem_enabled: %s)",
			huge>>20, size>>20, strings.TrimSpace(string(policy)))
	}
	return nil
}

// shmemPmdMapped is how much of this process's mapping at addr is shared memory mapped in
// huge pages, by its own account in /proc/self/smaps.
func shmemPmdMapped(addr uintptr) (int64, error) {
	f, err := os.Open("/proc/self/smaps")
	if err != nil {
		return 0, fmt.Errorf("reading this process's mappings: %w", err)
	}
	defer func() { _ = f.Close() }()
	start := strconv.FormatUint(uint64(addr), 16) + "-"
	in := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		// A mapping's header is "start-end perms ..."; its fields are "Name: value kB".
		if !strings.HasSuffix(fields[0], ":") {
			in = strings.HasPrefix(line, start)
			continue
		}
		if in && fields[0] == "ShmemPmdMapped:" && len(fields) >= 2 {
			kb, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				return 0, fmt.Errorf("reading ShmemPmdMapped %q: %w", fields[1], err)
			}
			return kb << 10, nil
		}
	}
	if err := sc.Err(); err != nil {
		return 0, fmt.Errorf("reading this process's mappings: %w", err)
	}
	return 0, fmt.Errorf("no mapping at %#x in /proc/self/smaps", addr)
}
