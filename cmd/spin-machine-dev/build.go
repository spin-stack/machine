// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"

	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/spin-stack/machine/internal/build"
)

// buildFlags are the choices a build takes.
type buildFlags struct {
	jobs       int
	registry   bool
	export     bool
	experiment string
	rebuild    bool
}

func (f *buildFlags) register(c *cobra.Command) {
	c.Flags().IntVar(&f.jobs, "jobs", 8, "compile jobs each build runs (CI: its runner's cores)")
	c.Flags().BoolVar(&f.registry, "cache-registry", false, "read and write BuildKit's cache in "+build.CacheRegistry+" (CI) instead of under .cache/")
	c.Flags().BoolVar(&f.export, "cache-export", true, "write the cache back; only main's lanes need to")
	c.Flags().StringVar(&f.experiment, "systemd-experiment", "", "build systemd with image/systemd/experiments/<name>.patch")
	c.Flags().BoolVar(&f.rebuild, "rebuild", false, "build what is named even when its manifest says it is current")
}

func (f buildFlags) options(c *cobra.Command, w workspace) build.Options {
	return build.Options{Root: w.root, Out: w.out, Pins: w.pins, Jobs: f.jobs, Registry: f.registry, Export: f.export,
		SystemdExperiment: f.experiment, Rebuild: f.rebuild, Stdout: c.ErrOrStderr(), Stderr: c.ErrOrStderr()}
}

// parts is what a verb was asked for, every part when nothing was, and whether spin-machine is in it.
func parts(args []string) (names []string, tools bool) {
	if len(args) == 0 {
		return nil, true
	}
	for _, a := range args {
		if a == "spin-machine" {
			tools = true
			continue
		}
		names = append(names, a)
	}
	return names, tools
}

func buildCmd() *cobra.Command {
	var f buildFlags
	c := &cobra.Command{
		Use:   "build [part...]",
		Short: "Build the machine's parts that are missing or stale into _output, and verify each",
		Long: `Build the machine's parts into _output: ` + strings.Join(build.Names(), ", ") + ` and spin-machine,
every one when none is named, with what a named part is made with.

A part is built when it is missing or stale. Each one leaves a manifest in _output/manifest naming a
digest of its inputs - the build arguments versions.yaml gives its Dockerfile and every file under
its directory - and one whose inputs have not moved, and whose output still verifies, is left as it
is. Every part is verified after it is built: the binaries run here, the kernel is a relocatable
bzImage, the tools are the pinned version (verify says what each checks).

In a workflow it writes what each part is to the run's summary, and qemu_version to the step's
outputs.`,
		ValidArgs: append(build.Names(), "spin-machine"),
		Args:      cobra.OnlyValidArgs,
		RunE: func(c *cobra.Command, args []string) error {
			w, err := open()
			if err != nil {
				return err
			}
			o := f.options(c, w)
			names, tools := parts(args)
			var results []build.Result
			if len(names) > 0 || len(args) == 0 {
				plan, err := build.Plan(names)
				if err != nil {
					return couldNot(err)
				}
				if results, err = build.Make(c.Context(), o, plan); err != nil {
					return couldNot(err)
				}
				if err := describe(o, results); err != nil {
					return couldNot(err)
				}
			}
			if tools {
				if err := build.Tools(c.Context(), o); err != nil {
					return couldNot(fmt.Errorf("building spin-machine: %w", err))
				}
				results = append(results, build.Result{Part: "spin-machine", From: "build", Built: true})
			}
			for _, r := range results {
				if r.Built {
					say(c, "✓ %s", r.Part)
				}
			}
			return emit(c.OutOrStdout(), results)
		},
	}
	f.register(c)
	return c
}

// describe writes what the built parts are to the run's summary and the step's outputs.
func describe(o build.Options, results []build.Result) error {
	for _, r := range results {
		lines, err := build.Summary(o, r.Part)
		if err != nil {
			return err
		}
		if len(lines) > 0 {
			if err := summary(strings.Join(lines, "\n") + "\n"); err != nil {
				return err
			}
		}
		if r.Part == "qemu" {
			v, err := build.QEMUVersion(o)
			if err != nil {
				return err
			}
			if err := output("qemu_version", v); err != nil {
				return err
			}
		}
	}
	return nil
}

func fetchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "fetch qemu",
		Short: "Put the published QEMU of the pinned version into _output instead of building it",
		Long: `Put the QEMU main published for the version versions.yaml pins into _output: the same tree a
build writes, verified the same way, in seconds against tens of minutes - the binaries are static,
so the image around them carries nothing they need. Needs a docker login that can read packages.

The manifest it leaves says it was fetched, so a build that wants this tree's QEMU builds it.`,
		ValidArgs: []string{"qemu"},
		Args:      cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		RunE: func(c *cobra.Command, _ []string) error {
			w, err := open()
			if err != nil {
				return err
			}
			if err := build.FetchQEMU(c.Context(), buildFlags{}.options(c, w)); err != nil {
				return couldNot(err)
			}
			say(c, "✓ qemu, fetched")
			return nil
		},
	}
}

func verifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "verify [part|args...]",
		Short: "Assert what is in _output is what each part has to be, and that QEMU accepts the machine's arguments",
		Long: `Assert what is in _output, building nothing:

  qemu       qemu-system-x86_64, its TCG twin and qemu-img run here, the six firmware files are
             there and qboot.bin is 64 KiB, only the TCG binary can emulate, both still offer
             the machine type every checkpoint is saved on (a newer one is said, not failed), and
             the TCG binary's query-qmp-schema has every QMP command and event the module
             defines, as the module defines it
  kernel     a bzImage whose setup header says relocatable, and the vmlinux it was made from
  e2fsprogs  the four tools and mke2fs.conf, and mkfs.ext4 answers the pinned version
  systemd    the packages the image installs
  openssh    the packages the image installs
  image      rootfs.qcow2, packages.txt and licenses.txt (image/build.sh asserts what is inside)
  args       the TCG QEMU accepts every interesting command line the machine package builds: each
             is started, stopped before its first instruction, and asked to answer on QMP; and
             each patch qemu/patches carries does what it is carried for (0005's is boot:patches')

Every part when none is named, and args with them. args refuses rather than skipping when there is
no QEMU to ask: spin-machine-dev fetch qemu (seconds) or build qemu (tens of minutes) puts one there.`,
		ValidArgs: append(build.Names(), "args"),
		Args:      cobra.OnlyValidArgs,
		RunE: func(c *cobra.Command, args []string) error {
			w, err := open()
			if err != nil {
				return err
			}
			names, withArgs := without(args, "args")
			if len(args) == 0 {
				names, withArgs = build.Names(), true
			}
			var plan []build.Part
			for _, n := range names {
				p, err := build.Lookup(n)
				if err != nil {
					return couldNot(err)
				}
				plan = append(plan, p)
			}
			if err := build.Verify(buildFlags{}.options(c, w), plan); err != nil {
				return exitError{code: 1, err: err}
			}
			if withArgs {
				return verifyArgs(c, w)
			}
			return nil
		},
	}
}

// verifyArgs asks the TCG QEMU in _output whether it accepts what the machine package builds,
// and what QEMU does with a chain handed over by descriptor. -count=1 because the answer depends on
// a file the test cache does not know about; -v because what was rewritten for the TCG binary, and
// what a host without /dev/vhost-vsock left unchecked, is the part a reader has to see.
func verifyArgs(c *cobra.Command, w workspace) error {
	tcg := filepath.Join(w.out, "bin", "qemu-system-x86_64-tcg")
	if _, err := os.Stat(tcg); err != nil {
		return couldNot(fmt.Errorf("no %s to ask: spin-machine-dev fetch qemu puts the published one there, in seconds", tcg))
	}
	cmd := []string{"test", "./machine", "-run", "TestQEMUAcceptsEveryArgument|TestAChainOverDescriptors|TestEachQEMUPatchDoesItsJob", "-count=1", "-v"}
	return withEnv("SPIN_MACHINE_OUTPUT", w.out, func() error { return command(c.Context(), c, w.root, "go", cmd...) })
}

// withEnv runs f with key set to value, as the program it starts sees it.
func withEnv(key, value string, f func() error) error {
	old, had := os.LookupEnv(key)
	if err := os.Setenv(key, value); err != nil {
		return couldNot(err)
	}
	defer func() {
		if had {
			_ = os.Setenv(key, old)
		} else {
			_ = os.Unsetenv(key)
		}
	}()
	return f()
}

