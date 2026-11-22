// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spin-stack/go-tools/versions"
	xssh "golang.org/x/crypto/ssh"
)

// bootchartName is the edit a tree carries to be charted, and the probe that charts it.
const bootchartName = "bootchart"

// bootchartConf is systemd-bootchart's configuration in a charted overlay. Its defaults chart a
// desktop's 20 s at 25 Hz, which would draw a boot to the first login of ~300 ms in eight samples:
// 200 Hz is a sample each 5 ms, and the probe stops it at the first login, so Samples is only a
// ceiling, 10 s, for a boot nobody logs into. ScaleX is pixels a second, 2000 so that a 5 ms
// sample is 10 px wide. Cmdline names a process by its command line: by default version 235 takes
// the first word of /proc/PID/sched, and it named journald, logind and udevd "9" (lab 45036914740).
// ControlGroup stays off: it reads cgroup v1's hierarchy and labelled every process (null) on this
// machine's v2 (lab 45036002014). What the sampling costs is measured, not assumed
// (docs/measuring.md, "Boot charts").
const bootchartConf = `[Bootchart]
Samples=2000
Frequency=200
Relative=no
Filter=yes
ScaleX=2000
Cmdline=yes
ControlGroup=no
`

// bootchartEdit is systemd-bootchart, Ubuntu's package at its pin in this checkout's
// versions.yaml, in the overlay as PID 1: /sbin/init is pointed at it, and it forks its sampler
// and execs systemd, so the tree boots with its own command line and no init= of its own. The
// package is fetched into the tree at dir and checked against its pin there, and again by the
// edit before it installs it.
func bootchartEdit(checkout, dir string) (Edit, error) {
	pins, err := versions.Load(filepath.Join(checkout, versions.File))
	if err != nil {
		return Edit{}, err
	}
	e, err := pins.Get("systemd-bootchart")
	if err != nil {
		return Edit{}, err
	}
	deb := filepath.Join(dir, bootchartName, path.Base(e.URL()))
	if err := fetchPinned(e.URL(), e.Pin, deb); err != nil {
		return Edit{}, err
	}
	return Edit{
		Files: map[string]string{"/etc/systemd/bootchart.conf": bootchartConf},
		// Only the binary: the package's bootchart.conf is its defaults, and its unit charts a boot
		// from where systemd starts it, which is not this one.
		Setup: `echo "` + e.Pin + `  ` + deb + `" | sha256sum -c --quiet
dpkg-deb --fsys-tarfile ` + deb + ` | tar -x -C "$MNT" ./usr/lib/systemd/systemd-bootchart
test -x "$MNT/usr/lib/systemd/systemd-bootchart"
test "$(basename "$(readlink "$MNT/usr/sbin/init")")" = systemd || { echo "the overlay's /sbin/init is not systemd, so a chart of it would not be of this machine" >&2; exit 1; }
ln -sfn /usr/lib/systemd/systemd-bootchart "$MNT/usr/sbin/init"
`,
	}, nil
}

// ChartFiles are the names the bootchart probe keeps repetition rep (from 1) of tree under:
// systemd-bootchart's chart and systemd-analyze plot's.
func ChartFiles(tree string, rep int) (bootchart, plot string) {
	name := fmt.Sprintf("%s-%d", tree, rep)
	return name + ".bootchart.svg", name + ".plot.svg"
}

// ChartSuffix names the tree releases measure draws a release with, after the release: the release
// with the bootchart edit.
const ChartSuffix = "-" + bootchartName

// fetchPinned downloads url to path unless a file is there, and refuses one whose sha256 is not pin
// either way: a file kept from an earlier tree is checked as a fresh one is.
func fetchPinned(url, pin, path string) error {
	if _, err := os.Stat(path); err != nil {
		if err := download(url, path); err != nil {
			return err
		}
	}
	b, err := os.ReadFile(path) //nolint:gosec // the tree's own copy of a pinned download
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	sum := sha256.Sum256(b)
	if got := hex.EncodeToString(sum[:]); got != pin {
		_ = os.Remove(path)
		return fmt.Errorf("%s from %s has sha256 %s, and versions.yaml pins %s", path, url, got, pin)
	}
	return nil
}

