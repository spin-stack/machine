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
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spin-stack/go-tools/versions"
	"github.com/spin-stack/machine/internal/build"
	xssh "golang.org/x/crypto/ssh"
)

// bootchartName is the edit a tree carries to be charted, and the probe that charts it.
const bootchartName = "bootchart"

// bootchartConf is systemd-bootchart's configuration in a charted overlay. Its defaults chart a
// desktop's 20 s at 25 Hz, which would draw a boot to the first login of ~300 ms in eight samples:
// 200 Hz is a sample each 5 ms, and the probe stops it at the first login, so Samples is only a
// ceiling, 10 s, for a boot nobody logs into. ScaleX is pixels a second, 2000 so that a 5 ms
// sample is 10 px wide. ControlGroup labels each process with its unit, which spin-stack's fork
// reads from cgroup v2's /proc/PID/cgroup and totals in a table of CPU by unit; Ubuntu's 235 read
// v1's hierarchy and labelled every process (null) (lab 45036002014). Cmdline stays off: it was
// on only because 235 named a process by the first word of /proc/PID/sched, which named journald,
// logind and udevd "9" (lab 45036914740), and the fork reads /proc/PID/comm again whenever a
// process runs, so the name is what it became and the unit says whose it is. What the sampling
// costs is measured, not assumed (docs/measuring.md, "Boot charts").
const bootchartConf = `[Bootchart]
Samples=2000
Frequency=200
Relative=no
Filter=yes
ScaleX=2000
Cmdline=no
ControlGroup=yes
`

