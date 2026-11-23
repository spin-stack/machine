// SPDX-License-Identifier: Apache-2.0

package patched

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// A Finding is a result of the analysis on a line the patches added.
type Finding struct {
	Rule, File, Message string
	Line                int
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d: %s: %s", f.File, f.Line, f.Rule, f.Message)
}

// Scope reads the SARIF the analysis wrote over dir's tree (dir/sarif/*.sarif) and answers the
// results on a line the patches added or changed, which it also writes to dir/scoped.sarif, what
// code scanning is given. A result anywhere else in a touched file is upstream's: it goes to
// dir/upstream.txt, one a line, and decides nothing. An alert for upstream's code is one nobody
// here fixes, and a ruleset that requires CodeQL would block a merge on it (alert #1, system/vl.c's
// -dump-vmstate fopen, which qemu 0001 does not change, dismissed 2026-11-23).
//
// No result says nothing about a file the extractor did not compile, and CodeQL's analysis exits
// 0 without it. So Scope also fails, naming each, on a touched C file the SARIF does not report
// extracted, or reports a failed compilation of: the analysis read none of it, or only part.
func Scope(ctx context.Context, dir string) ([]Finding, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "touched"))
	if err != nil {
		return nil, fmt.Errorf("reading the touched files (make the tree first): %w", err)
	}
	added := map[string]map[int]bool{}
	for _, f := range strings.Fields(string(raw)) {
		if added[f], err = addedLines(ctx, filepath.Join(dir, "upstream", f), filepath.Join(dir, "src", f)); err != nil {
			return nil, err
		}
	}
	sarifs, _ := filepath.Glob(filepath.Join(dir, "sarif", "*.sarif"))
	if len(sarifs) != 1 {
		return nil, fmt.Errorf("want one SARIF file in %s, found %d", filepath.Join(dir, "sarif"), len(sarifs))
	}
	if raw, err = os.ReadFile(sarifs[0]); err != nil {
		return nil, err
	}
	var log struct {
		Runs []map[string]json.RawMessage `json:"runs"`
	}
	var doc map[string]json.RawMessage
	if err := errors.Join(json.Unmarshal(raw, &doc), json.Unmarshal(raw, &log)); err != nil {
		return nil, fmt.Errorf("reading %s: %w", sarifs[0], err)
	}
	if len(log.Runs) == 0 {
		return nil, fmt.Errorf("%s has no run: the analysis did not finish", sarifs[0])
	}
	src, _ := filepath.Abs(filepath.Join(dir, "src"))
	var findings, upstream []Finding
	for _, run := range log.Runs {
		var results []result
		if err := json.Unmarshal(run["results"], &results); err != nil {
			return nil, fmt.Errorf("reading %s's results: %w", sarifs[0], err)
		}
		kept := []json.RawMessage{}
		for _, r := range results {
			if len(r.Locations) == 0 {
				continue
			}
			file, first, last := r.where(src)
			lines, ok := added[file]
			if !ok {
				continue
			}
			f := Finding{Rule: r.RuleID, File: file, Line: first, Message: r.Message.Text}
			ours := false
			for l := first; l <= last && !ours; l++ {
				if ours = lines[l]; ours {
					f.Line = l
				}
			}
			if ours {
				findings = append(findings, f)
				kept = append(kept, r.raw)
			} else {
				upstream = append(upstream, f)
			}
		}
		if run["results"], err = json.Marshal(kept); err != nil {
			return nil, err
		}
	}
	if doc["runs"], err = json.Marshal(log.Runs); err != nil {
		return nil, err
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "scoped.sarif"), out, 0o644); err != nil {
		return nil, err
	}
	var text strings.Builder
	for _, f := range upstream {
		text.WriteString(f.String() + "\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "upstream.txt"), []byte(text.String()), 0o644); err != nil {
		return nil, err
	}
	unread, err := notExtracted(log.Runs, src, slices.Collect(maps.Keys(added)))
	if err != nil {
		return findings, fmt.Errorf("reading %s's extraction diagnostics: %w", sarifs[0], err)
	}
	if len(unread) > 0 {
		return findings, fmt.Errorf("the analysis did not read every C file the patches touch: %s", strings.Join(unread, "; "))
	}
	return findings, nil
}

// The diagnostics CodeQL's C/C++ extractor writes into the SARIF: a file each compilation read
// without an error, and, without a build (build-mode none), each compilation that failed.
const (
	extractedDiagnostic = "cpp/diagnostics/successfully-extracted-files"
	buildlessDiagnostic = "cpp/bmn/extraction-information"
)

// notExtracted is each touched C file the runs do not report extracted, or report a failed
// compilation of, with why, sorted.
func notExtracted(runs []map[string]json.RawMessage, src string, touched []string) ([]string, error) {
	extracted, failed := map[string]bool{}, map[string]bool{}
	for _, run := range runs {
		var invocations []struct {
			Notifications []struct {
				Descriptor struct {
					ID string `json:"id"`
				} `json:"descriptor"`
				Locations  []location `json:"locations"`
				Properties struct {
					Attributes struct {
						Errors []struct {
							Args []string `json:"error_args"`
						} `json:"extraction_errors"`
					} `json:"attributes"`
				} `json:"properties"`
			} `json:"toolExecutionNotifications"`
		}
		if raw := run["invocations"]; raw != nil {
			if err := json.Unmarshal(raw, &invocations); err != nil {
				return nil, err
			}
		}
		for _, inv := range invocations {
			for _, n := range inv.Notifications {
				switch n.Descriptor.ID {
				case extractedDiagnostic:
					for _, l := range n.Locations {
						extracted[l.file(src)] = true
					}
				case buildlessDiagnostic:
					for _, e := range n.Properties.Attributes.Errors {
						if len(e.Args) > 0 {
							failed[inTree(e.Args[len(e.Args)-1], src)] = true
						}
					}
				}
			}
		}
	}
	var unread []string
	for _, f := range touched {
		switch {
		case !slices.Contains(cSources, filepath.Ext(f)):
		case failed[f]:
			unread = append(unread, f+" (its compilation failed)")
		case !extracted[f]:
			unread = append(unread, f+" (not extracted)")
		}
	}
	slices.Sort(unread)
	return unread, nil
}

