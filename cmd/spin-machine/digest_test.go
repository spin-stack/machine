// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// later is a now past every file a test writes, so its change time is not too new to keep.
var later = time.Now().Add(time.Hour)

func kernelFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vmlinux")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// forge puts a digest of all 0xff in place of every digest dir keeps, so a test sees which
// answers came from what was kept.
func forge(t *testing.T, dir string) [sha256.Size]byte {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no digest was kept")
	}
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		line, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		fields := strings.Fields(string(line))
		fields[len(fields)-1] = strings.Repeat("ff", sha256.Size)
		if err := os.WriteFile(path, []byte(strings.Join(fields, " ")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var ff [sha256.Size]byte
	for i := range ff {
		ff[i] = 0xff
	}
	return ff
}

func TestTheDigestOfAnUnchangedFileIsTheOneKept(t *testing.T) {
	path, dir := kernelFile(t, "kernel one"), t.TempDir()
	got, err := fileDigest(path, dir, later)
	if err != nil {
		t.Fatal(err)
	}
	if got != sha256.Sum256([]byte("kernel one")) {
		t.Fatalf("first boot's digest %x is not the file's", got)
	}
	forged := forge(t, dir)
	if got, err := fileDigest(path, dir, later); err != nil || got != forged {
		t.Fatalf("second boot hashed the file again (%x, %v): the kept digest was not read", got, err)
	}
}

// Each change a file can go through moves one part of its identity, and each is hashed again.
func TestAChangedFileIsHashedAgain(t *testing.T) {
	for name, change := range map[string]func(t *testing.T, path string){
		"rewritten, the same size": func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("kernel two"), 0o600); err != nil {
				t.Fatal(err)
			}
			old := time.Unix(1_000_000, 0)
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatal(err)
			}
		},
		"grown": func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("kernel two, longer"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		// Only the change time moves: a write whose modification time was set back.
		"its times set back": func(t *testing.T, path string) {
			st, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			time.Sleep(20 * time.Millisecond) // past the coarse clock's tick, so the change time moves
			if err := os.WriteFile(path, []byte("kernel two"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(path, st.ModTime(), st.ModTime()); err != nil {
				t.Fatal(err)
			}
		},
		"replaced by another file": func(t *testing.T, path string) {
			other := path + ".new"
			if err := os.WriteFile(other, []byte("kernel two"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(other, path); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			path, dir := kernelFile(t, "kernel one"), t.TempDir()
			if _, err := fileDigest(path, dir, later); err != nil {
				t.Fatal(err)
			}
			forge(t, dir)
			change(t, path)
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := fileDigest(path, dir, later); err != nil || got != sha256.Sum256(content) {
				t.Fatalf("after the change the digest is %x (%v), not the file's", got, err)
			}
		})
	}
}

func TestAFileChangedWithinASecondIsNotKept(t *testing.T) {
	path, dir := kernelFile(t, "kernel one"), t.TempDir()
	if _, err := fileDigest(path, dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("a file written just now was kept (%d entries, %v): a write in the same tick would not move its identity", len(entries), err)
	}
}

func TestWithNoCacheEveryBootHashes(t *testing.T) {
	path := kernelFile(t, "kernel one")
	if got, err := fileDigest(path, "", later); err != nil || got != sha256.Sum256([]byte("kernel one")) {
		t.Fatalf("digest %x (%v), not the file's", got, err)
	}
}

func TestAKernelThatIsNotThereIsNamed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vmlinux")
	if _, err := fileDigest(path, t.TempDir(), later); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("error %v does not name %s", err, path)
	}
}

// An entry of this file's identity whose digest is not one is hashed past, never trusted.
func TestAKeptDigestThatIsNotOneIsHashedAgain(t *testing.T) {
	for name, kept := range map[string]string{"short": "ffff", "not hex": strings.Repeat("zz", sha256.Size)} {
		t.Run(name, func(t *testing.T) {
			path, dir := kernelFile(t, "kernel one"), t.TempDir()
			if _, err := fileDigest(path, dir, later); err != nil {
				t.Fatal(err)
			}
			forge(t, dir)
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			entry := filepath.Join(dir, entries[0].Name())
			line, err := os.ReadFile(entry)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(entry, []byte(strings.Replace(string(line), strings.Repeat("ff", sha256.Size), kept, 1)), 0o600); err != nil {
				t.Fatal(err)
			}
			if got, err := fileDigest(path, dir, later); err != nil || got != sha256.Sum256([]byte("kernel one")) {
				t.Fatalf("digest %x (%v), not the file's", got, err)
			}
		})
	}
}

func TestTheDigestsLiveInTheUsersCache(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	if got, want := digestDir(), filepath.Join(cache, "spin-machine", "sha256"); got != want {
		t.Errorf("digests kept in %q, want %q", got, want)
	}
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", "")
	if got := digestDir(); got != "" {
		t.Errorf("with no cache directory the digests are kept in %q, want none", got)
	}
}
