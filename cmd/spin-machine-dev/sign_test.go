// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spin-stack/go-tools/versions"

	"github.com/spin-stack/machine/internal/build"
	"github.com/spin-stack/machine/internal/modsign"
)

// signingTree is a checkout with the series' config and none of the rest, and an _output whose
// kernel left two unsigned modules.
func signingTree(t *testing.T) workspace {
	t.Helper()
	v, err := versions.Load(filepath.Join("..", "..", versions.File))
	if err != nil {
		t.Fatal(err)
	}
	series, err := build.KernelSeries(v)
	if err != nil {
		t.Fatal(err)
	}
	w := workspace{root: t.TempDir(), out: t.TempDir(), pins: v}
	files := map[string]string{
		filepath.Join(w.root, "kernel", "config-"+series+"-"+build.Arch):      "CONFIG_SYSTEM_TRUSTED_KEYRING=y\nCONFIG_SYSTEM_TRUSTED_KEYS=\"\"\n",
		filepath.Join(w.out, "kernel/modules/7.4.0/kernel/fs/fuse/fuse.ko"):   "fuse",
		filepath.Join(w.out, "kernel/modules/7.4.0/kernel/fs/erofs/erofs.ko"): "erofs",
	}
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

// The certificate is written once and named in the kernel's config, which is then what
// kernel/Dockerfile builds in. Made again for the same key it changes nothing; made for another key
// it is refused, as a new key is a new kernel.
func TestTheModuleCertificateIsWrittenOnceAndNamedInTheConfig(t *testing.T) {
	w := signingTree(t)
	ours, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeModuleCertificate(w, ours); err != nil {
		t.Fatal(err)
	}
	cert, err := build.ReadModuleCertificate(w.root)
	if err != nil {
		t.Fatalf("the certificate written does not read back: %v", err)
	}
	if !cert.PublicKey.(*rsa.PublicKey).Equal(&ours.PublicKey) {
		t.Error("the certificate is not the key's")
	}
	series, _ := build.KernelSeries(w.pins)
	config, _ := os.ReadFile(filepath.Join(w.root, "kernel", "config-"+series+"-"+build.Arch))
	if !strings.Contains(string(config), "\nCONFIG_SYSTEM_TRUSTED_KEYS=\"certs/module-signing.crt\"\n") ||
		!strings.HasPrefix(string(config), "CONFIG_SYSTEM_TRUSTED_KEYRING=y\n") {
		t.Errorf("the config reads %q, want SYSTEM_TRUSTED_KEYS naming the certificate and nothing else changed", config)
	}
	if err := writeModuleCertificate(w, ours); err != nil {
		t.Errorf("the same key's certificate made again: %v", err)
	}
	theirs, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeModuleCertificate(w, theirs); err == nil {
		t.Error("another key's certificate replaced the tree's")
	}
}

// A release signs every module against the tree's certificate, once: a tree signed already asks
// the key for nothing. With no certificate, or with a key that is not the certificate's, nothing is
// signed, as the kernel would load none of it.
func TestAReleaseSignsItsModulesAgainstTheTreesCertificate(t *testing.T) {
	w := signingTree(t)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	opened := 0
	open := func(context.Context) (crypto.Signer, error) { opened++; return key, nil }
	if _, err := signModules(t.Context(), w, open); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("modules were signed with no certificate in the tree: %v", err)
	}
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeModuleCertificate(w, other); err != nil {
		t.Fatal(err)
	}
	if _, err := signModules(t.Context(), w, open); err == nil {
		t.Fatal("modules were signed by a key that is not the certificate's")
	}
	_ = os.Remove(filepath.Join(w.root, build.ModuleCertificate))
	if err := writeModuleCertificate(w, key); err != nil {
		t.Fatal(err)
	}
	opened = 0
	n, err := signModules(t.Context(), w, open)
	if err != nil || n != 2 {
		t.Fatalf("signModules = %d, %v, want both modules signed", n, err)
	}
	cert, _ := build.ReadModuleCertificate(w.root)
	if err := modsign.VerifyTree(filepath.Join(w.out, "kernel", "modules"), cert); err != nil {
		t.Fatal(err)
	}
	broken := func(context.Context) (crypto.Signer, error) { return nil, errors.New("no AWS credentials") }
	_ = os.WriteFile(filepath.Join(w.out, "kernel/modules/7.4.0/kernel/fs/fuse/fuse.ko"), []byte("rebuilt"), 0o644)
	if _, err := signModules(t.Context(), w, broken); err == nil || !strings.Contains(err.Error(), "no AWS credentials") {
		t.Errorf("a key that cannot be opened: %v", err)
	}
	if _, err := signModules(t.Context(), w, open); err == nil {
		t.Error("a tree with one module signed already was signed again")
	}
	_ = os.WriteFile(filepath.Join(w.out, "kernel/modules/7.4.0/kernel/fs/erofs/erofs.ko"), []byte("rebuilt"), 0o644)
	if _, err := signModules(t.Context(), w, open); err != nil {
		t.Fatal(err)
	}
	opened = 0
	if n, err := signModules(t.Context(), w, open); err != nil || n != 0 || opened != 0 {
		t.Errorf("a signed tree: signModules = %d, %v, key opened %d times, want nothing asked of it", n, err, opened)
	}
}

