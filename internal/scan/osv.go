// SPDX-License-Identifier: Apache-2.0

package scan

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
)

// SyncIssues keeps one issue per package of the base image for what OSV knows against release,
// the newest one (spin-machine-dev osv --issues): it opens one for a package that has findings,
// rewrites its list as the findings change, and closes it once the release has none in that
// package. findings is spin-machine-dev osv's file: id, package@version and summary, a tab between
// them. gh runs the GitHub CLI against the repository, and each action taken is said on stdout.
//
// One issue a package and not a finding, because a package is what gets fixed: Ubuntu ships one
// version of curl that answers a notice and the nineteen CVEs it names, and a new release closes
// all of them at once. An issue per finding made nineteen issues of one fix, and a notice and the
// CVEs it covers two issues of one finding.
//
// A closed issue is an answer, fixed or judged not to matter, and stays closed while its package
// has only the findings it listed. A finding it did not list reopens it.
func SyncIssues(release, findings string, gh func(args ...string) ([]byte, error), stdout io.Writer) error {
	f, err := os.Open(findings)
	if err != nil {
		return fmt.Errorf("reading the findings: %w", err)
	}
	defer f.Close()
	found, err := readFindings(f)
	if err != nil {
		return err
	}
	// Every issue and not a search: the search index lags the issues by minutes, and an issue
	// it has not seen yet is one opened again.
	out, err := gh("issue", "list", "--state", "all", "--limit", "1000", "--json", "number,title,state,body")
	if err != nil {
		return fmt.Errorf("listing issues: %w", err)
	}
	var issues []issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return fmt.Errorf("listing issues: %w", err)
	}
	for _, a := range plan(release, found, issues) {
		fmt.Fprintln(stdout, a.describe())
		if _, err := gh(a.args()...); err != nil {
			return fmt.Errorf("%s: %w", a.describe(), err)
		}
	}
	return nil
}

type finding struct {
	id, pkg, summary string
}

// name is the package without its version: what an issue is about across releases.
func (f finding) name() string {
	name, _, _ := strings.Cut(f.pkg, "@")
	return name
}

// readFindings reads spin-machine-dev osv's lines: id, package@version and summary.
func readFindings(r io.Reader) ([]finding, error) {
	var out []finding
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		if sc.Text() == "" {
			continue
		}
		cols := strings.SplitN(sc.Text(), "\t", 3)
		if len(cols) < 2 || cols[0] == "" || !strings.Contains(cols[1], "@") {
			return nil, fmt.Errorf("findings line %d: want id, package@version and summary: %q", n, sc.Text())
		}
		f := finding{id: cols[0], pkg: cols[1]}
		if len(cols) == 3 {
			f.summary = cols[2]
		}
		out = append(out, f)
	}
	return out, sc.Err()
}

type issue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	Body   string `json:"body"`
}

// listed is the findings an issue's body names, by the links body writes.
var listed = regexp.MustCompile(`osv\.dev/vulnerability/([^)\s]+)`)

func (i issue) ids() []string {
	var ids []string
	for _, m := range listed.FindAllStringSubmatch(i.Body, -1) { // mutate-exempt: any negative count is every match, so -1 and -2 find the same
		ids = append(ids, m[1])
	}
	return ids
}

func title(pkg string) string { return "osv: " + pkg }

// body is a package's issue: the release, the package as it is in it, and one line a finding.
func body(release string, fs []finding) string {
	var b strings.Builder
	var pkgs []string
	for _, f := range fs {
		if !slices.Contains(pkgs, f.pkg) {
			pkgs = append(pkgs, f.pkg)
		}
	}
	fmt.Fprintf(&b, "%s, the newest release, has %s with what OSV lists below (`spin-machine-dev osv`, run daily by osv.yml). "+
		"A new release built after the distribution ships a fix closes this issue; closing it by hand says the rest does not matter here, "+
		"and only a finding not listed reopens it.\n\n", release, "`"+strings.Join(pkgs, "`, `")+"`")
	for _, f := range fs {
		fmt.Fprintf(&b, "- [%s](https://osv.dev/vulnerability/%s)", f.id, f.id)
		if f.summary != "" {
			fmt.Fprintf(&b, ": %s", f.summary)
		}
		b.WriteString("\n")
	}
	return b.String()
}

type action struct {
	verb   string // create, edit, reopen, close
	number int
	title  string
	body   string
}

func (a action) describe() string {
	if a.number == 0 {
		return fmt.Sprintf("%s %q", a.verb, a.title)
	}
	return fmt.Sprintf("%s #%d %q", a.verb, a.number, a.title)
}

func (a action) args() []string {
	n := fmt.Sprint(a.number)
	switch a.verb {
	case "create":
		return []string{"issue", "create", "--title", a.title, "--body", a.body}
	case "edit":
		return []string{"issue", "edit", n, "--body", a.body}
	case "reopen":
		return []string{"issue", "reopen", n, "--comment", a.body}
	default:
		return []string{"issue", "close", n, "--reason", "completed", "--comment", a.body}
	}
}

// plan is what brings the issues to the findings: one issue a package, its body the package's
// findings in release.
func plan(release string, findings []finding, issues []issue) []action {
	byPkg := map[string][]finding{}
	var names []string
	for _, f := range findings {
		if _, ok := byPkg[f.name()]; !ok {
			names = append(names, f.name())
		}
		byPkg[f.name()] = append(byPkg[f.name()], f)
	}
	slices.Sort(names)

	// The newest issue of a title is the package's: an older one is history.
	byTitle := map[string]issue{}
	for _, i := range issues {
		if have, ok := byTitle[i.Title]; !ok || i.Number > have.Number {
			byTitle[i.Title] = i
		}
	}

	var out []action
	for _, name := range names {
		fs := byPkg[name]
		want := body(release, fs)
		i, ok := byTitle[title(name)]
		switch {
		case !ok:
			out = append(out, action{verb: "create", title: title(name), body: want})
		case i.State == "OPEN":
			if i.Body != want {
				out = append(out, action{verb: "edit", number: i.Number, title: i.Title, body: want})
			}
		default:
			var unlisted []string
			for _, f := range fs {
				if !slices.Contains(i.ids(), f.id) {
					unlisted = append(unlisted, f.id)
				}
			}
			if len(unlisted) > 0 {
				out = append(out,
					action{verb: "reopen", number: i.Number, title: i.Title, body: fmt.Sprintf("%s has findings this issue did not list: %s.", release, strings.Join(unlisted, ", "))},
					action{verb: "edit", number: i.Number, title: i.Title, body: want})
			}
		}
	}
	var gone []string
	for t, i := range byTitle {
		name, ok := strings.CutPrefix(t, "osv: ")
		if !ok || i.State != "OPEN" || strings.Contains(name, " ") {
			continue
		}
		if _, has := byPkg[name]; !has {
			gone = append(gone, t)
		}
	}
	slices.Sort(gone)
	for _, t := range gone {
		i := byTitle[t]
		out = append(out, action{verb: "close", number: i.Number, title: t, body: fmt.Sprintf("%s has no finding in this package.", release)})
	}
	return out
}
