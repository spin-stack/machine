// SPDX-License-Identifier: Apache-2.0

package main

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/spin-stack/spin-machine/internal/lab"
)

func init() { verbs = append(verbs, labCommand) }

// labCommand is measuring: trees, the probes that boot them, and the bench fleet that runs them.
func labCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "lab",
		Short: "Measure the machine: make trees, run probes over them, here or on the bench fleet",
		Long: `A tree is a whole machine in _output/trees/NAME, every part of it named in its manifest.json:
a release, with a kernel or a QEMU from a CI run, this checkout's spin-machine, an edit of every
boot's overlay. A probe boots trees and records numbers; an experiment is a probe over trees,
repetitions of each, interleaved, written to result.json.

Probes: ` + strings.Join(lab.ProbeNames(), ", ") + `.

A number is only worth comparing on a quiet host of the kind the goal is measured on: the bench
fleet, through lab dispatch. Here, lab run finds leads.`,
	}
	c.AddCommand(labTree(), labRun(), labShow(), labDispatch(), labCheck())
	return c
}

// treeFlags are a tree spec's flags, the same for lab tree and inside a --tree NAME=SPEC.
func treeFlags(fs *pflag.FlagSet, s *lab.TreeSpec) {
	fs.StringVar(&s.From, "from", "release", "what the tree starts from: release, release:vYYYYMMDD.NN, or a directory laid out as one (_output)")
	fs.StringVar(&s.Kernel, "kernel", "", "the kernel instead: run:N (a Kernel workflow run) or a bzImage")
	fs.StringVar(&s.QEMU, "qemu", "", "QEMU and its firmware instead: run:N (a QEMU workflow run) or a directory with bin/ and qemu/")
	fs.StringVar(&s.SpinMachine, "spin-machine", "", "here: built from this checkout; left out, the --from tree's own when it has one, else this checkout's (a release ships none)")
	fs.StringVar(&s.Append, "append", "", "added to the kernel command line of every boot")
	fs.StringArrayVar(&s.Edits, "edit", nil, "an edit of every boot's overlay, by name: lane (the first login's lane as this checkout builds it)")
	fs.StringVar(&s.Systemd, "systemd", "", "an image/systemd/experiments patch: systemd built with it here, in every overlay")
}

