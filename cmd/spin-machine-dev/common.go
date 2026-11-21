// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spin-stack/go-tools/versions"
)

// globals are the flags every verb takes.
// defaultOut is where builds land and verbs read when --out says nothing.
const defaultOut = "_output"

var globals = struct {
	// out is where builds land and verbs read: defaultOut, unless said otherwise. It starts there,
	// not empty, so a verb built without root's flags (a test's) keeps its state in the same place.
	out string
	// json writes one JSON document on stdout, for a program; progress stays on stderr.
	json bool
}{out: defaultOut}

// addGlobals registers the flags every verb takes on root.
func addGlobals(c *cobra.Command) {
	c.PersistentFlags().StringVar(&globals.out, "out", defaultOut, "where builds land and verbs read their state")
	c.PersistentFlags().BoolVar(&globals.json, "json", false, "write the result as one JSON document on stdout")
}

// repo is the repository a verb works on: the directory holding versions.yaml, at or above the
// working directory, as Task and git find theirs.
func repo() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", couldNot(fmt.Errorf("finding the repository: %w", err))
	}
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, versions.File)); err == nil {
			return d, nil
		}
		if filepath.Dir(d) == d {
			return "", couldNot(fmt.Errorf("no %s at or above %s: run spin-machine-dev from a checkout", versions.File, dir))
		}
	}
}

// workspace is the repository, its absolute output directory and its pins.
type workspace struct {
	root, out string
	pins      *versions.Versions
}

// outOf is --out, relative to the checkout's root unless it is absolute: where every verb keeps
// its state.
func outOf(root string) string {
	if filepath.IsAbs(globals.out) {
		return globals.out
	}
	return filepath.Join(root, globals.out)
}

func open() (workspace, error) {
	root, err := repo()
	if err != nil {
		return workspace{}, err
	}
	out := outOf(root)
	pins, err := versions.Load(filepath.Join(root, versions.File))
	if err != nil {
		return workspace{}, couldNot(err)
	}
	return workspace{root: root, out: out, pins: pins}, nil
}

// emit writes v as the verb's JSON document when --json asked for one.
func emit(w io.Writer, v any) error {
	if !globals.json {
		return nil
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// output appends key=value to the step's outputs in a workflow, for a later `if:` or name: a
// copy of what the verb says, never the only place it is. A value of several lines is written
// between delimiters, as GitHub reads one.
func output(key, value string) error {
	path := os.Getenv("GITHUB_OUTPUT")
	if path == "" {
		return nil
	}
	line := key + "=" + value + "\n"
	if strings.Contains(value, "\n") {
		b := make([]byte, 8) // mutate-exempt: any length of random delimiter is one no value holds
		_, _ = rand.Read(b)
		delim := "EOF_" + hex.EncodeToString(b)
		line = key + "<<" + delim + "\n" + strings.TrimSuffix(value, "\n") + "\n" + delim + "\n"
	}
	return appendTo(path, line)
}

// summary appends text to the run's summary in a workflow.
func summary(text string) error {
	path := os.Getenv("GITHUB_STEP_SUMMARY")
	if path == "" {
		return nil
	}
	return appendTo(path, text)
}

func appendTo(path, text string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644) //nolint:gosec // the runner's own file
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if _, err := f.WriteString(text); err != nil {
		_ = f.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return f.Close()
}

// say is a line of progress, for a person.
func say(c *cobra.Command, format string, a ...any) {
	_, _ = fmt.Fprintf(c.ErrOrStderr(), format+"\n", a...)
}

// command runs name in dir with the verb's own stdout and stderr; an exit status of the program's
// is the verb's, so a step that branches on 1 against 2 sees what the program said.
func command(ctx context.Context, c *cobra.Command, dir, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = c.InOrStdin(), c.OutOrStdout(), c.ErrOrStderr()
	return status(cmd.Run(), name)
}

// status is a program's failure as the verb's, its name said: a program's 1 is something to act
// on, as a verb's is - versions check's pin behind, a refused verification - and anything else is
// a program that could not do it.
func status(err error, name string) error {
	if err == nil {
		return nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return exitError{code: 1, err: fmt.Errorf("%s: %w", name, err)}
	}
	return couldNot(fmt.Errorf("%s: %w", name, err))
}

// versionsTool runs go-tools' versions command, at the version go.mod pins (its tool directive).
func versionsTool(ctx context.Context, c *cobra.Command, root string, args ...string) error {
	return command(ctx, c, root, "go", append([]string{"tool", "versions"}, args...)...)
}
