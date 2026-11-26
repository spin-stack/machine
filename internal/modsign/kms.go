// SPDX-License-Identifier: Apache-2.0

package modsign

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/x509"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
)

// Key is spin-stack/ci's KMS key the modules are signed with (module_signing.tf): RSA_4096 for
// SIGN_VERIFY, which only a job of this repository's release environment may sign with.
const Key = "alias/spin-machine-module-signing"

// kmsAPI is what KMS is asked.
type kmsAPI interface {
	GetPublicKey(context.Context, *kms.GetPublicKeyInput, ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error)
	Sign(context.Context, *kms.SignInput, ...func(*kms.Options)) (*kms.SignOutput, error)
}

// KMS signs with a KMS key, whose private half never leaves KMS: a digest goes in and a
// signature comes out.
type KMS struct {
	ctx    context.Context //nolint:containedctx // crypto.Signer's Sign takes none
	client kmsAPI
	key    string
	public crypto.PublicKey
}

// OpenKMS is a signer over key, with the environment's AWS credentials: in release.yml, the role
// the release environment assumes.
func OpenKMS(ctx context.Context, key string) (*KMS, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil { // mutate-exempt: reached only with an AWS account; newKMS's tests hold what is decided
		return nil, fmt.Errorf("modsign: AWS credentials: %w", err)
	}
	return newKMS(ctx, kms.NewFromConfig(cfg), key)
}

// newKMS is a signer over key, which must be RSA_4096 for SIGN_VERIFY: module_signing.tf's.
func newKMS(ctx context.Context, client kmsAPI, key string) (*KMS, error) {
	out, err := client.GetPublicKey(ctx, &kms.GetPublicKeyInput{KeyId: aws.String(key)})
	if err != nil {
		return nil, fmt.Errorf("modsign: reading %s: %w", key, err)
	}
	if out.KeySpec != kmstypes.KeySpecRsa4096 || out.KeyUsage != kmstypes.KeyUsageTypeSignVerify {
		return nil, fmt.Errorf("modsign: %s is a %s key for %s, not RSA_4096 for SIGN_VERIFY", key, out.KeySpec, out.KeyUsage)
	}
	public, err := x509.ParsePKIXPublicKey(out.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("modsign: %s's public key: %w", key, err)
	}
	return &KMS{ctx: ctx, client: client, key: key, public: public}, nil
}

// Public is the key's public half.
func (s *KMS) Public() crypto.PublicKey { return s.public }

// Sign signs digest, which must be a SHA-256 one, with PKCS #1 v1.5: deterministic, so a module
// signed twice is the same bytes.
func (s *KMS) Sign(_ io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	if _, pss := opts.(*rsa.PSSOptions); opts == nil || pss || opts.HashFunc() != crypto.SHA256 {
		return nil, errors.New("modsign: the key signs SHA-256 digests with PKCS #1 v1.5, and nothing else")
	}
	out, err := s.client.Sign(s.ctx, &kms.SignInput{
		KeyId: aws.String(s.key), Message: digest, MessageType: kmstypes.MessageTypeDigest,
		SigningAlgorithm: kmstypes.SigningAlgorithmSpecRsassaPkcs1V15Sha256,
	})
	if err != nil {
		return nil, fmt.Errorf("modsign: signing with %s: %w", s.key, err)
	}
	return out.Signature, nil
}
