// SPDX-License-Identifier: Apache-2.0

// Package scan says what is known against a release: the Linux kernel CNA's published CVEs against
// its kernel (KernelCVEs), and OSV's findings against its SBOM, kept as one issue a package
// (SyncIssues).
package scan

import (
	"bufio"
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// KernelCVEs is what the kernel CNA's records under published mean for the kernel at v, whose
// build compiled the files the list at sources names: a Markdown summary saying, for each CVE,
// whether v is past its fix, whether the kernel compiled none of the files the fix touched, or
// whether it applies, and for those that apply, where the fix is.
//
// "Not built" is read off the build and not the configuration: the kernel's build records, for
// every object it compiled, the sources and headers that went into it (kernel/Dockerfile writes
// their union as kernel/sources), so a CVE whose files are none of them is in code this kernel
// does not contain. Reasoning from CONFIG_ symbols to files through Kbuild's makefiles would be a
// second, worse copy of what the build already decided.
//
// The record's semver ranges are read and its git ranges are not: the kernel is built from a
// release tarball, so its version is all there is to compare.
func KernelCVEs(published string, v KernelVersion, sources string) (string, error) {
	built, err := readSources(sources)
	if err != nil {
		return "", err
	}
	records, err := readRecords(published)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	writeSummary(&b, v, classify(records, v, built))
	return b.String(), nil
}

// readSources reads kernel/sources, each path cleaned: the build records a header included by a
// relative path as it was written (net/ipv4/../core/devmem.h), and a CVE names it as
// net/core/devmem.h - 125 of 7.3.1's lines. An empty list is refused: it is a build that recorded
// nothing, and against it every CVE would read as not built.
func readSources(file string) (map[string]bool, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, fmt.Errorf("reading the kernel's sources: %w", err)
	}
	defer f.Close()
	sources := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			sources[path.Clean(line)] = true
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading the kernel's sources %s: %w", file, err)
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("the kernel's sources %s name no file", file)
	}
	return sources, nil
}

// record is the part of a CVE JSON 5 record this reads.
type record struct {
	Meta struct {
		ID string `json:"cveId"`
	} `json:"cveMetadata"`
	Containers struct {
		CNA struct {
			Title    string     `json:"title"`
			Affected []affected `json:"affected"`
		} `json:"cna"`
	} `json:"containers"`
}

type affected struct {
	DefaultStatus string   `json:"defaultStatus"`
	ProgramFiles  []string `json:"programFiles"`
	Versions      []rng    `json:"versions"`
}

type rng struct {
	Version         string `json:"version"`
	LessThan        string `json:"lessThan"`
	LessThanOrEqual string `json:"lessThanOrEqual"`
	Status          string `json:"status"`
	VersionType     string `json:"versionType"`
}

// readRecords reads every record under dir. A file that does not parse is an error rather than
// a CVE left out: a count with holes in it reads as a better kernel than the one there is.
func readRecords(dir string) ([]record, error) {
	var out []record
	err := filepath.WalkDir(dir, func(path string, _ fs.DirEntry, err error) error {
		// A directory is a year, which has no extension; the rest beside the records are .dyad and .sha1.
		if err != nil || filepath.Ext(path) != ".json" {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var r record
		if err := json.Unmarshal(b, &r); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, r)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading the CVE records in %s: %w", dir, err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no CVE record in %s", dir)
	}
	return out, nil
}

// KernelVersion is a kernel version's numbers: 7.3 is 7.3.0, and a release candidate of 7.4 is
// 7.4.-1, after every 7.3 and before 7.4.0, so a fix released in 7.4 is not taken for one it has.
type KernelVersion []int

// rcVersion is a release candidate's: 7.4-rc4.
var rcVersion = regexp.MustCompile(`^(\d+\.\d+)-rc[1-9]\d*$`)

