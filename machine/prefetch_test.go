// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"os"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// prefetch reads a mapped-ram state's data into the page cache and leaves its holes alone: a hole
// read ahead is a page of zeros the cache holds for nothing, and a state is mostly holes.
func TestPrefetchReadsTheDataAndNotTheHoles(t *testing.T) {
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
	for _, off := range []int64{0, 8 * mib} { // data, a hole of 7 MiB, data
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
	prefetch(f)
	deadline := time.Now().Add(10 * time.Second)
	for cached(t, f, 0, mib)+cached(t, f, 8*mib, mib) < 2*mib {
		if time.Now().After(deadline) {
			t.Fatalf("the data was not read ahead: %d and %d bytes cached", cached(t, f, 0, mib), cached(t, f, 8*mib, mib))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := cached(t, f, 2*mib, 4*mib); n != 0 {
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

// A resume reads ahead only a full load of a mapped-ram state: a fast load fetches a page when the
// guest touches it, which reading every page ahead would undo, and a stream is read in order once.
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
		if got := prefetches(Saved{Format: c.format}, LoadOptions{Fast: c.fast}); got != c.want {
			t.Errorf("%s, fast %v: reads ahead %v", c.format, c.fast, got)
		}
	}
}
