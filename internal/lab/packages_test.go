// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// A package put into an overlay leaves /usr's mtime as the image's update stamps hold it, so the
// units of ConditionNeedsUpdate stay skipped in the boots measured (#513, #637).
func TestAPackageInstalledKeepsUsrsMtime(t *testing.T) {
	if _, err := exec.LookPath("dpkg-deb"); err != nil {
		t.Skip("no dpkg-deb here")
	}
	pkg := t.TempDir()
	write(t, filepath.Join(pkg, "DEBIAN/control"), []byte("Package: p\nVersion: 1\nArchitecture: all\nMaintainer: m <m@x>\nDescription: d\n"))
	write(t, filepath.Join(pkg, "usr/bin/p"), []byte("new"))
	debs := t.TempDir()
	if out, err := exec.Command("dpkg-deb", "--root-owner-group", "--build", pkg, filepath.Join(debs, "p.deb")).CombinedOutput(); err != nil {
		t.Fatalf("dpkg-deb --build: %v: %s", err, out)
	}
	root := t.TempDir()
	write(t, filepath.Join(root, "usr/bin/p"), []byte("old"))
	write(t, filepath.Join(root, "var/lib/dpkg/info/p.list"), []byte("/usr\n/usr/bin\n/usr/bin/p\n"))
	stamp := time.Date(2026, 11, 23, 1, 2, 3, 456789000, time.UTC)
	if err := os.Chtimes(filepath.Join(root, "usr"), stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if err := setUp(t, packagesEdit(debs, "true"), root); err != nil {
		t.Fatal(err)
	}
	if b := read(t, filepath.Join(root, "usr/bin/p")); b != "new" {
		t.Errorf("the overlay's /usr/bin/p is %q, not the package's", b)
	}
	fi, err := os.Stat(filepath.Join(root, "usr"))
	if err != nil {
		t.Fatal(err)
	}
	if !fi.ModTime().Equal(stamp) {
		t.Errorf("/usr's mtime is %v after the package, %v before", fi.ModTime(), stamp)
	}
}
