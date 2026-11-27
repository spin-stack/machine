// SPDX-License-Identifier: Apache-2.0

package release

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// apkLinked are the packages each static binary links, by the -static or -dev package that
// carries the .a, with musl's libc and gcc's libgcc, in the apk database its build wrote: what
// the SBOM names as shipped and what alpine-licenses.txt carries the texts of. The rest of each
// database built them and is formulation.
var apkLinked = []struct{ name, db, pin, only string }{
	{"qemu-libraries", "qemu.apk", "qemu", "glib-static,pcre2-static,libffi-dev,pixman-static,zlib-static,libseccomp-static,libcap-ng-static,liburing-dev,libaio-dev,gettext-static,openssl-libs-static,jemalloc-static,musl-dev,gcc"},
	{"e2fsprogs-libraries", "e2fsprogs.apk", "e2fsprogs", "libarchive-static,acl-static,zlib-static,xz-static,zstd-static,bzip2-static,lz4-static,expat-static,openssl-libs-static,musl-dev,gcc"},
}

// noNotice are the aports whose licence asks nothing of a binary that links them, each with why:
// the only ones alpine-licenses.txt names without a text. Kept to what a licence says outright,
// never to a source too large to fetch.
var noNotice = map[string]string{
	// What a static binary takes from gcc is libgcc.a and libgcc_eh.a.
	"gcc": "what the binaries link of it is libgcc, under GPL-3.0-or-later WITH GCC-exception-3.1, " +
		"which permits propagating target code compiled by GCC with it under terms of one's choice " +
		"and asks for no notice to go with it",
}

// AlpineSources is where the Alpine package sources whose licence texts a release ships are
// fetched from, and kept. Zero URLs are Alpine's own.
type AlpineSources struct {
	// Aports serves an APKBUILD at <Aports>/<repository>/<origin>/APKBUILD?id=<commit>.
	Aports string
	// Distfiles serves each source Alpine built at <Distfiles>/v<series>/<file>: the fallback for
	// an upstream that moved or a source URL this file cannot expand.
	Distfiles string
	// Cache keeps the tarballs between releases, each checked against its sha512 on every read.
	Cache string
}

// apkPackage is one package of an apk database, as a record of /lib/apk/db/installed has it.
type apkPackage struct {
	name, version, origin, commit, license string
}

// apkPackages are the packages of an apk database named in only, each with the aport it was built
// from (o:) and the aports commit that built it (c:). One the database lacks, or that names no
// origin or commit, is an error: its source could not be found.
func apkPackages(raw []byte, only []string) ([]apkPackage, error) {
	var out []apkPackage
	for block := range strings.SplitSeq(string(raw), "\n\n") {
		var p apkPackage
		for line := range strings.SplitSeq(block, "\n") {
			k, v, _ := strings.Cut(line, ":")
			switch k {
			case "P":
				p.name = v
			case "V":
				p.version = v
			case "o":
				p.origin = v
			case "c":
				p.commit = v
			case "L":
				p.license = v
			}
		}
		if slices.Contains(only, p.name) {
			if p.origin == "" || p.commit == "" || p.version == "" {
				return nil, fmt.Errorf("%s's record in the apk database names no origin, commit or version", p.name)
			}
			out = append(out, p)
		}
	}
	for _, n := range only {
		if !slices.ContainsFunc(out, func(p apkPackage) bool { return p.name == n }) {
			return nil, fmt.Errorf("%s is not in the apk database", n)
		}
	}
	return out, nil
}

