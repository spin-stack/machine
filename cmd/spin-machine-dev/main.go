// SPDX-License-Identifier: Apache-2.0

// spin-machine-dev builds, measures and releases this machine. It is the command a person and a
// workflow run to work on the repository; cmd/spin-machine is the machine's own tool, the one a
// release ships and spin runs by hand, and this one is never shipped.
//
// The principles are spin-os's (spin-stack/ami docs/cli.md): a verb is a goal, state is files
// under _output with a manifest, progress goes to stderr and --json to stdout, and the exit
// status means the same everywhere - 0 done, 1 done with something to act on, 2 could not do it.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/spin-stack/machine/internal/lab"
)

func main() {
	os.Exit(exit(run(), os.Stderr))
}

// exitError is an exit status a verb chose, and what it says: a comparison's 1 for a regression
// is not a failure to say anything about.
type exitError struct {
	code int
	err  error
}

func (e exitError) Error() string {
	if e.err == nil {
		return fmt.Sprintf("exit status %d", e.code)
	}
	return e.err.Error()
}

func (e exitError) Unwrap() error { return e.err }

// couldNot is err as status 2: the verb could not do what it was asked.
func couldNot(err error) error { return exitError{code: 2, err: err} }

// exitCode is the status err exits with: none, a verb's own, or 2 for anything else, which is a
// verb that could not finish.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var e exitError
	if errors.As(err, &e) {
		return e.code
	}
	return 2
}

// exit says err on w, unless it is only a status, and answers the status to exit with.
func exit(err error, w io.Writer) int {
	var e exitError
	if err != nil && (!errors.As(err, &e) || e.err != nil) {
		_, _ = fmt.Fprintf(w, "spin-machine-dev: %v\n", err)
	}
	return exitCode(err)
}

// run is the command, its signal handling undone before main exits: os.Exit runs no defer.
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return root().ExecuteContext(ctx)
}

// root is the command and its verbs, each made fresh so that its flags start at their defaults.
func root() *cobra.Command {
	c := &cobra.Command{
		Use:           "spin-machine-dev",
		Short:         "Build, measure and release spin-machine",
		SilenceUsage:  true,
		SilenceErrors: true,
		// A shell's completion is not a verb anybody here asked for.
		CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
	}
	addGlobals(c)
	c.AddCommand(
		buildCmd(), fetchCmd(), verifyCmd(), publishCmd(), changedCmd(), needsCmd(),
		labCommand(lab.Probes),
		shellCmd(), vsockCmd("/dev/vhost-vsock"), menuconfigCmd(), cleanCmd(),
		outdatedCmd(), bumpCmd(), refreshCmd(),
		releaseCmd(),
		releasesCommand(), docsCommand(),
		osvCmd(), cvesCmd(),
		patchedCmd(),
		failedCmd(),
	)
	return c
}
