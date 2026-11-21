// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/spin-stack/spin-machine/internal/release"
	"github.com/spin-stack/spin-machine/internal/scan"
)

// newestRelease is the newest published release's tag: what anything takes next, so what is
// scanned. One still pinned somewhere is its consumer's to scan.
func newestRelease(ctx context.Context) (string, error) {
	return gh(ctx, "release", "view", "--json", "tagName", "-q", ".tagName")
}

func osvCmd() *cobra.Command {
	var issues bool
	c := &cobra.Command{
		Use:   "osv",
		Short: "Say what OSV knows against the newest release's SBOM, once its signature is the release's",
		Long: `Say what OSV knows against the newest release's SBOM, once the SBOM's signature is the
release's: one line a finding - id, package@version and summary - in _output/osv.tsv. osv.yml runs it
daily.

--issues brings the repository's issues to the findings: one open for each package with findings,
closed once the newest release has none - a package and not a finding, because a package is what
gets fixed. Needs gh and the network.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			w, err := open()
			if err != nil {
				return err
			}
			tag, err := newestRelease(c.Context())
			if err != nil {
				return couldNot(err)
			}
			findings := filepath.Join(w.out, "osv.tsv")
			n, err := osvScan(c.Context(), w, tag, findings)
			if err != nil {
				return couldNot(err)
			}
			say(c, "%s: %d finding(s), in %s", tag, n, findings)
			if !issues {
				return nil
			}
			if err := scan.SyncIssues(tag, findings, ghLogged, c.OutOrStdout()); err != nil {
				return exitError{code: 1, err: err}
			}
			return nil
		},
	}
	c.Flags().BoolVar(&issues, "issues", false, "bring the repository's issues to the findings")
	return c
}

// ghLogged runs the GitHub CLI, which reads its token and repository from the environment, with
// gh's own words on stderr for the job's log.
func ghLogged(args ...string) ([]byte, error) {
	cmd := exec.Command("gh", args...)
	cmd.Stderr = os.Stderr // mutate-exempt: gh's own words go to the job's log for a person; SyncIssues reads only its exit status and output
	return cmd.Output()
}

// osvScan writes OSV's findings against tag's SBOM to findings, and says how many.
func osvScan(ctx context.Context, w workspace, tag, findings string) (int, error) {
	dir, err := os.MkdirTemp("", "osv")
	if err != nil {
		return 0, fmt.Errorf("making a place for the SBOM: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	sbom, err := release.Signed(ctx, w.pins, tag, dir, release.Name(tag)+".cdx.json")
	if err != nil {
		return 0, err
	}
	e, err := w.pins.Get("osv-scanner")
	if err != nil {
		return 0, err
	}
	cmd := exec.CommandContext(ctx, "go", "run", "github.com/google/osv-scanner/v2/cmd/osv-scanner@"+e.Version,
		"scan", "source", "-L", sbom, "--format", "json")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// osv-scanner exits 1 when it found something, which is the answer; above 1 it failed.
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() > 1 {
			return 0, fmt.Errorf("osv-scanner: %w: %s", err, bytes.TrimSpace(stderr.Bytes()))
		}
	}
	lines, err := OSVFindings(&stdout)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(w.out, 0o755); err != nil {
		return 0, fmt.Errorf("making %s: %w", w.out, err)
	}
	text := strings.Join(lines, "\n")
	if len(lines) > 0 {
		text += "\n"
	}
	if err := os.WriteFile(findings, []byte(text), 0o644); err != nil { //nolint:gosec // findings, for a person and scan.SyncIssues
		return 0, fmt.Errorf("writing %s: %w", findings, err)
	}
	return len(lines), nil
}

// OSVFindings are osv-scanner's JSON as one line a finding - id, package@version, summary, a tab
// between them - sorted, each once: what scan.SyncIssues reads.
func OSVFindings(r io.Reader) ([]string, error) {
	var report struct {
		Results []struct {
			Packages []struct {
				Package struct {
					Name    string `json:"name"`
					Version string `json:"version"`
				} `json:"package"`
				Vulnerabilities []struct {
					ID      string `json:"id"`
					Summary string `json:"summary"`
				} `json:"vulnerabilities"`
			} `json:"packages"`
		} `json:"results"`
	}
	if err := json.NewDecoder(r).Decode(&report); err != nil {
		return nil, fmt.Errorf("reading osv-scanner's report: %w", err)
	}
	var lines []string
	for _, res := range report.Results {
		for _, p := range res.Packages {
			for _, v := range p.Vulnerabilities {
				lines = append(lines, v.ID+"\t"+p.Package.Name+"@"+p.Package.Version+"\t"+v.Summary)
			}
		}
	}
	slices.Sort(lines)
	return slices.Compact(lines), nil
}

func cvesCmd() *cobra.Command {
	var local bool
	c := &cobra.Command{
		Use:   "cves",
		Short: "Say what the kernel CNA's published CVEs mean for the newest release's kernel",
		Long: `Say what the kernel CNA's published CVEs mean for the newest release's kernel - --local: the
kernel in _output, which is how a bump or a config change is judged before it is released - each
fixed, in code the kernel does not compile, or applying and where its fix is. "Not compiled" is read
off the build (kernel/sources, every source and header the build compiled), not the configuration.

The CNA's records are fetched into _output/cves/vulns, or brought to today's; a release's sources
list comes out of its tarball once the tarball is its published sha256 and signed by the release. The summary is
in _output/cves/summary.md and on the run's summary. Needs git and the network; gh for a
release.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			w, err := open()
			if err != nil {
				return err
			}
			dir := filepath.Join(w.out, "cves")
			if err := records(c.Context(), c, filepath.Join(dir, "vulns")); err != nil {
				return couldNot(err)
			}
			var version, sources string
			if local {
				e, err := w.pins.Get("kernel")
				if err != nil {
					return couldNot(err)
				}
				version, sources = e.Version, filepath.Join(w.out, "kernel", "sources")
			} else {
				tag, err := newestRelease(c.Context())
				if err != nil {
					return couldNot(err)
				}
				if version, sources, err = release.Kernel(c.Context(), w.pins, tag, filepath.Join(dir, "release")); err != nil {
					return couldNot(err)
				}
			}
			v, err := scan.ParseKernelVersion(version)
			if err != nil {
				return couldNot(err)
			}
			out, err := scan.KernelCVEs(filepath.Join(dir, "vulns", "cve", "published"), v, sources)
			if err != nil {
				return exitError{code: 1, err: err}
			}
			if err := os.WriteFile(filepath.Join(dir, "summary.md"), []byte(out), 0o644); err != nil { //nolint:gosec // a summary, for a person
				return couldNot(fmt.Errorf("writing the summary: %w", err))
			}
			if err := summary(out); err != nil {
				return couldNot(err)
			}
			for line := range strings.Lines(out) {
				if strings.HasPrefix(line, "|") {
					_, _ = fmt.Fprint(c.OutOrStdout(), line)
				}
			}
			return nil
		},
	}
	c.Flags().BoolVar(&local, "local", false, "judge the kernel in _output instead of the newest release's")
	return c
}

// records is the kernel CNA's published CVE records, as they are today, in dir: data, read the way
// osv reads OSV's database, so not a pin in versions.yaml. Only cve/published, which
// scan.KernelCVEs reads, and not the repository's history.
func records(ctx context.Context, c *cobra.Command, dir string) error {
	run := func(args ...string) error { return command(ctx, c, "", "git", args...) }
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		if err := run("clone", "-q", "--depth", "1", "--sparse", "https://git.kernel.org/pub/scm/linux/security/vulns.git", dir); err != nil {
			return err
		}
		if err := run("-C", dir, "sparse-checkout", "set", "cve/published"); err != nil {
			return err
		}
	}
	if err := run("-C", dir, "fetch", "-q", "--depth", "1", "origin"); err != nil {
		return err
	}
	return run("-C", dir, "reset", "-q", "--hard", "FETCH_HEAD")
}
