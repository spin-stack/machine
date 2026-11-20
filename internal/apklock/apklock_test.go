// SPDX-License-Identifier: Apache-2.0

package apklock

import "testing"

// A lock is every installed package, name=version, sorted; a package with half its record is no
// lock, and neither is none.
func TestALockIsEveryInstalledPackage(t *testing.T) {
	db := "C:Q1abc=\nP:zlib-static\nV:1.3.1-r2\nA:x86_64\n\nP:glib-static\nV:2.88.1-r1\n\n"
	got, err := Lock(db)
	if err != nil || got != "glib-static=2.88.1-r1\nzlib-static=1.3.1-r2\n" {
		t.Fatalf("lock %q, %v", got, err)
	}
	for name, db := range map[string]string{
		"no version":          "P:glib\n",
		"no name":             "V:1\n",
		"nothing":             "\n",
		"one whole, one half": "P:musl\nV:1.2.5-r10\n\nP:glib\n",
	} {
		if _, err := Lock(db); err == nil {
			t.Errorf("%s: locked", name)
		}
	}
}
