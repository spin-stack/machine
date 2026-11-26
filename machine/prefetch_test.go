// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// ReadAhead reads a mapped-ram state's data into the page cache and leaves its holes alone: a hole
// read ahead is a page of zeros the cache holds for nothing, and a state is mostly holes. For a
// fast load it reads nothing.
func TestReadAheadReadsTheDataAndNotTheHoles(t *testing.T) {
	dir := t.TempDir()
	var fs unix.Statfs_t
	if err := unix.Statfs(dir, &fs); err != nil {
		t.Fatal(err)
	}
	if fs.Type == unix.TMPFS_MAGIC {
		t.Skip("the temporary directory is tmpfs, whose pages are never out of the cache")
	}
	const mib = 1 << 20
	f, err := os.Create(dir + "/state")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	data := make([]byte, mib)
	for i := range data {
		data[i] = 1
	}
	const short = mib + 64<<10
	for _, off := range []int64{0, short, 8 * mib} { // data, a hole of 64 KiB, data, a hole of ~6 MiB, data
		if _, err := f.WriteAt(data, off); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := unix.Fadvise(int(f.Fd()), 0, 0, unix.FADV_DONTNEED); err != nil {
		t.Fatal(err)
	}
	if cached(t, f, 0, mib) == mib {
		t.Skip("this filesystem keeps the state in the cache after DONTNEED")
	}
	loadingFast(t, true)
	ReadAhead(f, Saved{Format: FormatMappedRAM})
	time.Sleep(100 * time.Millisecond)
	if n := cached(t, f, 0, mib); n != 0 {
		t.Errorf("a fast load's state was read ahead: %d bytes cached", n)
	}
	loadingFast(t, false)
	ReadAhead(f, Saved{Format: FormatMappedRAM})
	deadline := time.Now().Add(10 * time.Second)
	for cached(t, f, 0, mib)+cached(t, f, short, mib)+cached(t, f, 8*mib, mib) < 3*mib {
		if time.Now().After(deadline) {
			t.Fatalf("the data was not read ahead: %d, %d and %d bytes cached", cached(t, f, 0, mib), cached(t, f, short, mib), cached(t, f, 8*mib, mib))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := cached(t, f, 3*mib, 4*mib); n != 0 {
		t.Errorf("%d bytes of the hole were read ahead", n)
	}
}

// cached is how many bytes of f's [off, off+n) are in the page cache.
func cached(t *testing.T, f *os.File, off, n int64) int64 {
	t.Helper()
	m, err := unix.Mmap(int(f.Fd()), off, int(n), unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Munmap(m) }()
	page := int64(os.Getpagesize())
	vec := make([]byte, (n+page-1)/page)
	if _, _, errno := unix.Syscall(unix.SYS_MINCORE, uintptr(unsafe.Pointer(&m[0])), uintptr(n), uintptr(unsafe.Pointer(&vec[0]))); errno != 0 {
		t.Fatal(errno)
	}
	var in int64
	for _, v := range vec {
		in += int64(v&1) * page
	}
	return in
}

// A resume reads ahead only a full load of a mapped-ram state: a stream is read in order once,
// and a fast load's guest faults pages in faster than a read in the file's order reaches them.
func TestOnlyAFullLoadOfAMappedRAMStateIsReadAhead(t *testing.T) {
	for _, c := range []struct {
		format string
		fast   bool
		want   bool
	}{
		{FormatMappedRAM, false, true},
		{FormatMappedRAM, true, false},
		{FormatStream, false, false},
	} {
		if got := prefetches(Saved{Format: c.format}, c.fast); got != c.want {
			t.Errorf("%s, fast %v: reads ahead %v", c.format, c.fast, got)
		}
	}
}

// A mapped-ram state loads fast only when its data is over fastAbove; a stream state, which has no
// page at a fixed offset, never does.
func TestALargeStateLoadsFast(t *testing.T) {
	for _, c := range []struct {
		format string
		data   int64
		want   bool
	}{
		{FormatMappedRAM, fastAbove + 1, true},
		{FormatMappedRAM, fastAbove, false},
		{FormatStream, fastAbove + 1, false},
	} {
		if got := fastLoad(c.format, c.data); got != c.want {
			t.Errorf("%s, %d bytes of data: fast %v, want %v", c.format, c.data, got, c.want)
		}
	}
}

// A state that would load fast loads fast only with the userfaultfd device in hand, which is then
// open read-write to hand QEMU: where the device does not open, it loads in full.
func TestAFastLoadIsTheDeviceInHand(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct {
		name, dev string
		fast      bool
		want      bool
	}{
		{"a device this process opens read-write", os.DevNull, true, true},
		{"no device", filepath.Join(dir, "none"), true, false},
		{"a directory, which nothing opens for writing", dir, true, false},
		{"a state that loads in full, which opens nothing", filepath.Join(dir, "none"), false, false},
	} {
		loadingFast(t, c.fast)
		userfaultfdAt(t, c.dev)
		f := userfaultfd(nil, Saved{Format: FormatMappedRAM})
		if got := f != nil; got != c.want {
			t.Errorf("%s: fast %v, want %v", c.name, got, c.want)
			continue
		}
		if f == nil {
			continue
		}
		fl, err := unix.FcntlInt(f.Fd(), unix.F_GETFL, 0)
		_ = f.Close()
		if err != nil || fl&unix.O_ACCMODE != unix.O_RDWR {
			t.Errorf("%s: the device is open with flags %#o (%v), want read-write, which QEMU opens it with", c.name, fl, err)
		}
	}
}

// loadingFast has every resume in t load fast, or none, whatever its state and host: a fast one
// with /dev/null for the userfaultfd device, which every process opens.
func loadingFast(t *testing.T, fast bool) {
	t.Helper()
	was := loadsFast
	loadsFast = func(*os.File, Saved) bool { return fast }
	t.Cleanup(func() { loadsFast = was })
	userfaultfdAt(t, os.DevNull)
}

// userfaultfdAt has the resumes in t open dev for the userfaultfd device.
func userfaultfdAt(t *testing.T, dev string) {
	t.Helper()
	was := userfaultfdDevice
	userfaultfdDevice = dev
	t.Cleanup(func() { userfaultfdDevice = was })
}
