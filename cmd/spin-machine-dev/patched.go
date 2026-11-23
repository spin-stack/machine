// SPDX-License-Identifier: Apache-2.0

package main

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/spin-stack/machine/internal/patched"
)

func patchedCmd() *cobra.Command {
	c := &cobra.Command{
		Use:    "patched",
		Short:  "Make the source trees the machine's C patches change, scope an analysis of them, and sanitize QEMU's",
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
		Use:   "compile <part>",
		Short: "Compile the C files a part's patches touch in its tree, for an analysis tracing the compiler",
		Long: `Compile the C files the part's patches touch in <out>/patched/<part>/src, which tree made, as
the part's build would: the kernel from its configuration in kernel/, QEMU for x86_64-softmmu.
codeql.yml runs it under CodeQL's tracer for the parts whose files include headers only their
builds generate. Parts that have it: ` + strings.Join(traced(), ", ") + `.`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			w, p, dir, err := patchedPart(args[0])
			if err != nil {
				return err
			}
			if err := patched.Compile(c.Context(), w.root, p, dir, c.ErrOrStderr()); err != nil {
				return couldNot(err)
			}
			say(c, "%s: compiled what the patches touch", p.Name)
			return nil
		},
	}, &cobra.Command{
		Use:   "scope <part>",
		Short: "Keep an analysis' results in the files a part's patches touch (exits 1 on one in a line they added)",
		Long: `Keep the results of the SARIF in <out>/patched/<part>/sarif that are in a file the part's
patches touch, in <out>/patched/<part>/scoped.sarif, for code scanning. Exits 1 when a result is on
a line the patches added or changed: upstream's own results in those files decide nothing. Exits 2
naming each touched C file the analysis did not extract, or failed to compile: no result in a file
it did not read is not a clean one.`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			_, p, dir, err := patchedPart(args[0])
			if err != nil {
				return err
			}
			findings, err := patched.Scope(c.Context(), dir)
			for _, f := range findings {
				_, _ = fmt.Fprintln(c.OutOrStdout(), f)
			}
			if err != nil {
				return couldNot(err)
			}
			if len(findings) > 0 {
				return exitError{code: 1, err: fmt.Errorf("%s: %d results on lines the patches added", p.Name, len(findings))}
			}
			say(c, "%s: no result on a line the patches added", p.Name)
			return nil
		},
	}, &cobra.Command{
		Use:   "sanitized",
		Short: "Build QEMU's patched tree under ASan and UBSan, as a QEMU a lab tree takes, and print its environment",
		Long: `Build QEMU from <out>/patched/qemu/src, which tree made, with the release's configuration (qemu/Dockerfile's
QEMU_FLAGS, less what the sanitizers cannot take) under AddressSanitizer and UndefinedBehaviorSanitizer,
into <out>/patched/qemu/sanitized: bin/qemu-system-x86_64, and in qemu/ the firmware of <out>/qemu,
which fetch qemu puts there. A lab build, dynamically linked against this host's libraries; nothing
ships it (sanitize.yml, #653).

Prints, a line each, SPIN_SANITIZED (the directory, for lab tree's --qemu) and the ASAN_OPTIONS and
UBSAN_OPTIONS that write every report and every process's start under <out>/patched/qemu/sanitizer,
emptied here: lines a workflow appends to GITHUB_ENV. reports reads them.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			w, _, dir, err := patchedPart("qemu")
			if err != nil {
				return err
			}
			laid, err := patched.Sanitized(c.Context(), w.root, dir, filepath.Join(w.out, "qemu"), c.ErrOrStderr())
			if err != nil {
				return couldNot(err)
			}
			logs, err := sanitizerLogs(dir)
			if err != nil {
				return couldNot(err)
			}
			_, _ = fmt.Fprintln(c.OutOrStdout(), "SPIN_SANITIZED="+laid)
			for _, e := range patched.SanitizerOptions(logs) {
				_, _ = fmt.Fprintln(c.OutOrStdout(), e)
			}
			return nil
		},
	}, &cobra.Command{
		Use:   "fuzzer",
		Short: "Build QEMU's libFuzzer binary from its patched tree under ASan and UBSan, and print its environment",
		Long: `Build qemu-fuzz-x86_64 from <out>/patched/qemu/src, which tree made, with clang, under
AddressSanitizer and UndefinedBehaviorSanitizer (QEMU's docs/devel/testing/fuzzing.rst). Prints
SPIN_FUZZER (the binary) and the ASAN_OPTIONS and UBSAN_OPTIONS of sanitized, whose logs reports
reads.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			_, _, dir, err := patchedPart("qemu")
			if err != nil {
				return err
			}
			bin, err := patched.Fuzzer(c.Context(), dir, c.ErrOrStderr())
			if err != nil {
				return couldNot(err)
			}
			logs, err := sanitizerLogs(dir)
			if err != nil {
				return couldNot(err)
			}
			_, _ = fmt.Fprintln(c.OutOrStdout(), "SPIN_FUZZER="+bin)
			for _, e := range patched.SanitizerOptions(logs) {
				_, _ = fmt.Fprintln(c.OutOrStdout(), e)
			}
			return nil
		},
	}, reportsCmd(), &cobra.Command{
		Use:   "severe <sarif-dir>",
		Short: "Fail on a result of medium security severity or above that the source does not suppress",
		Long: `Read the SARIF files in <sarif-dir>, which codeql.yml's analysis of this repository's own code
writes, and exit 1 naming each result whose rule's security severity is medium (4.0) or above and
which no codeql[<rule>] comment suppresses. A result below that goes to code scanning only.`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			severe, err := patched.Severe(args[0], patched.Medium)
			if err != nil {
				return couldNot(err)
			}
			for _, f := range severe {
				_, _ = fmt.Fprintln(c.OutOrStdout(), f)
			}
			if len(severe) > 0 {
				return exitError{code: 1, err: fmt.Errorf("%d results of medium security severity or above", len(severe))}
			}
			say(c, "no result of medium security severity or above")
			return nil
		},
	}, patchedFailed())
	return c
}

