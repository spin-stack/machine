// SPDX-License-Identifier: Apache-2.0

package patched

import (
	"bufio"
	"context"
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The QEMU sanitize.yml runs under AddressSanitizer and UndefinedBehaviorSanitizer (#653): the
// release's configuration, from the release's own Dockerfile, built from QEMU's patched tree with
// both sanitizers, and its reports traced back to the patch whose lines they stop in. It is a
// lab build: nothing ships it, and it is dynamically linked against the runner's libraries,
// because neither sanitizer's runtime links into a static musl binary as the release's is.

// notSanitized is each flag of the release's configuration the sanitized build leaves out, and
// why.
var notSanitized = map[string]string{
	"--static":                           "the sanitizers' runtimes are shared libraries of glibc's toolchain; the release is static against musl, which neither supports",
	"--enable-lto":                       "a report needs the source line, not a smaller binary, and LTO is most of a build's link",
	"--enable-strip":                     "a report's frames are named from the debug info",
	"--disable-debug-info":               "a report's frames are named from the debug info",
	"--extra-cflags=-U_FORTIFY_SOURCE":   "fortify's checked copies stand between ASan's interceptors and the accesses it checks",
	"--extra-cflags=-D_FORTIFY_SOURCE=3": "fortify's checked copies stand between ASan's interceptors and the accesses it checks",
	"--enable-malloc=jemalloc":           "ASan replaces malloc with its own, which is how it finds a use after free",
}

// releaseFlags is the configure flags qemu/Dockerfile builds the release with: its ENV QEMU_FLAGS.
func releaseFlags(dockerfile []byte) ([]string, error) {
	_, rest, ok := strings.Cut(string(dockerfile), "\nENV QEMU_FLAGS=\"")
	if !ok {
		return nil, errors.New("qemu/Dockerfile sets no QEMU_FLAGS")
	}
	value, _, ok := strings.Cut(rest, "\"")
	if !ok {
		return nil, errors.New("qemu/Dockerfile's QEMU_FLAGS has no end")
	}
	flags := strings.Fields(strings.ReplaceAll(value, "\\\n", " "))
	if len(flags) == 0 {
		return nil, errors.New("qemu/Dockerfile's QEMU_FLAGS is empty")
	}
	return flags, nil
}

// sanitizedFlags is the release's flags without those notSanitized names, with both sanitizers,
// debug info and no TCG, as the release's KVM binary has none.
func sanitizedFlags(release []string) []string {
	var flags []string
	for _, f := range release {
		if _, out := notSanitized[f]; !out {
			flags = append(flags, f)
		}
	}
	return append(flags, "--disable-tcg", "--enable-asan", "--enable-ubsan", "--enable-debug-info")
}

// Sanitized builds QEMU's tree in dir, which Tree made, as the release does but under both
// sanitizers, and lays it out in dir/sanitized as a lab tree takes a QEMU (--qemu): its
// qemu-system-x86_64 in bin/ and, in qemu/, the firmware of the QEMU at firmware, which is the
// release's, so that a machine's record is the one the release's QEMU gives. root is the
// checkout. It returns dir/sanitized, and refuses a binary without either runtime.
func Sanitized(ctx context.Context, root, dir, firmware string, out io.Writer) (string, error) {
	dockerfile, err := os.ReadFile(filepath.Join(root, "qemu", "Dockerfile"))
	if err != nil {
		return "", fmt.Errorf("reading the release's configuration: %w", err)
	}
	release, err := releaseFlags(dockerfile)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(firmware, "qboot.bin")); err != nil {
		return "", fmt.Errorf("no release firmware at %s (spin-machine-dev fetch qemu puts it there): %w", firmware, err)
	}
	src := filepath.Join(dir, "src")
	devices, err := os.ReadFile(filepath.Join(root, "qemu", "devices.mak"))
	if err != nil {
		return "", fmt.Errorf("reading the release's devices: %w", err)
	}
	if err := os.WriteFile(filepath.Join(src, "configs", "devices", "x86_64-softmmu", "spin.mak"), devices, 0o644); err != nil {
		return "", fmt.Errorf("adding the release's devices to the tree: %w", err)
	}
	build := filepath.Join(dir, "build-sanitized")
	if err := os.MkdirAll(build, 0o755); err != nil {
		return "", err
	}
	if err := shown(ctx, out, build, filepath.Join(src, "configure"), sanitizedFlags(release)...); err != nil {
		return "", err
	}
	if err := shown(ctx, out, build, "ninja", "-C", build, "qemu-system-x86_64"); err != nil {
		return "", err
	}
	binary := filepath.Join(build, "qemu-system-x86_64")
	if err := sanitizedBinary(binary); err != nil {
		return "", err
	}
	laid := filepath.Join(dir, "sanitized")
	if err := os.RemoveAll(laid); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(laid, "bin"), 0o755); err != nil {
		return "", err
	}
	if out, err := exec.CommandContext(ctx, "cp", binary, filepath.Join(laid, "bin")).CombinedOutput(); err != nil {
		return "", fmt.Errorf("placing %s: %w: %s", binary, err, out)
	}
	if out, err := exec.CommandContext(ctx, "cp", "-r", firmware, filepath.Join(laid, "qemu")).CombinedOutput(); err != nil {
		return "", fmt.Errorf("copying the firmware at %s: %w: %s", firmware, err, out)
	}
	return laid, nil
}

