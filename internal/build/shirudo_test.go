// SPDX-License-Identifier: Apache-2.0

package build

import (
	"context"
	"debug/elf"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spin-stack/go-tools/versions"
)

// patchText is a format-patch of one file, the line it adds placed after context, with or
// without format-patch's signature and with a note in the message when carried.
func patchText(subject, context, added string, note, signature bool) string {
	s := "From: A <a@example.com>\nDate: Tue, 3 Nov 2026 13:52:42 +0000\nSubject: [PATCH] " + subject + "\n\nWhy.\n"
	if note {
		s += "[ spin-stack/machine: " + shirudoNote + ", at\n  versions.yaml's shirudo pin. ]\n"
	}
	s += "---\n f.c | 1 +\n\ndiff --git a/f.c b/f.c\nindex 1111111..2222222 100644\n--- a/f.c\n+++ b/f.c\n@@ -10,2 +10,3 @@\n " + context + "\n+" + added + "\n };\n"
	if signature {
		s += "-- \n2.53.0\n"
	}
	return s
}

func mustParse(t *testing.T, file, text string) kernelPatch {
	t.Helper()
	p, err := parseKernelPatch(file, text)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// A carried patch is shirudo's when its change is, wherever its context sits and whether or not
// format-patch signed it; anything else needs a person, named.
func TestACarriedPatchIsComparedWithShirudosBySubject(t *testing.T) {
	declined := ""
	for s := range shirudoDeclined {
		declined = s
	}
	upstream := []kernelPatch{
		mustParse(t, "0020-a.patch", patchText("tpm: a", "struct inode *d_inode;", "x;", false, false)),
		mustParse(t, "0002-k.patch", patchText(declined, "c", "y;", false, true)),
	}
	moved := mustParse(t, "0027-a.patch", strings.NewReplacer("@@ -10,2 +10,3 @@", "@@ -99,2 +99,3 @@", "index 1111111..2222222", "index 3333333..4444444").
		Replace(patchText("tpm: a", "struct inode *d_inode;", "x;", true, true)))
	for _, tc := range []struct {
		name     string
		upstream []kernelPatch
		carried  []kernelPatch
		says     []string
		fails    []string
	}{
		{name: "the same change, signed and moved", upstream: upstream, carried: []kernelPatch{moved},
			says: []string{"0027-a.patch: as shirudo's 0020-a.patch", "shirudo's 0002-k.patch: not carried"}},
		{name: "a change upstream rewrote", upstream: upstream,
			carried: []kernelPatch{mustParse(t, "0027-a.patch", patchText("tpm: a", "struct inode *d_inode;", "z;", true, true))},
			fails:   []string{"0027-a.patch: shirudo's 0020-a.patch changes it"}},
		{name: "a patch upstream dropped", upstream: upstream[1:], carried: []kernelPatch{moved},
			fails: []string{`0027-a.patch: shirudo at pin no longer has "tpm: a"`}},
		{name: "an upstream patch nobody decided", carried: []kernelPatch{moved},
			upstream: append(slices.Clone(upstream), mustParse(t, "0025-new.patch", patchText("bpf: new", "c", "n;", false, false))),
			fails:    []string{"shirudo's 0025-new.patch: neither carried nor declined"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := compareShirudo(tc.upstream, tc.carried, "pin")
			for _, s := range tc.says {
				if !strings.Contains(out, s) {
					t.Errorf("says\n%s\nwant %q", out, s)
				}
			}
			if len(tc.fails) == 0 && err != nil {
				t.Fatalf("fails: %v", err)
			}
			for _, s := range tc.fails {
				if err == nil || !strings.Contains(err.Error(), s) {
					t.Errorf("fails with %v, want %q", err, s)
				}
			}
		})
	}
}

// The real path: shirudo's repository fetched at the pinned commit, kernel/patches read for the
// patches whose note says they are its.
func TestShirudoPatchesFetchesThePinnedCommit(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("config", "uploadpack.allowAnySHA1InWant", "true")
	if err := os.MkdirAll(filepath.Join(repo, shirudoPatches), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, shirudoPatches, "0020-a.patch"), []byte(patchText("tpm: a", "c", "x;", false, false)), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-qm", "patches")
	commit := git("rev-parse", "HEAD")

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "kernel", "patches"), 0o750); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		"0001-ours.patch": patchText("x86: ours", "c", "o;", false, true),
		"0027-a.patch":    patchText("tpm: a", "c", "x;", true, true),
	} {
		if err := os.WriteFile(filepath.Join(root, "kernel", "patches", name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	v := pins(t)
	w := *v
	w.Entries = slices.Clone(v.Entries)
	for i := range w.Entries {
		if w.Entries[i].Name == "shirudo" {
			w.Entries[i].Source, w.Entries[i].Pin = repo, commit
		}
	}
	out, err := ShirudoPatches(context.Background(), root, &w)
	if err != nil || out != "0027-a.patch: as shirudo's 0020-a.patch" {
		t.Errorf("ShirudoPatches = %q, %v", out, err)
	}
	for i := range w.Entries {
		if w.Entries[i].Name == "shirudo" {
			w.Entries[i].Pin = strings.Repeat("0", 40)
		}
	}
	if _, err := ShirudoPatches(context.Background(), root, &w); err == nil || !strings.Contains(err.Error(), "fetching shirudo") {
		t.Errorf("a commit the repository does not have: %v", err)
	}
}

// A patch with no Subject or no diff is not a patch to compare.
func TestAKernelPatchHasASubjectAndADiff(t *testing.T) {
	if _, err := parseKernelPatch("p", "diff --git a/f b/f\n+x\n"); err == nil {
		t.Error("no Subject")
	}
	if _, err := parseKernelPatch("p", "Subject: [PATCH] s\n\nno diff\n"); err == nil {
		t.Error("no diff")
	}
}

// shirudoTree lays out what the shirudo part leaves in out: bin as the guest's shirudo and the
// operator's shirudo-build, their licences and packages, a clang of the pinned LLVM and the rest of
// the operator half.
func mustCopy(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, 0o755); err != nil {
		t.Fatal(err)
	}
}

func shirudoTree(t *testing.T, out, bin string, v *versions.Versions) {
	t.Helper()
	b, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	llvm, _ := v.Get("llvm")
	op := func(p string) string { return filepath.Join(ShirudoOperator, p) }
	for f, content := range map[string][]byte{
		ShirudoBinary: b, ShirudoLicenses: []byte("licences"), ShirudoPackages: []byte("libc6-dev 2.43 amd64\n"),
		ShirudoOperatorPackages: []byte("dracut-core 110 amd64\n"),
		op(ShirudoBuild):        b, op(ShirudoClang): []byte("#!/bin/sh\necho " + llvm.Version + "\n"),
		op("install"): []byte("#!/bin/sh\n"), op("licenses.txt"): []byte("licences"),
		op("debs/Packages"): []byte("Package: dracut-core\n"), op("debs/wanted"): []byte("dracut-core\n"),
		op("libexec/shirudo/gen-initrd/shirudo-gen-initrd"): []byte("#!/bin/sh\n"),
		op("share/shirudo/vmlinux.h"):                       []byte("struct x;\n"),
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(out, f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, f), content, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// The shirudo a release ships is a static binary that names the pinned commit, with its licences
// and the packages it links beside it; anything else is refused, naming what is wrong.
func TestShirudoVerifiesAsAStaticBinaryOfThePin(t *testing.T) {
	v := pins(t)
	e, _ := v.Get("shirudo")
	named := func(t *testing.T, progs []elf.Prog64, commit string) string {
		t.Helper()
		path := writeELF(t, progs)
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.WriteString("shirudo version " + commit + "\n")
		_ = f.Close()
		return path
	}
	load := []elf.Prog64{{Type: uint32(elf.PT_LOAD)}}
	out := t.TempDir()
	shirudoTree(t, out, named(t, load, e.Pin[:7]), v)
	o := Options{Out: out, Pins: v}
	if err := VerifyShirudo(o); err != nil {
		t.Errorf("a static shirudo of the pin: %v", err)
	}
	for name, want := range map[string]string{
		ShirudoLicenses: "missing " + ShirudoLicenses, ShirudoPackages: "missing " + ShirudoPackages,
		ShirudoOperatorPackages:                       "missing " + ShirudoOperatorPackages,
		filepath.Join(ShirudoOperator, "install"):     "missing " + filepath.Join(ShirudoOperator, "install"),
		filepath.Join(ShirudoOperator, "debs/wanted"): "missing " + filepath.Join(ShirudoOperator, "debs/wanted"),
		filepath.Join(ShirudoOperator, ShirudoClang):  "missing clang",
		filepath.Join(ShirudoOperator, ShirudoBuild):  "missing shirudo-build",
	} {
		out := t.TempDir()
		shirudoTree(t, out, named(t, load, e.Pin[:7]), v)
		_ = os.Remove(filepath.Join(out, name))
		if err := VerifyShirudo(Options{Out: out, Pins: v}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("no %s: %v", name, err)
		}
	}
	for what, c := range map[string]struct {
		bin, want string
	}{
		"a dynamic shirudo":      {named(t, append(load, elf.Prog64{Type: uint32(elf.PT_INTERP)}), e.Pin[:7]), "is linked dynamically"},
		"another commit's":       {named(t, load, "0000000"), "built from another commit"},
		"a shirudo that is none": {filepath.Join(t.TempDir(), "none"), "missing shirudo"},
	} {
		out := t.TempDir()
		if _, err := os.Stat(c.bin); err == nil {
			shirudoTree(t, out, c.bin, v)
		}
		if err := VerifyShirudo(Options{Out: out, Pins: v}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", what, err, c.want)
		}
	}
	dynamic := t.TempDir()
	shirudoTree(t, dynamic, named(t, load, e.Pin[:7]), v)
	mustCopy(t, named(t, append(load, elf.Prog64{Type: uint32(elf.PT_INTERP)}), e.Pin[:7]), filepath.Join(dynamic, ShirudoOperator, ShirudoBuild))
	if err := VerifyShirudo(Options{Out: dynamic, Pins: v}); err == nil || !strings.Contains(err.Error(), "is linked dynamically") {
		t.Errorf("a dynamic shirudo-build: %v", err)
	}
	other := t.TempDir()
	shirudoTree(t, other, named(t, load, e.Pin[:7]), v)
	if err := os.WriteFile(filepath.Join(other, ShirudoOperator, ShirudoClang), []byte("#!/bin/sh\necho 22.1.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := VerifyShirudo(Options{Out: other, Pins: v}); err == nil || !strings.Contains(err.Error(), "is clang 22.1.0") {
		t.Errorf("another clang: %v", err)
	}
	if err := os.WriteFile(filepath.Join(other, ShirudoOperator, ShirudoClang), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := VerifyShirudo(Options{Out: other, Pins: v}); err == nil || !strings.Contains(err.Error(), "clang -dumpversion") {
		t.Errorf("a clang that does not run: %v", err)
	}
	if err := VerifyShirudo(Options{Out: out, Pins: pinsWithout(t, "llvm")}); err == nil {
		t.Error("no llvm pin gave no error")
	}
	if err := VerifyShirudo(Options{Out: out, Pins: pinsWithout(t, "shirudo")}); err == nil {
		t.Error("no shirudo pin gave no error")
	}
}
