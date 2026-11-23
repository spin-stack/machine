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

// Scope reads the SARIF the analysis wrote over dir's tree (dir/sarif/*.sarif), writes the
// results in the files the patches touch to dir/scoped.sarif, which is what code scanning is
// given, and answers those on a line the patches added. A result anywhere else in a touched file
// is upstream's: it goes to code scanning and decides nothing here.
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
	var findings []Finding
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
			kept = append(kept, r.raw)
			for l := first; l <= last; l++ {
				if lines[l] {
					findings = append(findings, Finding{Rule: r.RuleID, File: file, Line: l, Message: r.Message.Text})
					break
				}
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
