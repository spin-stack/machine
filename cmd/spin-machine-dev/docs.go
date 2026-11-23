// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/spin-stack/machine/internal/docsrec"
)

// docsCommand renders the pages that the log's entries and the experiments' records add up to.
// The pages are never committed (spin-stack/machine#709).
func docsCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "docs",
		Short: "The log and boot-time.md's tables, rendered from docs/log and docs/experiments",
		Long: `The log is one file an entry under docs/log, and the tables of docs/boot-time.md are one record
a row under docs/experiments. A pull request adds files of its own, so two in flight never touch
the same one. log and tables render the pages to stdout or --out; site writes both as web pages,
which pages.yml publishes beside the releases' page. Every verb refuses a file that does not parse.`,
	}
	c.AddCommand(docsLog(), docsTables(), docsSite(), docsSplit())
	return c
}

// readLog is the repository's log.
func readLog() (docsrec.Log, error) {
	root, err := repo()
	if err != nil {
		return docsrec.Log{}, err
	}
	l, err := docsrec.ReadLog(filepath.Join(root, docsrec.LogDir))
	if err != nil {
		return docsrec.Log{}, couldNot(err)
	}
	return l, nil
}

// readRecords is the repository's experiments.
func readRecords() ([]docsrec.Record, error) {
	root, err := repo()
	if err != nil {
		return nil, err
	}
	records, err := docsrec.ReadRecords(filepath.Join(root, docsrec.ExperimentsDir))
	if err != nil {
		return nil, couldNot(err)
	}
	return records, nil
}

// writePage writes text to out, or to stdout when out is empty.
func writePage(c *cobra.Command, out, text string) error {
	if out == "" {
		_, err := fmt.Fprint(c.OutOrStdout(), text)
		return err
	}
	if err := os.WriteFile(out, []byte(text), 0o644); err != nil { //nolint:gosec // a page to read
		return couldNot(fmt.Errorf("writing %s: %w", out, err))
	}
	return nil
}

func docsLog() *cobra.Command {
	var out string
	c := &cobra.Command{
		Use:   "log [--out FILE]",
		Short: "The log as one page: docs/log's head, its entries by date under their weeks, and rework.md",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			l, err := readLog()
			if err != nil {
				return err
			}
			return writePage(c, out, l.Markdown())
		},
	}
	c.Flags().StringVar(&out, "out", "", "the file to write; stdout when not given")
	return c
}

func docsTables() *cobra.Command {
	var out string
	c := &cobra.Command{
		Use:   "tables [--out FILE]",
		Short: "boot-time.md's tables, carried, measured and rejected, and open, from docs/experiments",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			records, err := readRecords()
			if err != nil {
				return err
			}
			return writePage(c, out, docsrec.TablesMarkdown(records))
		},
	}
	c.Flags().StringVar(&out, "out", "", "the file to write; stdout when not given")
	return c
}

func docsSite() *cobra.Command {
	var out string
	c := &cobra.Command{
		Use:   "site --out OUT",
		Short: "Write the log and the tables as web pages, OUT/log.html and OUT/experiments.html",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			l, err := readLog()
			if err != nil {
				return err
			}
			records, err := readRecords()
			if err != nil {
				return err
			}
			if err := os.MkdirAll(out, 0o755); err != nil {
				return couldNot(fmt.Errorf("making %s: %w", out, err))
			}
			for _, p := range []struct{ name, title, markdown string }{
				{"log.html", "Log", l.Markdown()},
				{"experiments.html", "Experiments", docsrec.TablesMarkdown(records)},
			} {
				html, err := docsrec.HTML(p.title, p.markdown)
				if err != nil {
					return couldNot(err)
				}
				if err := writePage(c, filepath.Join(out, p.name), html); err != nil {
					return err
				}
			}
			say(c, "wrote %s/log.html (%d entries) and %s/experiments.html (%d records)", out, len(l.Entries), out, len(records))
			return nil
		},
	}
	c.Flags().StringVar(&out, "out", "", "the directory the pages are written in")
	_ = c.MarkFlagRequired("out")
	return c
}

func docsSplit() *cobra.Command {
	var kinds string
	c := &cobra.Command{
		Use:   "split --kinds FILE",
		Short: "Turn docs/log.md and boot-time.md's tables into docs/log and docs/experiments, once (#709)",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			root, err := repo()
			if err != nil {
				return err
			}
			read := func(p string) (string, error) {
				data, err := os.ReadFile(filepath.Join(root, p))
				if err != nil {
					return "", couldNot(fmt.Errorf("reading %s: %w", p, err))
				}
				return string(data), nil
			}
			k := docsrec.Kinds{}
			list, err := os.ReadFile(kinds)
			if err != nil {
				return couldNot(err)
			}
			for _, l := range strings.Split(strings.TrimSpace(string(list)), "\n") {
				n, kind, _ := strings.Cut(l, " ")
				i, err := strconv.Atoi(n)
				if err != nil {
					return couldNot(fmt.Errorf("%s: %q is not N issue|pr", kinds, l))
				}
				k[i] = kind
			}
			log, err := read("docs/log.md")
			if err != nil {
				return err
			}
			bootTime, err := read("docs/boot-time.md")
			if err != nil {
				return err
			}
			files, err := docsrec.Split(log, bootTime, k)
			if err != nil {
				return couldNot(err)
			}
			for name, data := range files {
				p := filepath.Join(root, name)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					return couldNot(err)
				}
				if err := os.WriteFile(p, data, 0o644); err != nil { //nolint:gosec // documentation
					return couldNot(err)
				}
			}
			say(c, "wrote %d files", len(files))
			return nil
		},
	}
	c.Flags().StringVar(&kinds, "kinds", "", "lines of N issue|pr, every number of the repository")
	return c
}
