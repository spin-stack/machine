// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// spinMachine is cmd/spin-machine built as a release builds it, once for the package's tests,
// realGo the go that built it, which a fake go hands what a release asks of go besides a build,
// and repoRoot this repository, read before a test moves to a checkout of its own.
var spinMachine, realGo, repoRoot string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "spin-machine-dev-test")
	if err == nil {
		realGo, err = exec.LookPath("go")
	}
	if err == nil {
		repoRoot, err = filepath.Abs(filepath.Join("..", ".."))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	spinMachine = filepath.Join(dir, "spin-machine")
	cmd := exec.Command(realGo, "build", "-trimpath", "-buildvcs=false", "-o", spinMachine, "./cmd/spin-machine")
	cmd.Dir, cmd.Env = repoRoot, append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building spin-machine: %v: %s\n", err, out)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// goModule gives dir this repository's go.mod and go.sum: the toolchain and modules a release
// reads spin-machine's licences from.
func goModule(t *testing.T, dir string) {
	t.Helper()
	for _, f := range []string{"go.mod", "go.sum"} {
		b, err := os.ReadFile(filepath.Join(repoRoot, f))
		if err != nil {
			t.Fatal(err)
		}
		put(t, filepath.Join(dir, f), string(b), 0o644)
	}
}

// goBuildsSpinMachine is a fake go's body: a build of cmd/spin-machine is spinMachine, go env and
// go mod download are the real go's, and anything else does nothing.
func goBuildsSpinMachine() string {
	return `case "$1" in env|mod) exec ` + realGo + ` "$@";; esac
prev=""; for a; do [ "$prev" = -o ] && out=$a; prev=$a; done; [ "$prev" = ./cmd/spin-machine ] && cp ` + spinMachine + ` "$out"; exit 0`
}
