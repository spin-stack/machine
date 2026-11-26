// SPDX-License-Identifier: Apache-2.0

package release

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// components is every name and version an SBOM holds, however deep, each name with the versions
// it is given.
func components(t *testing.T, path string) map[string][]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			name, n := v["name"].(string)
			version, ok := v["version"].(string)
			if n && ok {
				got[name] = append(got[name], version)
			}
			for _, c := range v {
				walk(c)
			}
		case []any:
			for _, c := range v {
				walk(c)
			}
		}
	}
	walk(doc)
	return got
}

// The firmware is QEMU's own build of it, at QEMU's version; qboot.bin is built from its own
// commit, and is said once, at that.
func TestEachFileIsAtTheVersionItWasBuiltFrom(t *testing.T) {
	root, out := tree(t)
	o := options(t, root, out)
	if _, err := Pack(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	q, _ := o.Pins.Get("qemu")
	b, _ := o.Pins.Get("qboot")
	k, _ := o.Pins.Get("kernel")
	w, _ := o.Pins.Get("swtpm")
	p, _ := o.Pins.Get("passt")
	got := components(t, filepath.Join(out, "release", Name(o.Version), "usr/share/spin-stack/sbom/machine.cdx.json"))
	for name, want := range map[string]string{"bios.bin": q.Version, "linuxboot_dma.bin": q.Version, "qemu-img": q.Version, "qboot.bin": b.Pin,
		"vmlinux": k.Version, "rootfs.qcow2": o.Version} {
		if v := got[name]; len(v) != 1 || v[0] != want {
			t.Errorf("%s is %v in the SBOM, want once at %s", name, v, want)
		}
	}
	// swtpm is a source and a file of one name: its pin's tarball, and the binary built from it.
	if v := got["swtpm"]; !slices.Equal(v, []string{w.Version, w.Version}) {
		t.Errorf("swtpm is %v in the SBOM, want twice at %s, the source and the binary", v, w.Version)
	}
	if v := got["passt"]; !slices.Equal(v, []string{p.Version, p.Version}) {
		t.Errorf("passt is %v in the SBOM, want twice at %s, the source and the binary", v, p.Version)
	}
	// spin-machine is the machine the SBOM describes and the binary, both at the release's version,
	// the binary made of the modules its build info lists.
	if v := got["spin-machine"]; !slices.Equal(v, []string{o.Version, o.Version}) {
		t.Errorf("spin-machine is %v in the SBOM, want twice at %s, the machine and the binary", v, o.Version)
	}
	// shirudo is a file at the pinned commit and a source at its pin's version, the branch; what it
	// links is at the versions its pins and Ubuntu's archive gave the build.
	sh, _ := o.Pins.Get("shirudo")
	if v := got["shirudo"]; !slices.Equal(v, []string{sh.Pin, sh.Version}) {
		t.Errorf("shirudo is %v in the SBOM, want the binary at %s and the source at %s", v, sh.Pin, sh.Version)
	}
	ssl, _ := o.Pins.Get("openssl")
	tss, _ := o.Pins.Get("tpm2-tss")
	for name, want := range map[string]string{"openssl": ssl.Version, "tpm2-tss": tss.Version, "libelf-dev": "0.194-4"} {
		if v := got[name]; len(v) != 1 || v[0] != want {
			t.Errorf("%s is %v in the SBOM, want once at %s", name, v, want)
		}
	}
	if len(got["golang.org/x/sys"]) != 1 || len(got["stdlib"]) != 1 {
		t.Errorf("spin-machine's modules are not in the SBOM: x/sys %v, stdlib %v", got["golang.org/x/sys"], got["stdlib"])
	}
}
