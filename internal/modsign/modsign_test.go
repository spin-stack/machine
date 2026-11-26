// SPDX-License-Identifier: Apache-2.0

package modsign

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var (
	keysOnce sync.Once
	keys     [2]*rsa.PrivateKey
)

// key is one of two RSA keys made once for the package's tests: 2048 bits, as the kernel's
// verification and this package do not depend on the size, and 4096 takes seconds a key.
func key(t *testing.T, i int) *rsa.PrivateKey {
	t.Helper()
	keysOnce.Do(func() {
		for j := range keys {
			k, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				panic(err)
			}
			keys[j] = k
		}
	})
	return keys[i]
}

func certificate(t *testing.T, signer crypto.Signer) *x509.Certificate {
	t.Helper()
	der, err := Certificate(signer)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

// A module signed here verifies against its certificate as the kernel checks it, and as OpenSSL's
// CMS, which scripts/sign-file is built on, reads it: a detached signature over the module, with
// no authenticated attributes. Its content is the module, byte for byte.
func TestASignedModuleVerifiesAgainstItsCertificate(t *testing.T) {
	k := key(t, 0)
	cert := certificate(t, k)
	module := []byte("\x7fELF a module's bytes")
	signed, err := Sign(module, k, cert)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(signed, cert); err != nil {
		t.Fatalf("a module signed by the certificate's key does not verify: %v", err)
	}
	content, p7, ok := Split(signed)
	if !ok || !bytes.Equal(content, module) {
		t.Fatalf("the signed module's content is %q, want %q", content, module)
	}
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Log("no openssl: the signature is checked by Verify alone")
		return
	}
	dir := t.TempDir()
	for name, b := range map[string][]byte{
		"module": module, "sig.p7": p7, "cert.pem": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out, err := exec.Command("openssl", "cms", "-verify", "-binary", "-inform", "DER", "-in", filepath.Join(dir, "sig.p7"),
		"-content", filepath.Join(dir, "module"), "-certfile", filepath.Join(dir, "cert.pem"), "-nointern", "-noverify",
		"-out", os.DevNull).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "Verification successful") {
		t.Fatalf("openssl does not verify the signature: %v\n%s", err, out)
	}
}

// What the kernel refuses, Verify refuses: a module with no signature, one signed by another key,
// and one changed after it was signed.
func TestVerifyRefusesWhatTheKernelRefuses(t *testing.T) {
	ours, theirs := key(t, 0), key(t, 1)
	cert, other := certificate(t, ours), certificate(t, theirs)
	module := []byte("\x7fELF a module's bytes")
	signed, err := Sign(module, ours, cert)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := Sign(module, theirs, other)
	if err != nil {
		t.Fatal(err)
	}
	changed := bytes.Clone(signed)
	changed[1] ^= 1
	for name, m := range map[string][]byte{"unsigned": module, "signed by another key": foreign, "changed after signing": changed} {
		if err := Verify(m, cert); err == nil {
			t.Errorf("a module %s verifies", name)
		}
	}
}

// A signature is deterministic, so a release's modules are the same bytes each time they are
// signed, and so is the certificate: made again for the same key, it is the same file.
func TestSignaturesAndTheCertificateAreReproducible(t *testing.T) {
	k := key(t, 0)
	a, b := certificate(t, k), certificate(t, k)
	if !bytes.Equal(a.Raw, b.Raw) {
		t.Error("the certificate made twice for one key differs")
	}
	if err := a.CheckSignature(a.SignatureAlgorithm, a.RawTBSCertificate, a.Signature); err != nil {
		t.Errorf("the certificate's self-signature, which the kernel checks before it trusts the key, does not verify: %v", err)
	}
	s1, err := Sign([]byte("module"), k, a)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := Sign([]byte("module"), k, a)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(s1, s2) {
		t.Error("a module signed twice differs")
	}
}

// A key that is not the certificate's signs nothing, and a module signed already is not signed
// again.
func TestSignRefusesAnotherKeyAndASignedModule(t *testing.T) {
	ours, theirs := key(t, 0), key(t, 1)
	cert := certificate(t, ours)
	if _, err := Sign([]byte("module"), theirs, cert); err == nil {
		t.Error("a key that is not the certificate's signed a module")
	}
	signed, err := Sign([]byte("module"), ours, cert)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Sign(signed, ours, cert); err == nil {
		t.Error("a signed module was signed again")
	}
}