// parseTreeSpec reads NAME=FLAGS, the FLAGS split as a shell would.
func parseTreeSpec(arg string) (lab.TreeSpec, error) {
	name, flags, ok := strings.Cut(arg, "=")
	if !ok || name == "" {
		return lab.TreeSpec{}, fmt.Errorf("a tree is NAME=FLAGS, e.g. 'kaslr=--kernel run:44584704610', not %q", arg)
	}
	words, err := splitWords(flags)
	if err != nil {
		return lab.TreeSpec{}, fmt.Errorf("tree %s: %w", name, err)
	}
	s := lab.TreeSpec{Name: strings.TrimSpace(name)}
	fs := pflag.NewFlagSet(name, pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	treeFlags(fs, &s)
	if err := fs.Parse(words); err != nil {
		return lab.TreeSpec{}, fmt.Errorf("tree %s: %w", name, err)
	}
	if fs.NArg() > 0 {
		return lab.TreeSpec{}, fmt.Errorf("tree %s: %q is not a flag", name, fs.Arg(0))
	}
	return s, nil
}

// splitWords splits s on spaces, keeping what is inside single or double quotes together.
func splitWords(s string) ([]string, error) {
	var words []string
	var cur strings.Builder
	var quote rune
	in := false
	for _, r := range s {
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
			continue
		}
		switch {
		case r == '\'' || r == '"':
			quote, in = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if in {
				words, in = append(words, cur.String()), false
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
			in = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("an unclosed %c in %q", quote, s)
	}
	if in {
		words = append(words, cur.String())
	}
	return words, nil
}

// splitList splits each value on ';' and drops the blanks: a workflow input carries several
// trees or options in one string.
func splitList(values []string) []string {
	var out []string
	for _, v := range values {
		for part := range strings.SplitSeq(v, ";") {
			if p := strings.TrimSpace(part); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

func github() *lab.GitHub {
	return &lab.GitHub{Repo: cmp.Or(os.Getenv("GITHUB_REPOSITORY"), "spin-stack/spin-machine")}
}

func sources(root string, log io.Writer) lab.Sources {
	return lab.Sources{Checkout: root, Cache: filepath.Join(root, "_output", "lab"), GitHub: github(), Log: log}
}

func labTree() *cobra.Command {
	var s lab.TreeSpec
	c := &cobra.Command{
		Use:   "tree NAME",
		Short: "Make the tree NAME in _output/trees: a release, with the parts asked for replaced",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			root, err := repo()
			if err != nil {
				return couldNot(err)
			}
			s.Name = args[0]
			t, err := lab.MakeTree(filepath.Join(root, "_output", "trees", s.Name), s, sources(root, c.ErrOrStderr()))
			if err != nil {
				return couldNot(err)
			}
			_, _ = fmt.Fprintln(c.OutOrStdout(), t.Dir)
			return nil
		},
	}
	treeFlags(c.Flags(), &s)
	return c
}

// trees are the trees an experiment measures: each NAME=SPEC made, each bare NAME opened.
func trees(root string, names, specs []string, log io.Writer) ([]*lab.Tree, error) {
	var out []*lab.Tree
	for _, spec := range splitList(specs) {
		s, err := parseTreeSpec(spec)
		if err != nil {
			return nil, err
		}
		t, err := lab.MakeTree(filepath.Join(root, "_output", "trees", s.Name), s, sources(root, log))
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	for _, name := range names {
		t, err := lab.OpenTree(filepath.Join(root, "_output", "trees", name))
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// options reads k=v pairs.
func options(pairs []string) (lab.Options, error) {
	o := lab.Options{}
	for _, p := range splitList(pairs) {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			return nil, fmt.Errorf("an option is NAME=VALUE, not %q", p)
		}
		o[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return o, nil
}

func labRun() *cobra.Command {
	var specs, opts []string
	var reps int
	var out string
	c := &cobra.Command{
		Use:   "run PROBE [TREE...]",
		Short: "Run a probe over trees, interleaved, and write result.json",
		Long: `Run PROBE over every tree, REPS times each, interleaved, and write what it recorded to
OUT/result.json and its table to stdout. Trees are names in _output/trees, or made here with
--tree NAME=SPEC (lab tree's flags: --tree 'kaslr=--kernel run:44584704610'); ';' separates
several in one value. Exits 1 when a measurement was lost, 2 when the experiment could not run.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			p, ok := lab.Probes[args[0]]
			if !ok {
				return couldNot(fmt.Errorf("no probe %q; the probes are: %s", args[0], strings.Join(lab.ProbeNames(), ", ")))
			}
			root, err := repo()
			if err != nil {
				return couldNot(err)
			}
			o, err := options(opts)
			if err != nil {
				return couldNot(err)
			}
			ts, err := trees(root, args[1:], specs, c.ErrOrStderr())
			if err != nil {
				return couldNot(err)
			}
			scratch, err := lab.Scratch(filepath.Join(root, "_output", "lab"))
			if err != nil {
				return couldNot(err)
			}
			if out == "" {
				out = filepath.Join(root, "_output", "lab", "results", time.Now().UTC().Format("20060102-150405")+"-"+p.Name())
			}
			if err := os.MkdirAll(out, 0o755); err != nil {
				return couldNot(fmt.Errorf("making %s: %w", out, err))
			}
			r, err := lab.Experiment{Probe: p, Trees: ts, Reps: reps, Options: o, Scratch: scratch, Log: c.ErrOrStderr()}.Run(c.Context())
			if err != nil {
				return couldNot(err)
			}
			if err := r.Write(filepath.Join(out, "result.json")); err != nil {
				return couldNot(err)
			}
			return report(c.OutOrStdout(), r)
		},
	}
	c.Flags().StringArrayVar(&specs, "tree", nil, "a tree made for this run, NAME=SPEC")
	c.Flags().StringArrayVar(&opts, "opt", nil, "a probe option, NAME=VALUE (cpus, memory, flags, and the probe's own)")
	c.Flags().IntVar(&reps, "reps", 10, "repetitions of each tree")
	c.Flags().StringVar(&out, "out", "", "where result.json goes (default _output/lab/results/TIME-PROBE)")
	return c
}

// report prints r's table and the probe's detail, on the run's summary too in a workflow, and
// says 1 when a measurement was lost.
func report(w io.Writer, r *lab.Result) error {
	text := r.Table()
	if d, ok := lab.Probes[r.Probe].(lab.Detailer); ok {
		text += d.Detail(r)
	}
	_, _ = fmt.Fprintln(w, text)
	if f := os.Getenv("GITHUB_STEP_SUMMARY"); f != "" {
		if s, err := os.OpenFile(f, os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			_, _ = fmt.Fprintf(s, "```\n%s\n```\n", text)
			_ = s.Close()
		}
	}
	for _, s := range r.Samples {
		if s.Missed != "" {
			return exitError{code: 1}
		}
	}
	return nil
}

func labShow() *cobra.Command {
	return &cobra.Command{
		Use:   "show RUN_ID|DIR",
		Short: "Print the result of a lab.yml run, or of a result.json in DIR",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			path, err := resultOf(args[0])
			if err != nil {
				return couldNot(err)
			}
			r, err := lab.ReadResult(path)
			if err != nil {
				return couldNot(err)
			}
			return report(c.OutOrStdout(), r)
		},
	}
}

// resultOf is the result.json arg names: one in a directory, or a lab.yml run's, downloaded.
//
// mutate-lane: TestLabDispatchMeasuresOnTheBench, which reads its run's result through it.
func resultOf(arg string) (string, error) {
	if _, err := strconv.ParseInt(arg, 10, 64); err != nil {
		return filepath.Join(arg, "result.json"), nil
	}
	root, err := repo()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, "_output", "lab", "runs", arg, "result")
	if _, err := os.Stat(filepath.Join(dir, "result.json")); err != nil {
		if err := github().Artifact(arg, "lab-"+arg, dir); err != nil {
			return "", err
		}
	}
	return filepath.Join(dir, "result.json"), nil
}

// mutate-lane: TestLabDispatchMeasuresOnTheBench
func labDispatch() *cobra.Command {
	var specs, opts []string
	var reps int
	var host, ref string
	c := &cobra.Command{
		Use:   "dispatch PROBE",
		Short: "Run a probe on the bench fleet through lab.yml, one lab at a time, and print its result",
		Long: `Dispatch lab.yml with PROBE over the trees given, once no other lab run is queued or running -
the fleet measures one at a time, and two runs side by side measure each other - then wait for it
and print its result. Each --tree is NAME=SPEC, made on the runner.`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if _, ok := lab.Probes[args[0]]; !ok {
				return couldNot(fmt.Errorf("no probe %q; the probes are: %s", args[0], strings.Join(lab.ProbeNames(), ", ")))
			}
			if len(specs) == 0 {
				return couldNot(errors.New("dispatch measures the trees it is given: --tree NAME=SPEC"))
			}
			for _, s := range splitList(specs) {
				if _, err := parseTreeSpec(s); err != nil {
					return couldNot(err)
				}
			}
			if _, err := options(opts); err != nil {
				return couldNot(err)
			}
			gh := github()
			run, err := gh.Dispatch(c.Context(), ref, map[string]string{
				"probe": args[0], "trees": strings.Join(splitList(specs), "; "), "reps": strconv.Itoa(reps),
				"options": strings.Join(splitList(opts), "; "), "host": host,
			}, c.ErrOrStderr())
			if err != nil {
				return couldNot(err)
			}
			_, _ = fmt.Fprintf(c.ErrOrStderr(), "lab run %d: %s\n", run.ID, run.URL)
			if run, err = gh.Wait(c.Context(), run.ID); err != nil {
				return couldNot(err)
			}
			path, err := resultOf(strconv.FormatInt(run.ID, 10))
			if err != nil {
				return couldNot(fmt.Errorf("lab run %d %s and left no result: %w", run.ID, run.Conclusion, err))
			}
			r, err := lab.ReadResult(path)
			if err != nil {
				return couldNot(err)
			}
			return report(c.OutOrStdout(), r)
		},
	}
	c.Flags().StringArrayVar(&specs, "tree", nil, "a tree to measure, NAME=SPEC (lab tree's flags)")
	c.Flags().StringArrayVar(&opts, "opt", nil, "a probe option, NAME=VALUE")
	c.Flags().IntVar(&reps, "reps", 20, "repetitions of each tree")
	c.Flags().StringVar(&host, "host", "bench", "bench (nested KVM) or bench-metal (bare metal, one at a time across the org)")
	c.Flags().StringVar(&ref, "ref", "main", "the branch whose lab.yml and probes run")
	return c
}

// mutate-lane: TestLabCheckRunsABootTarget
func labCheck() *cobra.Command {
	var spec string
	c := &cobra.Command{
		Use:   "check TARGET",
		Short: "Run a boot/ Task target - a pass/fail check, or a probe not yet a lab probe - on one tree",
		Long: `Run boot:TARGET with the tree as the release it boots (SPIN_MACHINE_OUTPUT): the functional
checks (firmware, roms, compat, hotadd, logind, lockdown) and the probes still Go tests
(firmware-stages, waits), on a tree made like any other. Exits with the target's status.`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			root, err := repo()
			if err != nil {
				return couldNot(err)
			}
			ts, err := trees(root, nil, []string{spec}, c.ErrOrStderr())
			if err != nil {
				return couldNot(err)
			}
			cmd := exec.CommandContext(c.Context(), "go", "tool", "task", "boot:"+args[0], "OUTPUT_DIR="+ts[0].Dir)
			cmd.Dir, cmd.Stdout, cmd.Stderr = root, c.OutOrStdout(), c.ErrOrStderr()
			cmd.Env = append(os.Environ(), "SPIN_MACHINE_OUTPUT="+ts[0].Dir)
			if err := cmd.Run(); err != nil {
				return exitError{code: 1, err: fmt.Errorf("boot:%s on tree %s: %w", args[0], ts[0].Name(), err)}
			}
			return nil
		},
	}
	c.Flags().StringVar(&spec, "tree", "release=--from release", "the tree, NAME=SPEC")
	return c
}