// AlpineLicenses is the licence text of each Alpine package linked into the static binaries
// (apkLinked), taken from the source Alpine built it from. Alpine's -static, -dev and -doc
// packages carry no licence file, so the text is the source's: the APKBUILD of the package's
// aport at the aports commit its apk database records, the first tarball it names, checked
// against that APKBUILD's sha512sums before a byte of it is read, and the licence files at the
// top of it. The pins are the aports commit, which names the APKBUILD's content, and the sha512
// that APKBUILD gives the tarball, so nothing of this is in versions.yaml. A package with no text fails it, unless
// noNotice says why its licence asks for none.
func AlpineLicenses(ctx context.Context, o Options) (string, error) {
	src := o.Alpine
	if src.Aports == "" {
		src.Aports = "https://git.alpinelinux.org/aports/plain"
	}
	if src.Distfiles == "" {
		src.Distfiles = "https://distfiles.alpinelinux.org/distfiles"
	}
	if src.Cache == "" {
		return "", errors.New("the Alpine licences: no directory to keep the sources in")
	}
	alpine, err := o.Pins.Get("alpine")
	if err != nil {
		return "", err
	}
	// One source per aport: zlib-static and musl-dev are in both databases, and an aport's
	// subpackages share its source.
	type aport struct {
		apkPackage
		packages []string
	}
	byOrigin := map[string]*aport{}
	var aports []*aport
	for _, a := range apkLinked {
		raw, err := os.ReadFile(filepath.Join(o.Out, "sbom", a.db))
		if err != nil {
			return "", fmt.Errorf("missing the apk database %s (spin-machine-dev build makes it): %w", a.db, err)
		}
		pkgs, err := apkPackages(raw, strings.Split(a.only, ","))
		if err != nil {
			return "", fmt.Errorf("%s: %w", a.db, err)
		}
		for _, p := range pkgs {
			s, seen := byOrigin[p.origin]
			if !seen {
				s = &aport{p, []string{p.name}}
				byOrigin[p.origin] = s
				aports = append(aports, s)
				continue
			}
			if s.commit != p.commit || s.version != p.version {
				return "", fmt.Errorf("%s is %s from aports %s in one database and %s from %s in another, and only one source can be shipped",
					p.origin, s.version, s.commit, p.version, p.commit)
			} else if !slices.Contains(s.packages, p.name) {
				s.packages = append(s.packages, p.name)
			}
		}
	}
	slices.SortFunc(aports, func(a, b *aport) int { return strings.Compare(a.origin, b.origin) })

	var b strings.Builder
	b.WriteString("The licences of the Alpine packages linked into the static binaries in bin/, each from the source\n" +
		"Alpine built it from: the APKBUILD at the aports commit the build's apk database records, and the\n" +
		"tarball it names, checked against that APKBUILD's sha512.\n")
	for _, a := range aports {
		fmt.Fprintf(&b, "\n==> %s %s (%s)\n", a.origin, a.version, strings.Join(a.packages, ", "))
		fmt.Fprintf(&b, "aports commit: %s\nlicence:       %s\n", a.commit, a.license)
		if why, ok := noNotice[a.origin]; ok {
			fmt.Fprintf(&b, "no text:       %s\n", why)
			continue
		}
		text, err := src.licences(ctx, alpine.Version, a.apkPackage)
		if err != nil {
			return "", fmt.Errorf("the licence of %s %s: %w", a.origin, a.version, err)
		}
		b.WriteString(text)
	}
	return b.String(), nil
}