// What certificate wrote is committed on a branch of the run's own and proposed against main, the
// certificate and the config naming it; a tree that has them committed proposes nothing.
func TestTheCertificateIsProposedOnceWithItsConfig(t *testing.T) {
	dir := builderCheckout(t)
	t.Setenv("GITHUB_RUN_ID", "77")
	w, err := open()
	if err != nil {
		t.Fatal(err)
	}
	series, _ := build.KernelSeries(w.pins)
	config := filepath.Join("kernel", "config-"+series+"-"+build.Arch)
	put(t, filepath.Join(dir, config), "CONFIG_SYSTEM_TRUSTED_KEYS=\"\"\n", 0o644)
	for _, args := range [][]string{{"add", config}, {"commit", "-q", "-m", "config"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	calls := fakes(t, map[string]string{"gh": `[ "$1 $2" = "pr create" ] && echo https://x/pull/5`})
	if _, stderr, status := verb(t, "certificate", "propose"); status != 0 || !strings.Contains(stderr, "already") {
		t.Errorf("nothing written: exit %d, %q", status, stderr)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeModuleCertificate(w, key); err != nil {
		t.Fatal(err)
	}
	if _, stderr, status := verb(t, "certificate", "propose"); status != 0 || !strings.Contains(stderr, "https://x/pull/5") {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	if got := read(t, calls); !strings.Contains(got, "gh pr create --base main --head kernel/module-certificate-77") {
		t.Errorf("gh ran %q", got)
	}
	out, err := exec.Command("git", "-C", dir, "show", "--name-only", "--format=%s", "origin/kernel/module-certificate-77").CombinedOutput()
	if err != nil || string(out) != "kernel: the module signing certificate\n\n"+config+"\n"+build.ModuleCertificate+"\n" {
		t.Errorf("the branch proposed holds %q, %v", out, err)
	}
}

// certificate makes the certificate of the key it opens, and says a key it cannot open, as on any
// machine but a release environment's, could not make it.
func TestCertificateMakesTheKeysCertificate(t *testing.T) {
	dir := checkout(t)
	w, err := open()
	if err != nil {
		t.Fatal(err)
	}
	series, _ := build.KernelSeries(w.pins)
	put(t, filepath.Join(dir, "kernel", "config-"+series+"-"+build.Arch), "CONFIG_SYSTEM_TRUSTED_KEYS=\"\"\n", 0o644)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	opened := openModuleKey
	t.Cleanup(func() { openModuleKey = opened })
	openModuleKey = func(context.Context) (crypto.Signer, error) { return nil, errors.New("no AWS credentials") }
	if _, stderr, status := verb(t, "certificate"); status != 2 || !strings.Contains(stderr, "no AWS credentials") {
		t.Errorf("no key: exit %d, %q", status, stderr)
	}
	openModuleKey = func(context.Context) (crypto.Signer, error) { return key, nil }
	if _, stderr, status := verb(t, "certificate"); status != 0 || !strings.Contains(stderr, build.ModuleCertificate) {
		t.Fatalf("exit %d: %s", status, stderr)
	}
	if cert, err := build.ReadModuleCertificate(dir); err != nil || !cert.PublicKey.(*rsa.PublicKey).Equal(&key.PublicKey) {
		t.Errorf("the certificate written: %v", err)
	}
	put(t, filepath.Join(dir, "kernel", "config-"+series+"-"+build.Arch), "CONFIG_SYSTEM_TRUSTED_KEYRING=y\n", 0o644)
	if _, stderr, status := verb(t, "certificate"); status != 2 || !strings.Contains(stderr, "no CONFIG_SYSTEM_TRUSTED_KEYS line") {
		t.Errorf("a config with no trusted keys line: exit %d, %q", status, stderr)
	}
}

// A proposal gh cannot open, or a checkout git cannot read, could not be made; a tree with no
// kernel config has nowhere to name the certificate.
func TestWhatTheCertificateCannotDoItSays(t *testing.T) {
	dir := builderCheckout(t)
	t.Setenv("GITHUB_RUN_ID", "78")
	w, err := open()
	if err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "kernel"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeModuleCertificate(w, key); err == nil || !strings.Contains(err.Error(), "config") {
		t.Errorf("no kernel config: %v", err)
	}
	fakes(t, map[string]string{"gh": "exit 1"})
	if _, _, status := verb(t, "certificate", "propose"); status != 2 {
		t.Errorf("gh failing: exit %d", status)
	}
	t.Setenv("GIT_DIR", filepath.Join(dir, "no-repository"))
	if _, _, status := verb(t, "certificate", "propose"); status != 2 {
		t.Errorf("no git: exit %d", status)
	}
}