// Fuzzer builds QEMU's libFuzzer binary, qemu-fuzz-x86_64, from the tree in dir under both
// sanitizers, with clang, as QEMU's docs/devel/testing/fuzzing.rst does: every device QEMU builds
// by default, and the qtest accelerator its fuzz targets drive them with. It returns the binary.
func Fuzzer(ctx context.Context, dir string, out io.Writer) (string, error) {
	build := filepath.Join(dir, "build-fuzz")
	if err := os.MkdirAll(build, 0o755); err != nil {
		return "", err
	}
	configure := filepath.Join(dir, "src", "configure")
	if err := shown(ctx, out, build, configure, "--cc=clang", "--cxx=clang++", "--enable-fuzzing",
		"--enable-asan", "--enable-ubsan", "--target-list=x86_64-softmmu", "--disable-docs", "--disable-werror"); err != nil {
		return "", err
	}
	if err := shown(ctx, out, build, "ninja", "-C", build, "qemu-fuzz-x86_64"); err != nil {
		return "", err
	}
	binary := filepath.Join(build, "qemu-fuzz-x86_64")
	return binary, sanitizedBinary(binary)
}

// sanitizedBinary refuses a binary that does not call into both runtimes: configure that
// quietly found no sanitizer builds a QEMU every run of which is clean.
func sanitizedBinary(path string) error {
	f, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	syms, err := f.DynamicSymbols()
	if err != nil {
		return fmt.Errorf("reading %s's symbols: %w", path, err)
	}
	for _, want := range []string{"__asan_init", "__ubsan_handle_"} {
		if !slices.ContainsFunc(syms, func(s elf.Symbol) bool { return strings.HasPrefix(s.Name, want) }) {
			return fmt.Errorf("%s calls no %s: it was built without its sanitizer", path, want)
		}
	}
	return nil
}

// SanitizerOptions is the environment a sanitized QEMU runs in, writing every report and every
// process's start to files under logs: each process opens logs/sanitizer.<pid> as it starts
// (verbosity=1), so a run whose logs hold no start ran no sanitized QEMU. Leaks are not
// reported: the harness kills most machines, and QEMU leaks at a quit upstream does not hunt.
func SanitizerOptions(logs string) []string {
	logPath := "log_path=" + filepath.Join(logs, "sanitizer")
	return []string{
		"ASAN_OPTIONS=" + logPath + ":verbosity=1:detect_leaks=0",
		"UBSAN_OPTIONS=" + logPath + ":print_stacktrace=1",
	}
}

// A Report is one error a sanitizer reported: what it was, the file it was written to, and where
// it stopped.
type Report struct {
	Kind, Log string
	// Frames are the report's stack, innermost first, each a file in the tree and its line.
	Frames []Frame
	// Patch names the patches whose touched file holds the innermost frame in one, and whether
	// the frame's line is one they added; empty, the report is in upstream's code alone.
	Patch string
}

// A Frame is a place in the tree: a file, as a path in it, and a line.
type Frame struct {
	File string
	Line int
}

// Title is the report's kind and innermost place in the tree: what tells two reports apart.
func (r Report) Title() string {
	top := "no frame in the tree"
	if len(r.Frames) > 0 {
		top = fmt.Sprintf("%s:%d", r.Frames[0].File, r.Frames[0].Line)
	}
	return r.Kind + " at " + top
}

func (r Report) String() string {
	where := "upstream"
	if r.Patch != "" {
		where = r.Patch
	}
	return fmt.Sprintf("%s: %s (%s)", r.Title(), where, filepath.Base(r.Log))
}

var (
	processLine = regexp.MustCompile(`^==\d+==`)
	asanError   = regexp.MustCompile(`^==\d+==ERROR: (\w+Sanitizer: \S+)`)
	ubsanError  = regexp.MustCompile(`^(\S+?):(\d+):\d+: runtime error: (.*)$`)
	frame       = regexp.MustCompile(`^\s*#\d+ 0x[0-9a-f]+ in \S+ (\S+?):(\d+)(?::\d+)?$`)
)