// licences is the header and the licence texts of p's source.
func (s AlpineSources) licences(ctx context.Context, series string, p apkPackage) (string, error) {
	recipe, at, err := s.apkbuild(ctx, p)
	if err != nil {
		return "", err
	}
	if v := assignment(recipe, "pkgver") + "-r" + assignment(recipe, "pkgrel"); v != p.version {
		return "", fmt.Errorf("%s at aports %s is %s, and the apk database has %s", at, p.commit, v, p.version)
	}
	file, sum, upstream, err := tarball(recipe)
	if err != nil {
		return "", fmt.Errorf("%s: %w", at, err)
	}
	mirror := s.Distfiles + "/v" + series + "/" + file
	urls := []string{mirror}
	if upstream != "" {
		urls = []string{upstream, mirror}
	}
	local, err := s.fetch(ctx, file, sum, urls)
	if err != nil {
		return "", err
	}
	texts, err := licenceFiles(ctx, local)
	if err != nil {
		return "", fmt.Errorf("%s: %w", file, err)
	}
	// Both URLs, whichever served it: the sha512 names the bytes, and the file reads the same
	// whether this release downloaded them or found them kept.
	var b strings.Builder
	fmt.Fprintf(&b, "APKBUILD:      %s\nsource:        %s\n", at, urls[0])
	if upstream != "" {
		fmt.Fprintf(&b, "mirror:        %s\n", mirror)
	}
	fmt.Fprintf(&b, "sha512:        %s\n", sum)
	for _, t := range texts {
		fmt.Fprintf(&b, "\n--> %s\n\n%s", t.name, t.text)
		if !strings.HasSuffix(t.text, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String(), nil
}

// apkbuild is p's APKBUILD at its aports commit, from main or else community, and its URL.
func (s AlpineSources) apkbuild(ctx context.Context, p apkPackage) (recipe, at string, err error) {
	var errs []error
	for _, repo := range []string{"main", "community"} {
		at = s.Aports + "/" + repo + "/" + p.origin + "/APKBUILD?id=" + p.commit
		var buf bytes.Buffer
		if err := get(ctx, at, &buf); err != nil {
			errs = append(errs, err)
			continue
		}
		return buf.String(), at, nil
	}
	return "", "", fmt.Errorf("no APKBUILD for %s at aports %s: %w", p.origin, p.commit, errors.Join(errs...))
}

// assignmentLine is a top-level shell assignment of an APKBUILD, its value on one line.
var assignmentLine = regexp.MustCompile(`(?m)^([A-Za-z_][A-Za-z0-9_]*)=("[^"\n]*"|'[^'\n]*'|[^\s"']*)\s*$`)

// assignment is the value of the variable name an APKBUILD assigns on one line, unquoted.
func assignment(recipe, name string) string {
	for _, m := range assignmentLine.FindAllStringSubmatch(recipe, -1) { // mutate-exempt: any n below 0 is every match
		if m[1] == name {
			return strings.Trim(m[2], `"'`)
		}
	}
	return ""
}

// quoted is the value of an APKBUILD's assignment to name that may run over several lines.
func quoted(recipe, name string) string {
	m := regexp.MustCompile(`(?ms)^` + name + `=(["'])(.*?)(["'])`).FindStringSubmatch(recipe)
	if m == nil {
		return ""
	}
	return m[2]
}

// archive matches a source file abuild unpacks as a tarball.
var archive = regexp.MustCompile(`\.(tar\.(gz|bz2|xz|zst|lz|lzma)|tgz|tbz2|txz)$`)

// tarball is the first tarball an APKBUILD's sha512sums names, its sha512, and the URL of its
// source= entry when every variable in it is one the APKBUILD assigns plainly; empty when one is
// not, and Alpine's distfiles serve it instead. The first, because a recipe names its own source
// first and what it vendors or tests with after.
func tarball(recipe string) (file, sum, upstream string, err error) {
	for line := range strings.Lines(quoted(recipe, "sha512sums")) {
		f := strings.Fields(line)
		if len(f) == 2 && archive.MatchString(f[1]) {
			file, sum = f[1], f[0]
			break
		}
	}
	if file == "" {
		return "", "", "", errors.New("its sha512sums name no tarball")
	}
	vars := map[string]string{}
	for _, m := range assignmentLine.FindAllStringSubmatch(recipe, -1) { // mutate-exempt: any n below 0 is every match
		vars[m[1]] = expand(strings.Trim(m[2], `"'`), vars)
	}
	for _, entry := range strings.Fields(expand(quoted(recipe, "source"), vars)) {
		name, url, renamed := strings.Cut(entry, "::")
		if !renamed {
			url, name = entry, path.Base(entry)
		}
		if name == file && !strings.Contains(url, "$") && (strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://")) {
			return file, sum, url, nil
		}
	}
	return file, sum, "", nil
}

// variable is a plain shell expansion, $name or ${name}.
var variable = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// expand replaces each plain expansion in s whose variable is in vars, and leaves the rest.
func expand(s string, vars map[string]string) string {
	return variable.ReplaceAllStringFunc(s, func(m string) string {
		name := strings.Trim(m, "${}")
		if v, ok := vars[name]; ok {
			return v
		}
		return m
	})
}

// fetch is the path of file in the cache, once its sha512 is sum: kept from an earlier release if
// it still checks, else downloaded from the first of urls that serves it with that sum. A download
// is checked before it is put where a later read finds it.
func (s AlpineSources) fetch(ctx context.Context, file, sum string, urls []string) (string, error) {
	if err := os.MkdirAll(s.Cache, 0o755); err != nil {
		return "", fmt.Errorf("making %s: %w", s.Cache, err)
	}
	local := filepath.Join(s.Cache, sum[:min(len(sum), 16)]+"-"+file) // mutate-exempt: a name only this function reads
	if got, err := fileSHA512(local); err == nil && got == sum {
		return local, nil
	}
	var errs []error
	for _, url := range urls {
		tmp, err := os.CreateTemp(s.Cache, ".download-*")
		if err != nil {
			return "", fmt.Errorf("downloading %s: %w", file, err)
		}
		h := sha512.New()
		err = errors.Join(get(ctx, url, io.MultiWriter(tmp, h)), tmp.Close())
		if got := hex.EncodeToString(h.Sum(nil)); err == nil && got != sum {
			err = fmt.Errorf("%s is sha512 %s, and the APKBUILD says %s", url, got, sum)
		}
		if err == nil {
			err = os.Rename(tmp.Name(), local)
		}
		if err == nil {
			return local, nil
		}
		_ = os.Remove(tmp.Name())
		errs = append(errs, err)
	}
	return "", fmt.Errorf("no source for %s: %w", file, errors.Join(errs...))
}

func fileSHA512(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha512.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// get writes what url serves to w, refusing an answer that is not 200.
func get(ctx context.Context, url string, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("fetching %s: %w", url, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetching %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetching %s: %s", url, resp.Status)
	}
	if _, err := io.Copy(w, resp.Body); err != nil {
		return fmt.Errorf("fetching %s: %w", url, err)
	}
	return nil
}

// licenceName matches the files a source carries its licence in.
var licenceName = regexp.MustCompile(`(?i)^(copying|licen[cs]e|copyright)`)

type licenceText struct{ name, text string }

// licenceFiles are the licence files of the tarball file: those closest to its top, which are
// the package's own, and not the ones of what it vendors further down. tar reads it, as it reads
// every compression abuild unpacks.
func licenceFiles(ctx context.Context, file string) ([]licenceText, error) {
	list, err := tarOutput(ctx, "-tf", file)
	if err != nil {
		return nil, err
	}
	listed := map[string]bool{}
	var names []string
	depth := math.MaxInt
	for name := range strings.Lines(list) {
		name = strings.TrimSpace(name)
		if slices.Contains(strings.Split(name, "/"), "..") {
			continue
		}
		listed[name] = true
		if strings.HasSuffix(name, "/") || !licenceName.MatchString(path.Base(name)) {
			continue
		}
		switch d := strings.Count(name, "/"); {
		case d < depth:
			depth, names = d, []string{name}
		case d == depth:
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil, errors.New("it has no licence file (COPYING, LICENSE, LICENCE or COPYRIGHT)")
	}
	slices.Sort(names)
	dir, err := os.MkdirTemp(filepath.Dir(file), ".licences-")
	if err != nil {
		return nil, fmt.Errorf("unpacking the licence files of %s: %w", filepath.Base(file), err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	extract := func(names []string) error {
		_, err := tarOutput(ctx, append([]string{"-xf", file, "-C", dir, "--no-same-owner", "--no-same-permissions", "--"}, names...)...)
		return err
	}
	if err := extract(names); err != nil {
		return nil, err
	}
	// A licence file that is a link names the text it is (glib's COPYING is
	// LICENSES/LGPL-2.1-or-later.txt): that text is read, when it is in the tarball and not
	// already one of names.
	targets := map[string]string{}
	var follow []string
	for _, n := range names {
		link, err := os.Readlink(filepath.Join(dir, filepath.FromSlash(n)))
		if err != nil {
			continue
		}
		// Joined, an absolute link or one that climbs out names no member of the tarball.
		t := path.Join(path.Dir(n), link)
		if !listed[t] {
			return nil, fmt.Errorf("its licence file %s links to %s, which is not in it", n, link)
		}
		if !slices.Contains(names, t) {
			targets[n] = t
			follow = append(follow, t)
		}
	}
	if len(follow) > 0 {
		if err := extract(follow); err != nil {
			return nil, err
		}
	}
	var out []licenceText
	for _, n := range names {
		name, read := n, n
		if t, ok := targets[n]; ok {
			name, read = n+" -> "+t, t
		}
		p := filepath.Join(dir, filepath.FromSlash(read))
		if fi, err := os.Lstat(p); err != nil || !fi.Mode().IsRegular() {
			continue
		}
		text, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", read, err)
		}
		out = append(out, licenceText{name, string(text)})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("none of its licence files is a text: %s", strings.Join(names, ", "))
	}
	return out, nil
}

func tarOutput(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "tar", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("tar %s: %w: %s", args[0], err, bytes.TrimSpace(stderr.Bytes()))
	}
	return string(out), nil
}