// failing signs n digests and refuses the rest.
type failing struct {
	crypto.Signer
	n int
}

func (f *failing) Sign(r io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	if f.n == 0 {
		return nil, errors.New("throttled")
	}
	f.n--
	return f.Signer.Sign(r, digest, opts)
}

// A tree is signed whole or left as it was: a key that fails part way writes no module, and the
// tree can be signed again.
func TestATreeIsSignedWholeOrNotAtAll(t *testing.T) {
	k := key(t, 0)
	cert := certificate(t, k)
	dir := t.TempDir()
	for _, m := range []string{"kernel/fs/fuse/fuse.ko", "kernel/fs/erofs/erofs.ko"} {
		p := filepath.Join(dir, m)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(m), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "modules.dep"), []byte("kernel/fs/fuse/fuse.ko:\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SignTree(dir, &failing{Signer: k, n: 1}, cert); err == nil {
		t.Fatal("a key that failed part way signed the tree")
	}
	if err := VerifyTree(dir, cert); err == nil || !strings.Contains(err.Error(), "2 of 2") {
		t.Fatalf("after a failed signing, VerifyTree says %v, want both modules unsigned", err)
	}
	n, err := SignTree(dir, k, cert)
	if err != nil || n != 2 {
		t.Fatalf("SignTree = %d, %v, want 2 modules signed", n, err)
	}
	if err := VerifyTree(dir, cert); err != nil {
		t.Fatal(err)
	}
	dep, err := os.ReadFile(filepath.Join(dir, "modules.dep"))
	if err != nil || string(dep) != "kernel/fs/fuse/fuse.ko:\n" {
		t.Errorf("modules.dep is %q, %v: SignTree touched what is not a module", dep, err)
	}
	if _, err := SignTree(t.TempDir(), k, cert); err == nil {
		t.Error("a tree with no module was signed")
	}
}

// A trailer that says it carries a signature, over bytes that are not one, is refused as the
// kernel refuses it, and so is a signature with no signer or two.
func TestVerifyRefusesASignatureThatIsNotOne(t *testing.T) {
	k := key(t, 0)
	cert := certificate(t, k)
	signed, err := Sign([]byte("module"), k, cert)
	if err != nil {
		t.Fatal(err)
	}
	_, p7, _ := Split(signed)
	trailer := func(sig []byte) []byte {
		tr := make([]byte, trailerLen)
		tr[2] = pkeyIDPKCS7
		binary.BigEndian.PutUint32(tr[8:], uint32(len(sig)))
		return append(append(append([]byte("module"), sig...), tr...), Marker...)
	}
	var m message
	if _, err := asn1.Unmarshal(p7, &m); err != nil {
		t.Fatal(err)
	}
	m.Content.SignerInfos = append(m.Content.SignerInfos, m.Content.SignerInfos[0])
	two, err := asn1.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	for name, module := range map[string][]byte{
		"not PKCS #7":       trailer([]byte("garbage")),
		"two signers":       trailer(two),
		"a length too long": append(append([]byte("module"), 0, 0, pkeyIDPKCS7, 0, 0, 0, 0, 0, 0, 0, 1, 0), Marker...),
	} {
		if err := Verify(module, cert); err == nil {
			t.Errorf("a module whose signature is %s verifies", name)
		}
	}
}

// A key that fails signs nothing, a certificate included, and a module that cannot be read is
// named rather than skipped.
func TestWhatCannotBeSignedOrReadIsRefused(t *testing.T) {
	k := key(t, 0)
	cert := certificate(t, k)
	if _, err := Certificate(&failing{Signer: k}); err == nil {
		t.Error("a key that fails made a certificate")
	}
	if _, err := Sign([]byte("module"), &failing{Signer: k}, cert); err == nil {
		t.Error("a key that fails signed a module")
	}
	dir := t.TempDir()
	mod := filepath.Join(dir, "a.ko")
	if err := os.WriteFile(mod, []byte("module"), 0o000); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads a file of mode 0")
	}
	if _, err := SignTree(dir, k, cert); err == nil || !strings.Contains(err.Error(), "a.ko") {
		t.Errorf("an unreadable module: SignTree says %v", err)
	}
	if err := VerifyTree(dir, cert); err == nil || !strings.Contains(err.Error(), "a.ko") {
		t.Errorf("an unreadable module: VerifyTree says %v", err)
	}
	if err := VerifyTree(filepath.Join(dir, "absent"), cert); err == nil {
		t.Error("a tree that is not there verifies")
	}
	if err := os.Chmod(mod, 0o444); err != nil {
		t.Fatal(err)
	}
	if _, err := SignTree(dir, k, cert); err == nil {
		t.Error("a module that cannot be written was signed")
	}
}