// Reports reads every sanitizer log in logs, which a sanitized QEMU from dir's tree wrote, and
// returns each error, named by the patch it stopped in, and how many processes ran. An error
// with no process is a run that sanitized nothing.
func Reports(ctx context.Context, root string, p Part, dir, logs string) ([]Report, int, error) {
	files, err := filepath.Glob(filepath.Join(logs, "*"))
	if err != nil {
		return nil, 0, err
	}
	patches, err := filepath.Glob(filepath.Join(root, p.Patches))
	if err != nil {
		return nil, 0, err
	}
	at, err := newAttribution(ctx, dir, patches)
	if err != nil {
		return nil, 0, err
	}
	var reports []Report
	ran := 0
	for _, f := range files {
		rs, started, err := readLog(f, filepath.Join(dir, "src"))
		if err != nil {
			return nil, 0, err
		}
		if started {
			ran++
		}
		for _, r := range rs {
			r.Patch = at.patch(r.Frames)
			reports = append(reports, r)
		}
	}
	return reports, ran, nil
}

// readLog is the reports in one log, and whether it records a sanitized process's start. src is
// the tree the binary was built from, which a frame's path is made relative to.
func readLog(path, src string) ([]Report, bool, error) {
	f, err := os.Open(path) // #nosec G304 -- a log this run's sanitizer wrote
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = f.Close() }()
	var reports []Report
	started := false
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 64<<10), 1<<20) // mutate-exempt: the first buffer's size; it grows to the same bound
	for s.Scan() {
		line := s.Text()
		if processLine.MatchString(line) {
			started = true
		}
		if m := asanError.FindStringSubmatch(line); m != nil {
			reports = append(reports, Report{Kind: m[1], Log: path})
			continue
		}
		if m := ubsanError.FindStringSubmatch(line); m != nil {
			started = true
			r := Report{Kind: "runtime error: " + m[3], Log: path}
			if fr, ok := inTreeFrame(m[1], m[2], src); ok {
				r.Frames = append(r.Frames, fr)
			}
			reports = append(reports, r)
			continue
		}
		if m := frame.FindStringSubmatch(line); m != nil && len(reports) > 0 {
			if fr, ok := inTreeFrame(m[1], m[2], src); ok {
				last := &reports[len(reports)-1]
				if !slices.Contains(last.Frames, fr) {
					last.Frames = append(last.Frames, fr)
				}
			}
		}
	}
	return reports, started, s.Err()
}

// inTreeFrame is file:line as a place in src's tree, or false for a file outside it (a system
// library's). A relative path is the build directory's, which is beside src's files.
func inTreeFrame(file, line, src string) (Frame, bool) {
	n, err := strconv.Atoi(line)
	if err != nil {
		return Frame{}, false
	}
	if !filepath.IsAbs(file) {
		file = filepath.Join(src, "build", file)
	}
	rel, err := filepath.Rel(src, filepath.Clean(file))
	if err != nil || strings.HasPrefix(rel, "..") {
		return Frame{}, false
	}
	return Frame{File: filepath.ToSlash(rel), Line: n}, true
}

// attribution is which patches touch each file, and the lines all of them added there.
type attribution struct {
	by    map[string][]string
	added map[string]map[int]bool
}

func newAttribution(ctx context.Context, dir string, patches []string) (attribution, error) {
	at := attribution{by: map[string][]string{}, added: map[string]map[int]bool{}}
	for _, p := range patches {
		touched, err := Touched([]string{p})
		if err != nil {
			return at, fmt.Errorf("%s: %w", filepath.Base(p), err)
		}
		for _, f := range touched {
			at.by[f] = append(at.by[f], filepath.Base(p))
		}
	}
	for f := range at.by {
		lines, err := addedLines(ctx, filepath.Join(dir, "upstream", f), filepath.Join(dir, "src", f))
		if err != nil {
			return at, err
		}
		at.added[f] = lines
	}
	return at, nil
}

// patch names the patches touching the innermost frame in a file they touch, and whether its line
// is one they added: a report that stops in upstream's code under a patch's call is the patch's
// as much as one in its own line.
func (at attribution) patch(frames []Frame) string {
	for _, fr := range frames {
		ps, ok := at.by[fr.File]
		if !ok {
			continue
		}
		how := "in a file it touches"
		if at.added[fr.File][fr.Line] {
			how = "on a line it added"
		}
		return fmt.Sprintf("%s, %s:%d %s", strings.Join(ps, " "), fr.File, fr.Line, how)
	}
	return ""
}