// patchedFailedTitle is the issue a failed daily analysis opens: one a day, which failed looks for
// before it opens another.
func patchedFailedTitle(day string) string {
	return "CodeQL failed on main " + day
}

func patchedFailed() *cobra.Command {
	var run, day string
	c := &cobra.Command{
		Use:   "failed --run URL",
		Short: "Open the issue for a failed daily analysis, once a day",
		Long: `Open an issue titled "CodeQL failed on main <day>" pointing at --run, the failed run. codeql.yml
runs once a day on main and not on a pull request, so a red run is seen by nobody unless it opens
an issue (#653). One a day: an issue of that title, open or closed, is not opened again.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			title := patchedFailedTitle(cmp.Or(day, time.Now().UTC().Format(time.DateOnly)))
			url, err := issueTitled(c.Context(), title)
			if err != nil {
				return couldNot(err)
			}
			if url == "" {
				body := "The daily CodeQL run failed: " + run + ". A result on a line a patch added, a touched C file " +
					"the analysis did not read, or a result in this repository's Go of medium security severity or above " +
					"(codeql.yml's header). Fix it, or suppress a Go result that is safe in the source, with its reason (#653).\n"
				if url, err = gh(c.Context(), "issue", "create", "-R", github().Repo, "--title", title, "--body", body); err != nil {
					return couldNot(fmt.Errorf("opening %q: %w", title, err))
				}
			}
			say(c, "%s: %s", title, url)
			return nil
		},
	}
	c.Flags().StringVar(&run, "run", "", "the URL of the run that failed")
	c.Flags().StringVar(&day, "day", "", "the day the issue is named for, YYYY-MM-DD (default today, UTC)")
	_ = c.MarkFlagRequired("run")
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

func reportsCmd() *cobra.Command {
	var run string
	c := &cobra.Command{
		Use:   "reports",
		Short: "Name each sanitizer report a sanitized QEMU wrote, and the patch it stopped in (exits 1 on one)",
		Long: `Read the logs a QEMU that sanitized or fuzzer built wrote under <out>/patched/qemu/sanitizer and
print each error its sanitizers reported, with the innermost frame in the tree and the patches whose
file holds it, or upstream. Exits 1 on any report, whoever's code it is in, and 2 when the logs record
no sanitized process: a run that started none checked nothing. With --file-issues, each report whose
kind and place no issue is titled with yet is opened as one, citing the run.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			w, p, dir, err := patchedPart("qemu")
			if err != nil {
				return err
			}
			reports, ran, err := patched.Reports(c.Context(), w.root, p, dir, filepath.Join(dir, "sanitizer"))
			if err != nil {
				return couldNot(err)
			}
			for _, r := range reports {
				_, _ = fmt.Fprintln(c.OutOrStdout(), r)
			}
			if ran == 0 {
				return couldNot(fmt.Errorf("no sanitized QEMU wrote to %s: nothing was checked", filepath.Join(dir, "sanitizer")))
			}
			if len(reports) == 0 {
				say(c, "qemu: %d sanitized QEMU processes, no report", ran)
				return nil
			}
			if run != "" {
				for _, r := range reports {
					if err := fileReport(c, r, run); err != nil {
						return couldNot(err)
					}
				}
			}
			return exitError{code: 1, err: fmt.Errorf("%d sanitizer reports from %d QEMU processes", len(reports), ran)}
		},
	}
	c.Flags().StringVar(&run, "file-issues", "", "the run's URL: open an issue for each report no issue is titled with yet")
	return c
}

// fileReport opens an issue for r unless one has its title: its kind and innermost place, so a
// weekly run that finds it again does not open it again.
func fileReport(c *cobra.Command, r patched.Report, run string) error {
	title := "sanitize: " + r.Title()
	if url, err := issueTitled(c.Context(), title); err != nil || url != "" {
		return err
	}
	where := "upstream's code: no frame is in a file qemu/patches touches"
	if r.Patch != "" {
		where = r.Patch
	}
	body := "sanitize.yml's sanitized QEMU reported this (" + run + ", #653).\n\n" +
		"- report: " + r.Kind + "\n- stopped in: " + where + "\n- log: " + filepath.Base(r.Log) + " in the run's artifact\n\n" +
		"A report in a carried patch is fixed in the patch; one in upstream's code is reported upstream and recorded here."
	url, err := gh(c.Context(), "issue", "create", "-R", github().Repo, "--title", title, "--body", body)
	if err != nil {
		return fmt.Errorf("opening %q: %w", title, err)
	}
	say(c, "%s: %s", title, url)
	return nil
}

// sanitizerLogs empties the directory a sanitized QEMU writes its logs to, beside its tree.
func sanitizerLogs(dir string) (string, error) {
	logs := filepath.Join(dir, "sanitizer")
	if err := os.RemoveAll(logs); err != nil {
		return "", err
	}
	return logs, os.MkdirAll(logs, 0o777)
}

// traced is the parts analysed by tracing their compile.
func traced() []string {
	var names []string
	for _, p := range patched.Parts {
		if p.Traced() {
			names = append(names, p.Name)
		}
	}
	return names
}