// testKey is testdata/test.key: a key made for these tests and nothing else, with which the
// kernel's own scripts/sign-file signed testdata/module.ko into module.ko.signed against
// test.crt, the certificate Certificate makes for it.
func testKey(t *testing.T) crypto.Signer {
	t.Helper()
	b, err := os.ReadFile("testdata/test.key")
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(b)
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return k.(crypto.Signer)
}

// A module signed here is byte for byte what the kernel's scripts/sign-file appends (7.4-rc4's,
// sha256, the same key and certificate): the SignedData, the module_signature trailer and the
// marker. And the certificate is the one made for that key the day the fixture was: its serial,
// names and dates are what the committed certificate is checked against when it is made again.
func TestASignatureIsWhatSignFileAppends(t *testing.T) {
	k := testKey(t)
	der, err := Certificate(k)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("testdata/test.crt")
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(b)
	if !bytes.Equal(der, block.Bytes) {
		t.Fatal("the certificate made for the test key is not testdata/test.crt")
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	module, err := os.ReadFile("testdata/module.ko")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/module.ko.signed")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Sign(module, k, cert)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("modsign appends %x\nsign-file appends %x", got[len(module):], want[len(module):])
	}
	if err := Verify(want, cert); err != nil {
		t.Errorf("sign-file's module does not verify: %v", err)
	}
}

// Split reads a signature only where the trailer says one fits: a module that is the marker alone,
// or whose trailer claims one byte more than there is, carries none.
func TestSplitReadsOnlyASignatureThatFits(t *testing.T) {
	over := make([]byte, trailerLen)
	over[2] = pkeyIDPKCS7
	binary.BigEndian.PutUint32(over[8:], 7) // "module" is 6 bytes
	for name, m := range map[string][]byte{
		"the marker alone":      []byte(Marker),
		"a length one too long": append(append([]byte("module"), over...), Marker...),
	} {
		if _, _, ok := Split(m); ok {
			t.Errorf("%s carries a signature", name)
		}
	}
}

// What follows the SignedData, or a message that is not SignedData, is not a module signature.
func TestVerifyRefusesMoreOrOtherThanSignedData(t *testing.T) {
	k := key(t, 0)
	cert := certificate(t, k)
	signed, err := Sign([]byte("module"), k, cert)
	if err != nil {
		t.Fatal(err)
	}
	_, p7, _ := Split(signed)
	var m message
	if _, err := asn1.Unmarshal(p7, &m); err != nil {
		t.Fatal(err)
	}
	m.ContentType = oidData
	other, err := asn1.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	m.ContentType = oidSignedData
	m.Content.SignerInfos[0].SID.Serial = big.NewInt(737)
	serial, err := asn1.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	// The signature itself covers the module and not who it names, so only the name refuses this.
	m.Content.SignerInfos[0].SID.Serial = cert.SerialNumber
	m.Content.SignerInfos[0].SID.Issuer = asn1.RawValue{FullBytes: mustName(t, "a tenant")}
	issuer, err := asn1.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	for name, sig := range map[string][]byte{
		"followed by more":      append(bytes.Clone(p7), 0x05),
		"naming another issuer": issuer,
		"not SignedData":        other,
		"by another serial":     serial,
	} {
		tr := make([]byte, trailerLen)
		tr[2] = pkeyIDPKCS7
		binary.BigEndian.PutUint32(tr[8:], uint32(len(sig)))
		if err := Verify(append(append(append([]byte("module"), sig...), tr...), Marker...), cert); err == nil {
			t.Errorf("a signature %s verifies", name)
		}
	}
}

func mustName(t *testing.T, cn string) []byte {
	t.Helper()
	b, err := asn1.Marshal(pkix.Name{CommonName: cn}.ToRDNSequence())
	if err != nil {
		t.Fatal(err)
	}
	return b
}