// ParseKernelVersion is s, such as 7.3.3 or 7.4-rc4, as a KernelVersion.
func ParseKernelVersion(s string) (KernelVersion, error) {
	series := s
	if m := rcVersion.FindStringSubmatch(s); m != nil {
		series = m[1]
	}
	v, ok := parseVersion(series)
	if !ok {
		return nil, fmt.Errorf("%q is not a kernel version", s)
	}
	if series != s {
		v = append(v, -1) // mutate-exempt: any negative third number orders and prints a candidate the same
	}
	return v, nil
}

func parseVersion(s string) (KernelVersion, bool) {
	var v KernelVersion
	for p := range strings.SplitSeq(s, ".") {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false
		}
		v = append(v, n)
	}
	return v, len(v) > 0
}

func (v KernelVersion) compare(w KernelVersion) int {
	for i := range max(len(v), len(w)) {
		if c := cmp.Compare(v.at(i), w.at(i)); c != 0 {
			return c
		}
	}
	return 0
}

func (v KernelVersion) at(i int) int {
	if i < len(v) {
		return v[i]
	}
	return 0
}

func (v KernelVersion) series() string {
	return fmt.Sprintf("%d.%d", v.at(0), v.at(1))
}

// atMost is v <= bound, where bound is a version, "*" (no bound), or a series and "*": 7.2.*
// is every release of 7.2.
func (v KernelVersion) atMost(bound string) bool {
	if bound == "*" {
		return true
	}
	if prefix, ok := strings.CutSuffix(bound, ".*"); ok {
		b, ok := parseVersion(prefix)
		return ok && KernelVersion(v[:min(len(v), len(b))]).compare(b) <= 0
	}
	b, ok := parseVersion(bound)
	return ok && v.compare(b) <= 0
}

// contains is whether a range names v. A git range never does: there is no commit to compare.
func (r rng) contains(v KernelVersion) bool {
	if r.VersionType == "git" {
		return false
	}
	lo, ok := parseVersion(r.Version)
	if !ok {
		return false
	}
	switch {
	case r.LessThan != "":
		hi, ok := parseVersion(r.LessThan)
		return ok && lo.compare(v) <= 0 && v.compare(hi) < 0
	case r.LessThanOrEqual != "":
		return lo.compare(v) <= 0 && v.atMost(r.LessThanOrEqual)
	default:
		return lo.compare(v) == 0
	}
}

// status is what an affected entry says of v: its default, unless one of its ranges names v.
func (a affected) status(v KernelVersion) string {
	status := a.DefaultStatus
	for _, r := range a.Versions {
		if r.contains(v) {
			status = r.Status
		}
	}
	return status
}

// fixes are the versions an entry says a fix landed in: the start of each range that is
// unaffected from there on (a stable release up to its series' end, or mainline up to "*"). A git
// range's version is a commit, which parseVersion refuses.
func (a affected) fixes() []KernelVersion {
	var out []KernelVersion
	for _, r := range a.Versions {
		if r.Status != "unaffected" || r.LessThanOrEqual == "" {
			continue
		}
		if f, ok := parseVersion(r.Version); ok {
			out = append(out, f)
		}
	}
	return out
}

type verdict int

const (
	// fixed: the version is past the fix, or before the bug.
	fixed verdict = iota
	// notBuilt: the version has the bug, in files this kernel did not compile.
	notBuilt
	// applies: the version has the bug, in files it compiled, or the record names no file.
	applies
)

type cve struct {
	id, title string
	verdict   verdict
	// fix is the first version past v the record says is fixed, nil when there is none yet.
	fix KernelVersion
}

