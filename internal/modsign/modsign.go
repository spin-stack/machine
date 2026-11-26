// SPDX-License-Identifier: Apache-2.0

// Package modsign signs the guest kernel's modules the way the kernel checks them, and makes the
// certificate the kernel trusts them by (kernel/README.md, "Modules").
//
// A signed module is the module, a PKCS #7 SignedData over it, a module_signature trailer that
// says how long that is, and a marker: what scripts/sign-file appends, and what
// kernel/module/signing.c reads back. The signature is RSA PKCS #1 v1.5 over SHA-256 with no
// authenticated attributes (pkcs7_verify refuses a module signature that has them), so it is
// deterministic: the same module signed by the same key is the same bytes, and a release's modules
// are reproducible (#732). The key is a crypto.Signer: spin-stack/ci's KMS key in release.yml
// (KMS), never one this repository holds.
package modsign

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // RFC 5280's key identifier, a name and not a signature
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Marker ends every signed module (MODULE_SIGNATURE_MARKER).
const Marker = "~Module signature appended~\n"

// trailerLen is struct module_signature's size: algo, hash, id_type, signer_len, key_id_len, three
// bytes of padding and a big-endian sig_len.
const trailerLen = 12

// pkeyIDPKCS7 is module_signature's id_type for a PKCS #7 signature, the only one the kernel takes.
const pkeyIDPKCS7 = 2

var (
	oidData          = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}
	oidSignedData    = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
	oidSHA256        = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}
	oidRSAEncryption = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 1}
)

type issuerAndSerial struct {
	Issuer asn1.RawValue
	Serial *big.Int
}

type signerInfo struct {
	Version            int
	SID                issuerAndSerial
	DigestAlgorithm    pkix.AlgorithmIdentifier
	SignatureAlgorithm pkix.AlgorithmIdentifier
	Signature          []byte
}

type contentInfo struct {
	ContentType asn1.ObjectIdentifier
}

type signedData struct {
	Version          int
	DigestAlgorithms []pkix.AlgorithmIdentifier `asn1:"set"`
	ContentInfo      contentInfo
	SignerInfos      []signerInfo `asn1:"set"`
}

type message struct {
	ContentType asn1.ObjectIdentifier
	Content     signedData `asn1:"explicit,tag:0"`
}

// Split is module's content and its PKCS #7 signature, and false when it carries no signature: what
// the kernel reads before it verifies.
func Split(module []byte) (content, signature []byte, ok bool) {
	body, found := bytes.CutSuffix(module, []byte(Marker))
	if !found || len(body) < trailerLen {
		return module, nil, false
	}
	trailer := body[len(body)-trailerLen:]
	n := int(binary.BigEndian.Uint32(trailer[8:]))
	if trailer[2] != pkeyIDPKCS7 || n > len(body)-trailerLen {
		return module, nil, false
	}
	end := len(body) - trailerLen
	return body[:end-n], body[end-n : end], true
}

// Sign is module with signer's signature appended, as the kernel checks it against cert, the
// certificate of signer's key. A module signed already is refused: a second signature would sit
// inside the content the first one covers.
func Sign(module []byte, signer crypto.Signer, cert *x509.Certificate) ([]byte, error) {
	if !samePublicKey(cert.PublicKey, signer.Public()) {
		return nil, errors.New("modsign: the signing key is not the certificate's: a module it signs loads nowhere")
	}
	if bytes.HasSuffix(module, []byte(Marker)) {
		return nil, errors.New("modsign: the module is signed already")
	}
	digest := sha256.Sum256(module)
	sig, err := signer.Sign(rand.Reader, digest[:], crypto.SHA256)
	if err != nil {
		return nil, fmt.Errorf("modsign: signing: %w", err)
	}
	p7, err := asn1.Marshal(message{
		ContentType: oidSignedData,
		Content: signedData{
			Version:          1,
			DigestAlgorithms: []pkix.AlgorithmIdentifier{{Algorithm: oidSHA256}},
			ContentInfo:      contentInfo{ContentType: oidData},
			SignerInfos: []signerInfo{{
				Version:            1,
				SID:                issuerAndSerial{Issuer: asn1.RawValue{FullBytes: cert.RawIssuer}, Serial: cert.SerialNumber},
				DigestAlgorithm:    pkix.AlgorithmIdentifier{Algorithm: oidSHA256},
				SignatureAlgorithm: pkix.AlgorithmIdentifier{Algorithm: oidRSAEncryption, Parameters: asn1.NullRawValue},
				Signature:          sig,
			}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("modsign: encoding the signature: %w", err)
	}
	trailer := make([]byte, trailerLen)
	trailer[2] = pkeyIDPKCS7
	binary.BigEndian.PutUint32(trailer[8:], uint32(len(p7))) //nolint:gosec // a few hundred bytes
	return slices.Concat(module, p7, trailer, []byte(Marker)), nil
}

// Verify says whether module carries a signature by cert's key over its content, as the kernel
// checks one: by issuer and serial, over SHA-256, PKCS #1 v1.5. What a release asks of every module
// before it ships one.
func Verify(module []byte, cert *x509.Certificate) error {
	content, p7, ok := Split(module)
	if !ok {
		return errors.New("unsigned")
	}
	var m message
	if rest, err := asn1.Unmarshal(p7, &m); err != nil || len(rest) > 0 || !m.ContentType.Equal(oidSignedData) {
		return fmt.Errorf("its signature is not PKCS #7 SignedData: %v", err)
	}
	if len(m.Content.SignerInfos) != 1 {
		return fmt.Errorf("its signature has %d signers, not one", len(m.Content.SignerInfos))
	}
	si := m.Content.SignerInfos[0]
	if !bytes.Equal(si.SID.Issuer.FullBytes, cert.RawIssuer) || si.SID.Serial == nil || si.SID.Serial.Cmp(cert.SerialNumber) != 0 {
		return errors.New("signed by another key")
	}
	pub, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return errors.New("the certificate's key is not RSA")
	}
	digest := sha256.Sum256(content)
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], si.Signature); err != nil {
		return fmt.Errorf("its signature does not verify: %w", err)
	}
	return nil
}