func download(url, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("making %s: %w", filepath.Dir(path), err)
	}
	resp, err := http.Get(url) //nolint:gosec,noctx // a URL from versions.yaml, checked against its pin
	if err != nil {
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading %s: %s", url, resp.Status)
	}
	f, err := os.Create(path) //nolint:gosec // under the tree being made
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = f.Close()
		return fmt.Errorf("downloading %s to %s: %w", url, path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// bootchart draws a tree's boot, a tree made with the bootchart edit: systemd-bootchart's chart of
// every process from PID 1 to the first login, and systemd-analyze plot's of every unit, from the
// same boot, kept beside result.json. It is a diagnostic and never a goal number: the sampler runs
// on the guest's vCPU beside the boot it draws, and its first_login is not ssh-first's.
//
// The first login is the one that stops the chart: its command signals the sampler, which stops at
// once and writes the chart, so the chart ends where the login did, and chart_end says where that
// is on the chart's own clock.
type bootchart struct{}

func (bootchart) Name() string { return bootchartName }
func (bootchart) About() string {
	return "a boot drawn by systemd-bootchart to the first login, which stops it, and by systemd-analyze plot: charts kept beside result.json"
}
func (bootchart) Options() []Option     { return shape }
func (bootchart) Rows(Options) []string { return []string{"boot"} }
func (bootchart) Phases() int           { return 1 }

func (bootchart) Needs(o Options) []Need {
	return append(append(bootNeeds, Need{"boot", "vsock-cid"}, Need{"boot", "append"}), plugNeeds(o)...)
}

func (bootchart) Metrics() []Metric {
	return []Metric{
		{"first_login", "ms", "the login that stops the chart, its command included, from the moment before QEMU is exec'd, with the sampler running"},
		{"pid1", "ms", "the sampler's start, PID 1's, on the chart's clock: the kernel's since it started"},
		{"chart_end", "ms", "the first login's command on the chart's clock, where the chart ends"},
		{"dropped", "count", "samples the sampler could not take in time, which the chart leaves out"},
		{"chart", "KiB", "the bootchart SVG's size"},
	}
}

// sampler prints the pid of systemd-bootchart's sampler while it runs, as root, found by its
// executable: its name is init, the /sbin/init the kernel ran and the sampler forked from before
// PID 1 exec'd systemd, and a lookup by systemd-bootchart's name found nothing (lab 45035203425).
// test -ef and no readlink: a fork for each of the guest's ~200 processes kept the stop command on its
// one vCPU for ~140 ms, and the probe's first login read 569 ms p50 against 426 without (labs
// 45036494125, 45036914740).
const sampler = `for p in /proc/[0-9]*/exe; do [ "$p" -ef /usr/lib/systemd/systemd-bootchart ] && p=${p%/exe} && echo ${p#/proc/}; done; true`

// stopChart is the first login's command: where on the chart it ran, and the signal that stops the
// sampler, whose pid it prints, as root: asRoot is "" for spin's login, which is root's, and sudo
// for the lane's. Not sudo for root as well: it ran beside the boot it charts, and the chart drew
// sudo and its PAM session for ~0.1 s past the login (lab 45036002014).
func stopChart(asRoot string) string {
	return `cut -d' ' -f1 /proc/uptime; ` + asRoot + `sh -c 'pids=$(` + sampler + `); [ -z "$pids" ] || kill -HUP $pids; echo $pids'`
}

// fetchChart waits for the sampler to have written the chart and exited, then prints it.
const fetchChart = `timeout 30 sudo -n sh -c 'while [ -n "$(` + sampler + `)" ]; do sleep 0.05; done' && cat /run/log/bootchart-*.svg`

// fetchPlot waits for the boot to have finished, which systemd-analyze plot refuses to draw before,
// and prints its plot.
const fetchPlot = "timeout 30 systemctl is-system-running --wait >/dev/null; systemd-analyze plot --scale-svg=20"

// mutate-lane: TestEveryProbeMeasuresATree
func (bootchart) Measure(_ context.Context, m *Measurement) error {
	if !slices.ContainsFunc(m.Tree.Manifest.Edits, func(e NamedEdit) bool { return e.Name == bootchartName }) {
		return Refuse("tree %s has no bootchart edit to draw it: make it with --edit bootchart", m.Tree.Name())
	}
	if m.Keep == "" {
		return fmt.Errorf("the bootchart probe keeps its charts, and the experiment names no directory to keep them in")
	}
	overlay, err := m.Overlay("overlay.qcow2", m.Key.Edits()...)
	if err != nil {
		return err
	}
	cid := NewCID()
	t0 := time.Now()
	qmp := filepath.Join(m.Socks, "m.qmp")
	args := append([]string{"--vsock-cid", fmt.Sprint(cid), "--append", m.Tree.Cmdline()}, m.qmpToPlug(qmp)...)
	mc, err := m.Launch(nil, m.BootArgs(overlay, args...)...)
	if err != nil {
		return err
	}
	plugged := m.Plug(qmp)
	defer mc.Stop()

	stop := stopChart("")
	if m.Key.User() != "root" {
		stop = stopChart("sudo -n ")
	}
	deadline := t0.Add(60 * time.Second)
	var uptime string
	why := error(nil)
	for time.Now().Before(deadline) {
		if _, uptime, why = m.Key.Run(cid, xssh.KeyExchangeMLKEM768X25519, stop); why == nil {
			break
		}
		if err := mc.Exited(); err != nil {
			return err
		}
		time.Sleep(2 * time.Millisecond)
	}
	if why != nil {
		return Miss("never logged into on vsock to stop the chart: %v", why)
	}
	login := time.Since(t0)
	if err := plugged(); err != nil {
		return err
	}
	said := strings.Fields(uptime)
	if len(said) < 2 {
		return Miss("no sampler was running at the first login to stop: the login said %q", uptime)
	}
	end, err := strconv.ParseFloat(said[0], 64)
	if err != nil {
		return Miss("the login that stopped the chart printed no uptime: %q", uptime)
	}
	_, svg, err := m.Key.Run(cid, xssh.KeyExchangeMLKEM768X25519, fetchChart)
	if err != nil {
		return Miss("the sampler wrote no chart: %v", err)
	}
	start, dropped, err := chartHeader(svg)
	if err != nil {
		return Miss("%v", err)
	}
	chart, plotFile := ChartFiles(m.Tree.Name(), m.Rep+1)
	if err := os.WriteFile(filepath.Join(m.Keep, chart), []byte(svg), 0o644); err != nil { //nolint:gosec // a chart for a person to read
		return fmt.Errorf("keeping the chart: %w", err)
	}
	// A plot is the chart's companion, and a boot that never finished draws none: the chart is
	// still kept, and the log says why the plot is not.
	if _, plot, err := m.Key.Run(cid, xssh.KeyExchangeMLKEM768X25519, fetchPlot); err != nil {
		m.Logf("no systemd-analyze plot: %v", err)
	} else if err := os.WriteFile(filepath.Join(m.Keep, plotFile), []byte(plot), 0o644); err != nil { //nolint:gosec // a chart for a person to read
		return fmt.Errorf("keeping the plot: %w", err)
	}
	m.RecordMs("first_login", login)
	m.Record("pid1", start*1000)
	m.Record("chart_end", end*1000)
	m.Record("dropped", float64(dropped))
	m.Record("chart", float64(len(svg))/1024)
	m.Logf("first login %v, the chart from %.0f to %.0f ms of the kernel's clock, %d samples dropped, kept as %s",
		login.Round(time.Millisecond), start*1000, end*1000, dropped, chart)
	return nil
}

var (
	chartStart   = regexp.MustCompile(`Log start time: ([0-9.]+)s`)
	chartDropped = regexp.MustCompile(`dropped ([0-9]+) samples`)
)

// chartHeader is what systemd-bootchart's SVG says of itself (svg.c's svg_title): when its sampling
// started, in seconds of the kernel's clock, and how many samples it dropped.
func chartHeader(svg string) (start float64, dropped int, err error) {
	s, d := chartStart.FindStringSubmatch(svg), chartDropped.FindStringSubmatch(svg)
	if s == nil || d == nil {
		return 0, 0, fmt.Errorf("what the guest printed is not a bootchart: no start time or dropped samples in its %d bytes", len(svg))
	}
	start, _ = strconv.ParseFloat(s[1], 64) // mutate-exempt: ParseFloat reads any bitSize but 32 as 64
	dropped, _ = strconv.Atoi(d[1])
	return start, dropped, nil
}

// Detail names the charts the probe kept, which result.json does not hold.
func (bootchart) Detail(r *Result) string {
	var b strings.Builder
	b.WriteString("\ncharts, beside result.json:\n")
	for _, s := range r.Samples {
		if s.Missed == "" {
			chart, plot := ChartFiles(s.Tree, s.Rep)
			fmt.Fprintf(&b, "  %s, %s\n", chart, plot)
		}
	}
	return b.String()
}