// bootchartEdit is systemd-bootchart, built from spin-stack/systemd-bootchart at the commit this
// checkout's versions.yaml pins, in the overlay as PID 1: /sbin/init is pointed at it, and it
// forks its sampler and execs systemd, so the tree boots with its own command line and no init= of
// its own. The commit's tarball is fetched into the tree at dir and checked against its pin there,
// and compiled in a container (bootchart.Dockerfile), which checks it again; the edit checks the
// binary it installs is the one that build wrote.
func bootchartEdit(checkout, dir string) (Edit, error) {
	pins, err := versions.Load(filepath.Join(checkout, versions.File))
	if err != nil {
		return Edit{}, err
	}
	e, err := pins.Get("systemd-bootchart")
	if err != nil {
		return Edit{}, err
	}
	source := filepath.Join(dir, bootchartName, "source")
	if err := fetchPinned(e.URL(), e.Pin, filepath.Join(source, e.Version+".tar.gz")); err != nil {
		return Edit{}, err
	}
	args, err := build.Args(pins, strings.Fields("ubuntu systemd-bootchart")...)
	if err != nil {
		return Edit{}, err
	}
	out := filepath.Join(dir, bootchartName)
	cmd := exec.Command("docker", slices.Concat([]string{"buildx", "build", "--progress=plain", "--platform", "linux/amd64",
		"--file", filepath.Join(checkout, "internal", "lab", "bootchart.Dockerfile")}, args,
		[]string{"--output", "type=local,dest=" + out, source})...)
	if b, err := cmd.CombinedOutput(); err != nil {
		return Edit{}, fmt.Errorf("building systemd-bootchart at %s: %w: %s", e.Version, err, b)
	}
	bin := filepath.Join(out, "systemd-bootchart")
	sum, err := fileSHA256(bin)
	if err != nil {
		return Edit{}, fmt.Errorf("the systemd-bootchart build at %s wrote no binary: %w", e.Version, err)
	}
	return Edit{
		Files: map[string]string{"/etc/systemd/bootchart.conf": bootchartConf},
		Setup: `echo "` + sum + `  ` + bin + `" | sha256sum -c --quiet
install -m 0755 ` + bin + ` "$MNT/usr/lib/systemd/systemd-bootchart"
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

// DependencyFiles are the names the bootchart probe keeps repetition rep (from 1) of tree's
// dependency view under: systemd-analyze critical-chain of the login's units, and systemd-analyze
// dot --order among the units on those chains, as dot and as the SVG graphviz draws of it.
func DependencyFiles(tree string, rep int) (chain, order, graph string) {
	name := fmt.Sprintf("%s-%d", tree, rep)
	return name + ".chain.txt", name + ".order.dot", name + ".order.svg"
}

// chartData is the name the chart's samples are kept under beside the chart itself: the JSON the
// fork writes beside its SVG, for a comparison a script makes rather than a person's eye.
func chartData(chart string) string { return strings.TrimSuffix(chart, ".svg") + ".json" }

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
// same boot, kept beside result.json with the boot's dependency view (keepDependencies). It is a diagnostic and never a goal number: the sampler runs
// on the guest's vCPU beside the boot it draws, and its first_login is not ssh-first's.
//
// The first login is the one that stops the chart: its command signals the sampler, which stops at
// once and writes the chart, so the chart ends where the login did, and chart_end says where that
// is on the chart's own clock.
type bootchart struct{}

func (bootchart) Name() string { return bootchartName }
func (bootchart) About() string {
	return "a boot drawn by systemd-bootchart to the first login, which stops it, and by systemd-analyze plot, with the login's critical chains and their ordering graph: kept beside result.json"
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
// executable, the one thing every version names alike: Ubuntu's 235 left it named init, the
// /sbin/init the kernel ran, and a lookup by systemd-bootchart's name found nothing (lab
// 45035203425); the fork names it bootchart.
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

// fetchData prints the JSON the sampler wrote beside its chart, once fetchChart has seen it exit.
const fetchData = "cat /run/log/bootchart-*.json"

// fetchPlot waits for the boot to have finished, which systemd-analyze plot refuses to draw before,
// and prints its plot.
const fetchPlot = "timeout 30 systemctl is-system-running --wait >/dev/null; systemd-analyze plot --scale-svg=20"

// loginUnits are the units a login waits for, which critical-chain is asked about: the user's
// manager, whose start the login's PAM session waits for, and what accepts the connection. On
// spin's path that is the forwarder that stands for spin's supervisor and the sshd it forwards to;
// on the lane, systemd's vsock socket, which starts an sshd for each connection. $(id -u) is the
// login's own user, evaluated in the guest.
func loginUnits(spinsPath bool) string {
	if spinsPath {
		return "spin-lab-forward.service ssh.service user@$(id -u).service"
	}
	return "sshd-vsock.socket user@$(id -u).service"
}

// chainedUnit is a unit's name at the start of a line of critical-chain's tree, after the tree's
// drawing and before a space and its times: a template's instance carries an @ of its own, as
// user@0.service does.
var chainedUnit = regexp.MustCompile(`(?m)^[\s│├└─]*(\S+\.(?:service|socket|target|mount|device|slice|scope|path|timer|swap|automount))(?:\s|$)`)

// chainUnits are the units on critical-chain's chains, each once, in the order printed.
func chainUnits(chain string) []string {
	var units []string
	for _, m := range chainedUnit.FindAllStringSubmatch(chain, -1) { // mutate-exempt: any n below 0 finds every match
		if !slices.Contains(units, m[1]) {
			units = append(units, m[1])
		}
	}
	return units
}

// orderAmong is the systemd-analyze dot command for the ordering between units and nothing else:
// a pattern on both ends keeps an edge only when both its units match one, where a positional
// pattern would keep every edge into sysinit.target, which most of the boot is ordered before.
func orderAmong(units []string) string {
	var b strings.Builder
	b.WriteString("systemd-analyze dot --order")
	for _, end := range []string{"from", "to"} {
		for _, u := range units {
			fmt.Fprintf(&b, " --%s-pattern='%s'", end, u)
		}
	}
	return b.String()
}

// keepDependencies keeps the dependency view of a finished boot in m.Keep, from what run, a login
// to the guest, prints: the login's critical chains, the system manager's and the login user's
// own, and the ordering among the system chain's units, drawn by graphviz where the probe runs.
// Like the plot, a view that could not be taken is left out and the log says why; only a file
// that could not be written fails the measurement.
func keepDependencies(m *Measurement, run func(command string) (string, error)) error {
	chainFile, orderFile, graphFile := DependencyFiles(m.Tree.Name(), m.Rep+1)
	system := "systemd-analyze critical-chain --no-pager " + loginUnits(m.Key.SpinsPath)
	chain, err := run(system)
	if err != nil {
		m.Logf("no critical chain: %v", err)
		return nil
	}
	view := "$ " + system + "\n" + chain
	user := "systemd-analyze --user critical-chain --no-pager"
	if out, err := run(user); err != nil {
		m.Logf("no critical chain of the login user's manager: %v", err)
	} else {
		view += "\n$ " + user + "\n" + out
	}
	if err := os.WriteFile(filepath.Join(m.Keep, chainFile), []byte(view), 0o644); err != nil { //nolint:gosec // for a person to read
		return fmt.Errorf("keeping the critical chain: %w", err)
	}
	units := chainUnits(chain)
	if len(units) == 0 {
		m.Logf("the critical chain names no unit, so there is no ordering to draw: %q", chain)
		return nil
	}
	order, err := run(orderAmong(units))
	if err != nil {
		m.Logf("no ordering graph: %v", err)
		return nil
	}
	if err := os.WriteFile(filepath.Join(m.Keep, orderFile), []byte(order), 0o644); err != nil { //nolint:gosec // for a person and graphviz
		return fmt.Errorf("keeping the ordering graph: %w", err)
	}
	cmd := exec.Command("dot", "-Tsvg")
	cmd.Stdin = strings.NewReader(order)
	svg, err := cmd.Output()
	if err != nil {
		m.Logf("the ordering graph is kept as dot only, not drawn: %v", err)
		return nil
	}
	if err := os.WriteFile(filepath.Join(m.Keep, graphFile), svg, 0o644); err != nil { //nolint:gosec // a graph for a person to read
		return fmt.Errorf("keeping the ordering graph's drawing: %w", err)
	}
	return nil
}

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
	// The data and the plot are the chart's companions: a chart without them is still kept, and
	// the log says why they are not.
	if _, data, err := m.Key.Run(cid, xssh.KeyExchangeMLKEM768X25519, fetchData); err != nil {
		m.Logf("no chart data: %v", err)
	} else if err := os.WriteFile(filepath.Join(m.Keep, chartData(chart)), []byte(data), 0o644); err != nil { //nolint:gosec // data for a person's scripts
		return fmt.Errorf("keeping the chart's data: %w", err)
	}
	// A plot is the chart's companion, and a boot that never finished draws none: the chart is
	// still kept, and the log says why the plot is not.
	if _, plot, err := m.Key.Run(cid, xssh.KeyExchangeMLKEM768X25519, fetchPlot); err != nil {
		m.Logf("no systemd-analyze plot: %v", err)
	} else {
		if marked, err := markLogin(plot, end); err != nil {
			m.Logf("the plot is kept without its first login: %v", err)
		} else {
			plot = marked
		}
		if err := os.WriteFile(filepath.Join(m.Keep, plotFile), []byte(plot), 0o644); err != nil { //nolint:gosec // a chart for a person to read
			return fmt.Errorf("keeping the plot: %w", err)
		}
	}
	if err := keepDependencies(m, func(command string) (string, error) {
		_, out, err := m.Key.Run(cid, xssh.KeyExchangeMLKEM768X25519, command)
		return out, err
	}); err != nil {
		return err
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

// The plot's geometry, as systemd-analyze plot draws it (analyze-plot.c): its units are drawn in
// a translated group whose first child is the box they sit in; the kernel's bar starts where the
// kernel did, which is the plot's 0 only when the firmware reports no time of its own; and its
// first faint line is a tenth of a second from 0.
var (
	plotBox    = regexp.MustCompile(`<g transform="(translate\([0-9.]+, ?[0-9.]+\))">\s*<rect class="box" x="0" y="0" width="[0-9.]+" height="([0-9.]+)"`)
	plotKernel = regexp.MustCompile(`<rect class="kernel" x="([0-9.]+)"`)
	plotTenth  = regexp.MustCompile(`<line class="sec01" x1="([0-9.]+)"`)
)

// markLogin draws the first login on a plot, at s seconds of the kernel's clock: chart_end, where
// the bootchart ends. The plot is taken once the boot has finished, after the probe's three
// later logins, so it runs past the first login, and the first login's own session scope has
// ended and is gone from it by then: without the line, the login the chart is about is nowhere on
// the plot, and units that started after it read as if the login waited for them.
func markLogin(plot string, s float64) (string, error) {
	box, kernel, tenth := plotBox.FindStringSubmatch(plot), plotKernel.FindStringSubmatch(plot), plotTenth.FindStringSubmatch(plot)
	head, ended := strings.CutSuffix(strings.TrimRight(plot, "\n"), "</svg>")
	if box == nil || kernel == nil || tenth == nil || !ended {
		return "", fmt.Errorf("what the guest printed is not a systemd-analyze plot: no unit box, kernel bar, tenth-of-a-second line or end in its %d bytes", len(plot))
	}
	height, _ := strconv.ParseFloat(box[2], 64) // mutate-exempt: ParseFloat reads any bitSize but 32 as 64
	x0, _ := strconv.ParseFloat(kernel[1], 64)  // mutate-exempt: ParseFloat reads any bitSize but 32 as 64
	px, _ := strconv.ParseFloat(tenth[1], 64)   // mutate-exempt: ParseFloat reads any bitSize but 32 as 64
	x := x0 + s*10*px
	mark := fmt.Sprintf(`<g transform="%s">
  <line x1="%.3f" y1="0" x2="%.3f" y2="%.3f" style="stroke: rgb(0,128,0); stroke-width: 2; stroke-dasharray: 6,4" />
  <text x="%.3f" y="-20" style="fill: rgb(0,128,0)">first login %.0f ms</text>
</g>
`, box[1], x, x, height, x+4, s*1000)
	return head + mark + "</svg>\n", nil
}

// Detail names the charts the probe kept, which result.json does not hold.
func (bootchart) Detail(r *Result) string {
	var b strings.Builder
	b.WriteString("\ncharts, beside result.json:\n")
	for _, s := range r.Samples {
		if s.Missed == "" {
			chart, plot := ChartFiles(s.Tree, s.Rep)
			chain, order, graph := DependencyFiles(s.Tree, s.Rep)
			fmt.Fprintf(&b, "  %s, %s, %s, %s, %s, %s\n", chart, chartData(chart), plot, chain, order, graph)
		}
	}
	return b.String()
}