// Modules is every module under dir, by path.
func Modules(dir string) ([]string, error) {
	var mods []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".ko") {
			mods = append(mods, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("modsign: listing the modules in %s: %w", dir, err)
	}
	if len(mods) == 0 {
		return nil, fmt.Errorf("modsign: no module in %s", dir)
	}
	return mods, nil
}

// SignTree signs every module under dir in place, and is the number signed. All of them or none:
// each is signed before any is written, so a key that fails part way leaves the tree as it was.
func SignTree(dir string, signer crypto.Signer, cert *x509.Certificate) (int, error) {
	mods, err := Modules(dir)
	if err != nil {
		return 0, err
	}
	signed := make([][]byte, len(mods))
	for i, path := range mods {
		raw, err := os.ReadFile(path) //nolint:gosec // a module the kernel build wrote
		if err != nil {
			return 0, fmt.Errorf("modsign: %w", err)
		}
		if signed[i], err = Sign(raw, signer, cert); err != nil {
			return 0, fmt.Errorf("%w: %s", err, path)
		}
	}
	for i, path := range mods {
		if err := os.WriteFile(path, signed[i], 0o644); err != nil { //nolint:gosec // a module, read by modprobe as any other
			return 0, fmt.Errorf("modsign: %w", err)
		}
	}
	return len(mods), nil
}

// VerifyTree refuses dir unless every module under it is signed by cert's key, naming each one
// that is not.
func VerifyTree(dir string, cert *x509.Certificate) error {
	mods, err := Modules(dir)
	if err != nil {
		return err
	}
	var bad []string
	for _, path := range mods {
		raw, err := os.ReadFile(path) //nolint:gosec // a module the kernel build wrote
		if err != nil {
			return fmt.Errorf("modsign: %w", err)
		}
		if err := Verify(raw, cert); err != nil {
			rel, _ := filepath.Rel(dir, path)
			bad = append(bad, rel+": "+err.Error())
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("modsign: %d of %d modules in %s would not load: %s", len(bad), len(mods), dir, strings.Join(bad, "; "))
	}
	return nil
}

// certificateName is the certificate's subject and issuer: who a person reading the kernel's
// .builtin_trusted_keys sees signed a module.
var certificateName = pkix.Name{CommonName: "spin-stack/machine module signing", Organization: []string{"spin-stack"}}

// Certificate is the self-signed certificate of signer's key, DER, that the kernel builds in as its
// one trusted key (SYSTEM_TRUSTED_KEYS). The kernel verifies a builtin certificate's self-signature
// before it takes the key (x509_check_for_self_signed), so making it takes one signature by the key
// itself. Everything in it is fixed, the dates and serial included, and the signature is
// deterministic: made again for the same key, it is the same bytes.
func Certificate(signer crypto.Signer) ([]byte, error) {
	pub, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil {
		return nil, fmt.Errorf("modsign: the key's public half: %w", err)
	}
	var spki struct {
		Algorithm pkix.AlgorithmIdentifier
		Key       asn1.BitString
	}
	// It parses: MarshalPKIXPublicKey just wrote it.
	_, _ = asn1.Unmarshal(pub, &spki)
	skid := sha1.Sum(spki.Key.Bytes) //nolint:gosec // RFC 5280 4.2.1.2's method (1)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(736),
		Subject:               certificateName,
		NotBefore:             time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC),    // mutate-exempt: X.509 keeps no nanoseconds
		NotAfter:              time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC), // mutate-exempt: X.509 keeps no nanoseconds
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		BasicConstraintsValid: true,
		SubjectKeyId:          skid[:],
		SignatureAlgorithm:    x509.SHA256WithRSA,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, signer.Public(), signer)
	if err != nil {
		return nil, fmt.Errorf("modsign: making the certificate: %w", err)
	}
	return der, nil
}

func samePublicKey(a, b crypto.PublicKey) bool {
	eq, ok := a.(interface{ Equal(crypto.PublicKey) bool })
	return ok && eq.Equal(b)
}
