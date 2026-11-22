// SPDX-License-Identifier: Apache-2.0

// coverfloor holds `task test`'s coverage profile to a floor, counting only what CI can run.
//
// A function whose doc comment says `mutate-lane: TestName` is held by a lane's test, one that
// needs root and KVM, which CI's Gate has neither of. go-tools' mutate already leaves such a
// function out (its laneMarker, read the same way here: the marker anywhere in the doc comment,
// and mutate refuses one that names no test). Counted, every probe the lab
// gained pulled the total down however well its testable half was tested, and #406 passed at
// 79.96% against a floor of 80 (#410).
//
// The comparison is on the exact percentage. `go tool cover -func` rounds to one decimal, and
// read #406's 79.96 as 80.0, which passed.
//
//	go run ./internal/coverfloor 80 coverage.out
package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
)

func main() {
	os.Exit(run(os.Args[1:], ".", os.Stdout, os.Stderr)) // mutate-exempt: task test runs this on every push, and any other slice of the arguments exits 2 there
}

// run is the command on args, a floor and a profile, in the module at dir: 0 at or above the
// floor, 1 below it, 2 when it could not tell.
func run(args []string, dir string, stdout, stderr io.Writer) int {
	if len(args) != 2 {
		_, _ = fmt.Fprintln(stderr, "usage: coverfloor FLOOR coverage.out")
		return 2
	}
	floor, err := strconv.ParseFloat(args[0], 64) // mutate-exempt: ParseFloat reads every bitSize but 32 as 64
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "coverfloor: the floor: %v\n", err)
		return 2
	}
	t, err := measure(args[1], dir)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "coverfloor: %v\n", err)
		return 2
	}
	got := t.percent(false)
	if got < floor {
		_, _ = fmt.Fprintf(stdout, "coverage is %.2f%% of what CI runs, below the floor of %g%%\n", got, floor)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "coverage: %.2f%% of what CI runs (floor %g%%); %.2f%% with the %d statements only a lane runs\n",
		got, floor, t.percent(true), t.laneStatements)
	return 0
}