func classify(records []record, v KernelVersion, sources map[string]bool) []cve {
	var out []cve
	for _, r := range records {
		f := cve{id: r.Meta.ID, title: r.Containers.CNA.Title, verdict: fixed}
		var files []string
		var fixes []KernelVersion
		for _, a := range r.Containers.CNA.Affected {
			files = append(files, a.ProgramFiles...)
			fixes = append(fixes, a.fixes()...)
			if a.status(v) == "affected" {
				f.verdict = applies
			}
		}
		if f.verdict == applies && len(files) > 0 && !slices.ContainsFunc(files, func(p string) bool { return sources[p] }) {
			f.verdict = notBuilt
		}
		if f.verdict == applies {
			f.fix = firstFixAfter(v, fixes)
		}
		out = append(out, f)
	}
	slices.SortFunc(out, func(a, b cve) int { return cmp.Compare(a.id, b.id) })
	return out
}

// firstFixAfter prefers a release of v's own series, which refresh.yml takes on its own, to a
// later series, which is a person's decision.
func firstFixAfter(v KernelVersion, fixes []KernelVersion) KernelVersion {
	var same, later KernelVersion
	for _, f := range fixes {
		if f.compare(v) <= 0 {
			continue
		}
		if f.series() == v.series() {
			if same == nil || f.compare(same) < 0 { // mutate-exempt: <= between equal versions keeps the same version
				same = f
			}
		} else if later == nil || f.compare(later) < 0 { // mutate-exempt: <= between equal versions keeps the same version
			later = f
		}
	}
	if same != nil {
		return same
	}
	return later
}

func (v KernelVersion) String() string {
	if len(v) == 3 && v[2] < 0 {
		return fmt.Sprintf("%d.%d-rc", v[0], v[1])
	}
	parts := make([]string, len(v))
	for i, n := range v {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ".")
}

func writeSummary(w io.Writer, v KernelVersion, findings []cve) {
	ver := v.String()
	var nFixed, nNotBuilt int
	var inSeries, laterSeries, unfixed []cve
	for _, f := range findings {
		switch {
		case f.verdict == fixed:
			nFixed++
		case f.verdict == notBuilt:
			nNotBuilt++
		case f.fix == nil:
			unfixed = append(unfixed, f)
		case f.fix.series() == v.series():
			inSeries = append(inSeries, f)
		default:
			laterSeries = append(laterSeries, f)
		}
	}
	n := len(findings)
	pct := func(k int) string { return fmt.Sprintf("%.1f%%", 100*float64(k)/float64(n)) }
	fmt.Fprintf(w, "## Linux %s against the kernel CNA's %d published CVEs\n\n", ver, n)
	fmt.Fprintln(w, "| | CVEs | |")
	fmt.Fprintln(w, "|---|---:|---:|")
	fmt.Fprintf(w, "| Fixed in %s, or older than the bug | %d | %s |\n", ver, nFixed, pct(nFixed))
	fmt.Fprintf(w, "| In files this kernel does not compile | %d | %s |\n", nNotBuilt, pct(nNotBuilt))
	fmt.Fprintf(w, "| Apply: fixed in a later %s release, which refresh.yml proposes | %d | %s |\n", v.series(), len(inSeries), pct(len(inSeries)))
	fmt.Fprintf(w, "| Apply: fixed only in a later series, a person's decision | %d | %s |\n", len(laterSeries), pct(len(laterSeries)))
	fmt.Fprintf(w, "| Apply: no fix released | %d | %s |\n", len(unfixed), pct(len(unfixed)))
	list(w, "Fixed only in a later series", laterSeries)
	list(w, fmt.Sprintf("Fixed in a later %s release", v.series()), inSeries)
	list(w, "No fix released", unfixed)
}

func list(w io.Writer, heading string, findings []cve) {
	if len(findings) == 0 {
		return
	}
	fmt.Fprintf(w, "\n<details><summary>%s (%d)</summary>\n\n", heading, len(findings))
	for _, f := range findings {
		fix := ""
		if f.fix != nil {
			fix = " - fixed in " + f.fix.String()
		}
		fmt.Fprintf(w, "- [%s](https://www.cve.org/CVERecord?id=%s) %s%s\n", f.id, f.id, f.title, fix)
	}
	fmt.Fprintln(w, "\n</details>")
}
