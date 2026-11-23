// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/spin-stack/machine/internal/patched"
)

func patchedCmd() *cobra.Command {
	c := &cobra.Command{
		Use:    "patched",
		Short:  "Make the source trees the machine's C patches change, and scope an analysis of them",
		Hidden: true,
		Long: `The trees codeql.yml analyses (#653): a part's pinned upstream source with its patches applied,
in <out>/patched/<part>. Parts: ` + strings.Join(patched.Names(), ", ") + `.`,
	}
	c.AddCommand(&cobra.Command{
		Use:   "tree <part>",
		Short: "Make a part's patched tree, with every C file the patches do not touch taken out",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			w, p, dir, err := patchedPart(args[0])
			if err != nil {
				return err
			}
			if err := patched.Tree(c.Context(), w.root, w.pins, p, dir); err != nil {
				return couldNot(err)
			}
			say(c, "%s: %s", p.Name, filepath.Join(dir, "src"))
			return nil
		},
	}, &cobra.Command{
		Use:   "scope <part>",
		Short: "Keep an analysis' results in the files a part's patches touch (exits 1 on one in a line they added)",
		Long: `Keep the results of the SARIF in <out>/patched/<part>/sarif that are in a file the part's
patches touch, in <out>/patched/<part>/scoped.sarif, for code scanning. Exits 1 when a result is on
a line the patches added or changed: upstream's own results in those files decide nothing.`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			_, p, dir, err := patchedPart(args[0])
			if err != nil {
				return err
			}
			findings, err := patched.Scope(c.Context(), dir)
			if err != nil {
				return couldNot(err)
			}
			for _, f := range findings {
				_, _ = fmt.Fprintln(c.OutOrStdout(), f)
			}
			if len(findings) > 0 {
				return exitError{code: 1, err: fmt.Errorf("%s: %d results on lines the patches added", p.Name, len(findings))}
			}
			say(c, "%s: no result on a line the patches added", p.Name)
			return nil
		},
	})
	return c
}

// patchedPart is the workspace, the part named and the directory its tree is made in.
func patchedPart(name string) (workspace, patched.Part, string, error) {
	p, err := patched.Find(name)
	if err != nil {
		return workspace{}, p, "", couldNot(err)
	}
	w, err := open()
	if err != nil {
		return workspace{}, p, "", err
	}
	return w, p, filepath.Join(w.out, "patched", p.Name), nil
}
