// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/spin-stack/machine/machine"
)

// digestDir is where boot --tpm keeps the digests of the kernels and initrds it measured, under
// the user's cache directory, or "" when there is none and every boot hashes.
func digestDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "spin-machine", "sha256")
}

// fileDigest is machine.FileSHA256 of path, kept in dir under the file's identity: its device,
// inode, size, modification and change times. The CLI starts one machine and exits, and the lab
// starts it before every boot, so hashing the 42 MB vmlinux each time put +26 ms on the first
// login's clock (#776). A write to the file moves its change time, which no caller can set back,
// and a file replaced is another inode, so an entry that matches is the file it was taken from.
//
// A file whose change time is within a second of now is hashed and not kept, as git's index and
// ccache's inode cache refuse a file too new: the kernel's clock for those times is coarse, and a
// write in the same tick as the one hashed would leave the identity unchanged.
func fileDigest(path, dir string, now time.Time) ([sha256.Size]byte, error) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("reading %s: %w", path, err)
	}
	entry := filepath.Join(dir, fmt.Sprintf("%d-%d", st.Dev, st.Ino))
	identity := fmt.Sprintf("%d %d %d ", st.Size, st.Mtim.Nano(), st.Ctim.Nano())
	if dir != "" {
		if kept, err := os.ReadFile(entry); err == nil { // #nosec G304 -- this user's cache
			if hexSum, ok := strings.CutPrefix(strings.TrimSpace(string(kept)), identity); ok {
				if b, err := hex.DecodeString(hexSum); err == nil && len(b) == sha256.Size {
					return [sha256.Size]byte(b), nil
				}
			}
		}
	}
	sum, err := machine.FileSHA256(path)
	if err != nil {
		return sum, err
	}
	if dir != "" && now.Sub(time.Unix(0, st.Ctim.Nano())) > time.Second {
		// A digest not kept is one hashed again at the next boot, so a cache that cannot be
		// written costs time and never a wrong PCR: the boot goes on.
		_ = keep(entry, identity+hex.EncodeToString(sum[:])+"\n")
	}
	return sum, nil
}

// keep writes line to entry whole, through a rename, so a boot running beside this one reads the
// entry before or after and never half of it.
func keep(entry, line string) error {
	if err := os.MkdirAll(filepath.Dir(entry), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(entry), ".digest-*")
	if err != nil {
		return err
	}
	_, werr := f.WriteString(line)
	if err := errors.Join(werr, f.Close()); err != nil {
		_ = os.Remove(f.Name())
		return fmt.Errorf("writing %s: %w", f.Name(), err)
	}
	return os.Rename(f.Name(), entry)
}
