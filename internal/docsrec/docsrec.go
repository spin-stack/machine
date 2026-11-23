// SPDX-License-Identifier: Apache-2.0

// Package docsrec reads the log's entries, one file an entry under docs/log, and the experiments'
// records, one file a row under docs/experiments, and renders what they add up to: the log, and
// the tables of docs/boot-time.md. The pages are rendered, never committed (spin-stack/machine#709):
// every pull request that decided something appended to one docs/log.md and edited one table, so
// any two in flight conflicted once one merged, and each conflict became a replacement PR. A pull
// request now adds files of its own, which no other edits.
package docsrec

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"time"
)

// The directories, relative to the repository's root.
const (
	LogDir         = "docs/log"
	ExperimentsDir = "docs/experiments"
)

// dateLayout is how a date is written in an entry's front matter, a record and a file name.
const dateLayout = "2006-01-02"

// runID is a GitHub Actions run id, as a lab, report, compat or parts run is cited: every one is
// eleven digits, and no commit, count or measurement in the prose is.
var runID = regexp.MustCompile(`\b\d{11}\b`)

// slug is the part of a file name after its date or issue.
var slug = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// parseDate reads a YYYY-MM-DD date, the only form a date is kept in.
func parseDate(s string) (time.Time, error) {
	d, err := time.Parse(dateLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("date %q is not YYYY-MM-DD", s)
	}
	return d, nil
}

// checkRefs holds the issues, pull requests and runs a file lists to its text: each issue and pull
// request is cited there as #N, and every run id the text cites is listed. A list is what a program
// reads instead of the prose, so it must say what the prose says.
func checkRefs(text string, issues, prs, runs []int) error {
	for _, n := range slices.Concat(issues, prs) {
		if !regexp.MustCompile(`#` + strconv.Itoa(n) + `\b`).MatchString(text) {
			return fmt.Errorf("issues and prs: #%d is listed but the text does not cite it", n)
		}
	}
	for _, id := range runID.FindAllString(text, -1) {
		n, _ := strconv.Atoi(id)
		if !slices.Contains(runs, n) {
			return fmt.Errorf("runs: the text cites run %s, which runs does not list", id)
		}
	}
	for _, n := range runs {
		if !regexp.MustCompile(`\b` + strconv.Itoa(n) + `\b`).MatchString(text) {
			return fmt.Errorf("runs: %d is listed but the text does not cite it", n)
		}
	}
	return nil
}
