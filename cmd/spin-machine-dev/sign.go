// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"crypto"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"

	"github.com/spf13/cobra"

	"github.com/spin-stack/machine/internal/build"
	"github.com/spin-stack/machine/internal/modsign"
)

// openModuleKey opens the key the modules are signed with: spin-stack/ci's KMS key, which only
// this repository's release environment may sign with. A variable for the verbs' tests, which sign
// with a key of their own, as nothing else can ask KMS.
var openModuleKey = func(ctx context.Context) (crypto.Signer, error) {
	return modsign.OpenKMS(ctx, modsign.Key)
}

func certificateCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "certificate",
		Short: "Make the certificate of the module signing key, which the kernel trusts, and name it in the kernel's config",
		Long: `Make the certificate the guest kernel trusts its modules by: the self-signed certificate of
spin-stack/ci's KMS key (` + modsign.Key + `), written to ` + build.ModuleCertificate + `, and
the series' config's SYSTEM_TRUSTED_KEYS set to it. The kernel checks a builtin certificate's
self-signature, so making it takes one signature by the key, which only this repository's release
environment may ask for: module-certificate.yml runs it there and proposes what it wrote.

The certificate is the same bytes every time it is made for the key, so a certificate the tree has
already is left as it is, and one made for another key is refused: a new key is a new kernel, and
every module signed before stops loading on it.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			w, err := open()
			if err != nil {
				return err
			}
			signer, err := openModuleKey(c.Context())
			if err != nil {
				return couldNot(err)
			}
			if err := writeModuleCertificate(w, signer); err != nil {
				return couldNot(err)
			}
			say(c, "✓ %s, for %s; commit it with the kernel's config", build.ModuleCertificate, modsign.Key)
			return nil
		},
	}
	c.AddCommand(certificateProposeCmd())
	return c
}

func certificateProposeCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "propose",
		Short:  "Propose the certificate certificate wrote, and the config naming it, as a pull request against main",
		Hidden: true,
		Long: `Commit ` + build.ModuleCertificate + ` and the kernel's config on a branch of this run's own, push it
and open a pull request against main. Nothing is proposed when the tree has them as committed:
the certificate is the same bytes each time it is made. module-certificate.yml runs it as the
release app, so the checks on the pull request run.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			w, err := open()
			if err != nil {
				return err
			}
			changed := exec.CommandContext(c.Context(), "git", "-C", w.root, "status", "--porcelain", "--", "kernel") //nolint:gosec // this checkout
			out, err := changed.Output()
			if err != nil {
				return couldNot(fmt.Errorf("git status: %w", err))
			}
			if len(bytes.TrimSpace(out)) == 0 {
				say(c, "the tree has this key's certificate already")
				return nil
			}
			branch := "kernel/module-certificate-" + os.Getenv("GITHUB_RUN_ID")
			title := "kernel: the module signing certificate"
			for _, step := range [][]string{
				{"git", "switch", "-q", "-c", branch},
				{"git", "add", "--", build.ModuleCertificate, "kernel"},
				{"git", "commit", "-q", "-m", title},
				{"git", "push", "origin", branch},
			} {
				if err := command(c.Context(), c, w.root, step[0], step[1:]...); err != nil {
					return err
				}
			}
			body := "The certificate of " + modsign.Key + ", which the kernel builds in as its one trusted key, and the series' config naming it (#736), made by `spin-machine-dev certificate` in the release environment."
			url, err := gh(c.Context(), "pr", "create", "--base", "main", "--head", branch, "--title", title, "--body", body)
			if err != nil {
				return couldNot(err)
			}
			say(c, "%s", url)
			return summary(url + "\n")
		},
	}
}

// trustedKeys is the config line naming the kernel's trusted keys, whatever it names.
var trustedKeys = regexp.MustCompile(`(?m)^CONFIG_SYSTEM_TRUSTED_KEYS=.*$`)

// writeModuleCertificate writes signer's certificate to the tree's ModuleCertificate and names it
// in the series' config, which kernel/Dockerfile holds to naming it exactly when the tree has it.
func writeModuleCertificate(w workspace, signer crypto.Signer) error {
	der, err := modsign.Certificate(signer)
	if err != nil {
		return err
	}
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	path := filepath.Join(w.root, build.ModuleCertificate)
	switch had, err := os.ReadFile(path); { //nolint:gosec // the tree's own certificate
	case err == nil && !bytes.Equal(had, cert):
		return fmt.Errorf("%s is another key's certificate: a new key is a new kernel, which loads no module signed before, and a decision", build.ModuleCertificate)
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("reading %s: %w", build.ModuleCertificate, err)
	}
	if err := os.WriteFile(path, cert, 0o644); err != nil { //nolint:gosec // a public certificate
		return fmt.Errorf("writing %s: %w", build.ModuleCertificate, err)
	}
	series, err := build.KernelSeries(w.pins)
	if err != nil {
		return err
	}
	config := filepath.Join(w.root, "kernel", "config-"+series+"-"+build.Arch)
	b, err := os.ReadFile(config) //nolint:gosec // the series' config
	if err != nil {
		return fmt.Errorf("reading the kernel's config: %w", err)
	}
	if !trustedKeys.Match(b) {
		return fmt.Errorf("%s has no CONFIG_SYSTEM_TRUSTED_KEYS line to name the certificate in", config)
	}
	named := trustedKeys.ReplaceAll(b, []byte(`CONFIG_SYSTEM_TRUSTED_KEYS="certs/`+filepath.Base(build.ModuleCertificate)+`"`))
	if err := os.WriteFile(config, named, 0o644); err != nil { //nolint:gosec // the series' config
		return fmt.Errorf("writing the kernel's config: %w", err)
	}
	return nil
}

// signModules signs the kernel's modules under out with the key openKey opens, against the tree's
// certificate, unless every one is signed by it already: what a release ships them as, and what the
// image installs. A tree with no certificate is refused, as a kernel built from it trusts no key and
// would load none of them.
func signModules(ctx context.Context, w workspace, openKey func(context.Context) (crypto.Signer, error)) (int, error) {
	cert, err := build.ReadModuleCertificate(w.root)
	if err != nil {
		return 0, fmt.Errorf("%w: without it the kernel trusts no key and loads no module, and spin-machine-dev certificate makes it (#746)", err)
	}
	dir := filepath.Join(w.out, "kernel", "modules")
	if modsign.VerifyTree(dir, cert) == nil {
		return 0, nil
	}
	signer, err := openKey(ctx)
	if err != nil {
		return 0, err
	}
	n, err := modsign.SignTree(dir, signer, cert)
	if err != nil {
		return 0, err
	}
	return n, modsign.VerifyTree(dir, cert)
}
