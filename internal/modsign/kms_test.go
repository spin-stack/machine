// SPDX-License-Identifier: Apache-2.0

package modsign

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
)

// fakeKMS is KMS's answers for one local key, and what it was asked to sign.
type fakeKMS struct {
	key  *rsa.PrivateKey
	spec kmstypes.KeySpec
	asks []kms.SignInput
}

func (f *fakeKMS) GetPublicKey(_ context.Context, in *kms.GetPublicKeyInput, _ ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error) {
	der, err := x509.MarshalPKIXPublicKey(&f.key.PublicKey)
	if err != nil {
		return nil, err
	}
	return &kms.GetPublicKeyOutput{KeyId: in.KeyId, PublicKey: der, KeySpec: f.spec, KeyUsage: kmstypes.KeyUsageTypeSignVerify}, nil
}

func (f *fakeKMS) Sign(_ context.Context, in *kms.SignInput, _ ...func(*kms.Options)) (*kms.SignOutput, error) {
	f.asks = append(f.asks, *in)
	sig, err := rsa.SignPKCS1v15(nil, f.key, crypto.SHA256, in.Message)
	return &kms.SignOutput{Signature: sig}, err
}

// The module_signing.tf key is RSA_4096: another kind of key is refused before anything is signed,
// and KMS is asked for a PKCS #1 v1.5 signature of the SHA-256 digest alone, which is what the
// kernel verifies and what keeps a signature the same each time.
func TestKMSSignsDigestsWithTheKeyTheKernelTrusts(t *testing.T) {
	k := key(t, 0)
	if _, err := newKMS(t.Context(), &fakeKMS{key: k, spec: kmstypes.KeySpecRsa2048}, Key); err == nil {
		t.Fatal("an RSA_2048 key was taken for the module key")
	}
	fake := &fakeKMS{key: k, spec: kmstypes.KeySpecRsa4096}
	s, err := newKMS(t.Context(), fake, Key)
	if err != nil {
		t.Fatal(err)
	}
	cert := certificate(t, s)
	signed, err := Sign([]byte("module"), s, cert)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(signed, cert); err != nil {
		t.Fatalf("a module KMS signed does not verify: %v", err)
	}
	digest := sha256.Sum256([]byte("module"))
	last := fake.asks[len(fake.asks)-1]
	if aws.ToString(last.KeyId) != Key || last.MessageType != kmstypes.MessageTypeDigest ||
		last.SigningAlgorithm != kmstypes.SigningAlgorithmSpecRsassaPkcs1V15Sha256 || string(last.Message) != string(digest[:]) {
		t.Errorf("KMS was asked %+v, want %s to sign the module's SHA-256 digest with PKCS #1 v1.5", last, Key)
	}
	for _, opts := range []crypto.SignerOpts{nil, crypto.SHA512, &rsa.PSSOptions{Hash: crypto.SHA256}} {
		if _, err := s.Sign(nil, digest[:], opts); err == nil {
			t.Errorf("the key signed with %v", opts)
		}
	}
}

// brokenKMS answers as KMS does when it cannot: the key's public half unreadable, or a signature
// refused.
type brokenKMS struct {
	fakeKMS
	read, sign error
	der        []byte
}

func (b *brokenKMS) GetPublicKey(ctx context.Context, in *kms.GetPublicKeyInput, o ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error) {
	if b.read != nil {
		return nil, b.read
	}
	out, err := b.fakeKMS.GetPublicKey(ctx, in, o...)
	if b.der != nil {
		out.PublicKey = b.der
	}
	return out, err
}

func (b *brokenKMS) Sign(context.Context, *kms.SignInput, ...func(*kms.Options)) (*kms.SignOutput, error) {
	return nil, b.sign
}

// A key KMS cannot read, or whose public half does not parse, is no signer, and a signature KMS
// refuses is an error naming the key.
func TestKMSSaysWhatItCouldNotDo(t *testing.T) {
	k := key(t, 0)
	for name, b := range map[string]*brokenKMS{
		"unreadable":       {fakeKMS: fakeKMS{key: k, spec: kmstypes.KeySpecRsa4096}, read: errors.New("AccessDenied")},
		"not a public key": {fakeKMS: fakeKMS{key: k, spec: kmstypes.KeySpecRsa4096}, der: []byte("garbage")},
	} {
		if _, err := newKMS(t.Context(), b, Key); err == nil {
			t.Errorf("a key that is %s was taken", name)
		}
	}
	s, err := newKMS(t.Context(), &brokenKMS{fakeKMS: fakeKMS{key: k, spec: kmstypes.KeySpecRsa4096}, sign: errors.New("throttled")}, Key)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("module"))
	if _, err := s.Sign(nil, digest[:], crypto.SHA256); err == nil || !strings.Contains(err.Error(), Key) {
		t.Errorf("a signature KMS refused: %v", err)
	}
}

// Without the release role's credentials there is no signer: anywhere but a job of the release
// environment, which is the only one that may assume the role.
func TestOpenKMSNeedsCredentials(t *testing.T) {
	for k, v := range map[string]string{
		"AWS_REGION": "us-west-2", "AWS_ACCESS_KEY_ID": "", "AWS_SECRET_ACCESS_KEY": "", "AWS_SESSION_TOKEN": "",
		"AWS_PROFILE": "", "AWS_CONFIG_FILE": "/nonexistent", "AWS_SHARED_CREDENTIALS_FILE": "/nonexistent",
		"AWS_EC2_METADATA_DISABLED": "true", "AWS_WEB_IDENTITY_TOKEN_FILE": "", "AWS_ROLE_ARN": "",
		"AWS_CONTAINER_CREDENTIALS_FULL_URI": "", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI": "",
	} {
		t.Setenv(k, v)
	}
	if _, err := OpenKMS(t.Context(), Key); err == nil || !strings.Contains(err.Error(), Key) {
		t.Errorf("a signer was opened with no credentials: %v", err)
	}
}