// without is args less name, and whether name was there.
func without(args []string, name string) ([]string, bool) {
	i := slices.Index(args, name)
	if i < 0 {
		return args, false
	}
	return slices.Delete(slices.Clone(args), i, i+1), true
}

func publishCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "publish qemu",
		Short:  "Build and push QEMU's runtime image, tagged with the pinned version, which fetch pulls",
		Hidden: true,
		Long: `Build and push the runtime image, tagged with the pinned QEMU version, to the image
fetch qemu pulls. main's parts.yml runs it, with a docker login that can write packages.`,
		ValidArgs: []string{"qemu"},
		Args:      cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		RunE: func(c *cobra.Command, _ []string) error {
			w, err := open()
			if err != nil {
				return err
			}
			if err := build.PublishQEMU(c.Context(), buildFlags{}.options(c, w)); err != nil {
				return couldNot(err)
			}
			return nil
		},
	}
}

func changedCmd() *cobra.Command {
	var base string
	var parts []string
	c := &cobra.Command{
		Use:    "changed --base <commit> --part <part>... [path...]",
		Short:  "Say whether the commits since a base can change what a part builds",
		Hidden: true,
		Long: `Say whether the commits since --base can change what the parts named build: true when
anything under their paths, the parts they are made with, or the paths named changed, or when
versions.yaml hands their pins other build arguments than it did at the base. The pins and paths
are internal/build's, so a workflow names the part and only what it adds, and a part's pin added to
internal/build reaches every workflow at once. A workflow's path filter cannot say which lines of
versions.yaml moved; this can, so a bump of e2fsprogs does not rebuild QEMU and the kernel.

Prints build=true or build=false, and writes it to the step's outputs. It errs towards building: no
base, a base this clone lacks, or a versions.yaml there it cannot read is true.`,
		RunE: func(c *cobra.Command, extra []string) error {
			w, err := open()
			if err != nil {
				return err
			}
			if len(parts) == 0 {
				return couldNot(fmt.Errorf("--part names none: say which parts (%s) the change is to reach", strings.Join(build.Names(), ", ")))
			}
			plan, err := build.Plan(parts)
			if err != nil {
				return couldNot(err)
			}
			var pins, paths []string
			for _, p := range plan {
				pins = append(pins, p.Pins...)
				paths = append(paths, p.Paths...)
			}
			touched := build.Touches(c.Context(), w.root, base, w.pins, pins, append(paths, extra...))
			_, _ = fmt.Fprintf(c.OutOrStdout(), "build=%t\n", touched)
			return output("build", fmt.Sprint(touched))
		},
	}
	c.Flags().StringVar(&base, "base", "", "the commit the change is to; none builds")
	c.Flags().StringSliceVar(&parts, "part", nil, "the parts whose pins and paths decide it, with the parts they are made with")
	return c
}

func needsCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "needs",
		Short:  "Say whether every job a workflow's last job needs passed or was skipped (exits 1 when one did not)",
		Hidden: true,
		Long: `Say whether every job a workflow's last job needs passed or was skipped, from $NEEDS, the
job's toJSON(needs). A skipped job is one the change did not reach (parts.yml's changed), which is
a pass; a job that failed or was cancelled is not, and neither is $NEEDS empty or unreadable: a
summary that passes on nothing is the check a ruleset trusts saying yes to a run it did not see.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			results, err := needsResults(os.Getenv("NEEDS"))
			if err != nil {
				return couldNot(err)
			}
			var failed []string
			for _, job := range slices.Sorted(maps.Keys(results)) {
				say(c, "%s: %s", job, results[job])
				if r := results[job]; r != "success" && r != "skipped" {
					failed = append(failed, job+" "+r)
				}
			}
			if len(failed) > 0 {
				return exitError{code: 1, err: fmt.Errorf("not every job passed: %s", strings.Join(failed, ", "))}
			}
			return nil
		},
	}
}

// needsResults is each needed job's result, from a job's toJSON(needs).
func needsResults(raw string) (map[string]string, error) {
	var needs map[string]struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal([]byte(raw), &needs); err != nil {
		return nil, fmt.Errorf("reading NEEDS, the job's toJSON(needs): %w", err)
	}
	if len(needs) == 0 {
		return nil, errors.New("NEEDS names no job: set it to the job's toJSON(needs)")
	}
	results := map[string]string{}
	for job, n := range needs {
		results[job] = n.Result
	}
	return results, nil
}