// result is a SARIF result: what this reads of it, and all of it to write back.
type result struct {
	raw     json.RawMessage
	RuleID  string `json:"ruleId"`
	Message struct {
		Text string `json:"text"`
	} `json:"message"`
	Locations []location `json:"locations"`
}

// location is a SARIF location: what this reads of it.
type location struct {
	Physical struct {
		Artifact struct {
			URI string `json:"uri"`
		} `json:"artifactLocation"`
		Region struct {
			Start int `json:"startLine"`
			End   int `json:"endLine"`
		} `json:"region"`
	} `json:"physicalLocation"`
}

// file is the file a location is in, as a path in the tree whose root is src.
func (l location) file(src string) string {
	return inTree(l.Physical.Artifact.URI, src)
}

// inTree is a file the SARIF names, by URI or absolute path, as a path in the tree at src.
func inTree(uri, src string) string {
	return strings.TrimPrefix(strings.TrimPrefix(uri, "file://"), src+"/")
}

func (r *result) UnmarshalJSON(b []byte) error {
	type plain result
	r.raw = append(json.RawMessage(nil), b...)
	return json.Unmarshal(b, (*plain)(r))
}

// where is the file a result is in, as a path in the tree, and its first and last lines.
func (r result) where(src string) (string, int, int) {
	l := r.Locations[0]
	return l.file(src), l.Physical.Region.Start, max(l.Physical.Region.Start, l.Physical.Region.End)
}

// addedLines is every line of now that the patches added or changed against was: the new side
// of each hunk of diff -U0. A file with no upstream copy is all the patches'.
func addedLines(ctx context.Context, was, now string) (map[int]bool, error) {
	if _, err := os.Stat(was); err != nil {
		was = os.DevNull
	}
	out, err := exec.CommandContext(ctx, "diff", "-U0", was, now).Output()
	var exit *exec.ExitError
	if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) {
		return nil, fmt.Errorf("diff %s %s: %w", was, now, err)
	}
	lines := map[int]bool{}
	for line := range strings.Lines(string(out)) {
		hunk, ok := strings.CutPrefix(line, "@@ -")
		if !ok {
			continue
		}
		_, plus, _ := strings.Cut(hunk, " +")
		span, _, _ := strings.Cut(plus, " ")
		start, count, found := strings.Cut(span, ",")
		first, _ := strconv.Atoi(start)
		n := 1
		if found {
			n, _ = strconv.Atoi(count)
		}
		for l := first; l < first+n; l++ {
			lines[l] = true
		}
	}
	return lines, nil
}

// Medium is the security severity, CVSS-like as CodeQL rates its rules, from which a result in
// the repository's own code fails codeql.yml: GitHub's "medium" begins at 4.0. The repository's
// first analysis gave one result at that or above (go/insecure-hostkeycallback, high, 2026-11-23),
// so the threshold costs nothing today, and a new one is fixed or suppressed in the source with
// its reason. Results below it, and those of rules with no security severity, go to code scanning.
const Medium = 4.0

// Severe is each result of the SARIF files in dir whose rule's security severity is at least at,
// and which the analysis does not report suppressed: the alert-suppression query marks a result
// under a `codeql[<rule>]` comment, which says in the source why it stays.
func Severe(dir string, at float64) ([]Finding, error) {
	sarifs, _ := filepath.Glob(filepath.Join(dir, "*.sarif"))
	if len(sarifs) == 0 {
		return nil, fmt.Errorf("no SARIF file in %s", dir)
	}
	var severe []Finding
	for _, path := range sarifs {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		type rules []struct {
			ID         string `json:"id"`
			Properties struct {
				Severity string `json:"security-severity"`
			} `json:"properties"`
		}
		var log struct {
			Runs []struct {
				Tool struct {
					Driver     struct{ Rules rules }   `json:"driver"`
					Extensions []struct{ Rules rules } `json:"extensions"`
				} `json:"tool"`
				Results []json.RawMessage `json:"results"`
			} `json:"runs"`
		}
		if err := json.Unmarshal(raw, &log); err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		if len(log.Runs) == 0 {
			return nil, fmt.Errorf("%s has no run: the analysis did not finish", path)
		}
		for _, run := range log.Runs {
			severity := map[string]float64{}
			all := run.Tool.Driver.Rules
			for _, e := range run.Tool.Extensions {
				all = append(all, e.Rules...)
			}
			for _, r := range all {
				if s, err := strconv.ParseFloat(r.Properties.Severity, 64); err == nil { // mutate-exempt: any bitSize but 32 parses a float64
					severity[r.ID] = s
				}
			}
			for _, b := range run.Results {
				var r result
				var suppressed struct {
					Suppressions []json.RawMessage `json:"suppressions"`
				}
				if err := errors.Join(json.Unmarshal(b, &r), json.Unmarshal(b, &suppressed)); err != nil {
					return nil, fmt.Errorf("reading %s's results: %w", path, err)
				}
				if len(suppressed.Suppressions) > 0 || severity[r.RuleID] < at {
					continue
				}
				f := Finding{Rule: r.RuleID, Message: r.Message.Text}
				if len(r.Locations) > 0 {
					f.File, f.Line, _ = r.where("")
				}
				severe = append(severe, f)
			}
		}
	}
	return severe, nil
}
