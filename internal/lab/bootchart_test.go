// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const bootchartCommit = "c9eb2b24491a19fd209fa59b264c3f43dc4d06d2"

// checkoutPinning is a checkout whose versions.yaml pins systemd-bootchart's source to body, served
// by a server of the test's own.
func checkoutPinning(t *testing.T, body string, pin string) string {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
	t.Cleanup(srv.Close)
	// The server's certificate trusted, as GitHub's is.
	saved := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = saved })
	checkout := t.TempDir()
	write(t, filepath.Join(checkout, "versions.yaml"), []byte(`- name: ubuntu
  kind: image
  source: mirror.gcr.io/library/ubuntu
  version: '26.04'
  pin: sha256:`+sha("ubuntu")+`
  track: digest
- name: systemd-bootchart
  kind: download
  source: `+srv.URL+`/archive/{version}.tar.gz
  version: `+bootchartCommit+`
  pin: `+pin+`
  track: commit https://github.com/spin-stack/systemd-bootchart.git main
`))
	return checkout
}

// fakeDocker puts a docker on the PATH that records its arguments and runs script, and returns
// where the arguments are recorded: no file there is no build.
func fakeDocker(t *testing.T, script string) string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	write(t, filepath.Join(bin, "docker"), []byte("#!/bin/sh\necho \"$*\" >> "+log+"\n"+script+"\n"))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

// builds is a docker that writes body as the binary into the directory its --output names.
func builds(body string) string {
	return `for a; do case "$a" in type=local,dest=*) printf '` + body + `' > "${a#type=local,dest=}/systemd-bootchart";; esac; done`
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// The bootchart edit compiles the source its checkout pins, fetched into the tree, and installs
// the binary that build wrote, and only that.
func TestTheBootchartEditInstallsWhatThePinnedSourceBuilt(t *testing.T) {
	dir := t.TempDir()
	log := fakeDocker(t, builds("the binary"))
	e, err := namedEdit("bootchart", checkoutPinning(t, "the source", sha("the source")), dir)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "bootchart", "source")
	if b, err := os.ReadFile(filepath.Join(source, bootchartCommit+".tar.gz")); err != nil || string(b) != "the source" {
		t.Errorf("the tree's source = %q, %v", b, err)
	}
	call, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"SYSTEMD_BOOTCHART_SHA256=" + sha("the source"), "SYSTEMD_BOOTCHART_VERSION=" + bootchartCommit,
		"UBUNTU_IMAGE=mirror.gcr.io/library/ubuntu:26.04@sha256:" + sha("ubuntu"), "bootchart.Dockerfile"} {
		if !strings.Contains(string(call), want) {
			t.Errorf("the build was not handed %s: %s", want, call)
		}
	}
	if f := strings.Fields(string(call)); f[len(f)-1] != source {
		t.Errorf("the build's context is not the fetched source %s: %s", source, call)
	}
	bin := filepath.Join(dir, "bootchart", "systemd-bootchart")
	if !strings.Contains(e.Setup, sha("the binary")+"  "+bin) || !strings.Contains(e.Setup, "install -m 0755 "+bin+` "$MNT/usr/lib/systemd/systemd-bootchart"`) ||
		!strings.Contains(e.Setup, `ln -sfn /usr/lib/systemd/systemd-bootchart "$MNT/usr/sbin/init"`) {
		t.Errorf("the edit neither checks the binary built nor boots it:\n%s", e.Setup)
	}
	conf := e.Files["/etc/systemd/bootchart.conf"]
	if !strings.Contains(conf, "Frequency=200") || !strings.Contains(conf, "ControlGroup=yes") {
		t.Errorf("bootchart.conf = %q", conf)
	}
}

// A source that is not the pin's is never built, and a build that fails or writes no binary makes
// no edit and says why.
func TestTheBootchartEditRefusesWhatItCannotBuild(t *testing.T) {
	dir := t.TempDir()
	log := fakeDocker(t, builds("the binary"))
	if _, err := namedEdit("bootchart", checkoutPinning(t, "another source", sha("the source")), dir); err == nil ||
		!strings.Contains(err.Error(), "versions.yaml pins "+sha("the source")) {
		t.Errorf("a source that is not the pin's: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "bootchart", "source", bootchartCommit+".tar.gz")); err == nil {
		t.Error("a source that is not the pin's was left in the tree")
	}
	if _, err := os.Stat(log); err == nil {
		t.Error("a source that is not the pin's was built")
	}

	fakeDocker(t, "echo 'configure: error: no C compiler'; exit 1")
	if _, err := namedEdit("bootchart", checkoutPinning(t, "the source", sha("the source")), t.TempDir()); err == nil ||
		!strings.Contains(err.Error(), "building systemd-bootchart at "+bootchartCommit) || !strings.Contains(err.Error(), "no C compiler") {
		t.Errorf("a build that failed: %v", err)
	}
	fakeDocker(t, "")
	if _, err := namedEdit("bootchart", checkoutPinning(t, "the source", sha("the source")), t.TempDir()); err == nil ||
		!strings.Contains(err.Error(), "wrote no binary") {
		t.Errorf("a build that wrote nothing: %v", err)
	}
}

// What a chart says of itself is read off its header, and what is not a chart is refused.
func TestAChartsHeaderSaysWhenItStartedAndWhatItDropped(t *testing.T) {
	svg := `<text class="t2" x="20" y="125">Log start time: 0.152s</text>
<text class="sec" x="20" y="155">Graph data: 200.000 samples/sec, recorded 1990 total, dropped 3 samples, 41 processes, 12 filtered</text>`
	start, dropped, err := chartHeader(svg)
	if err != nil || start != 0.152 || dropped != 3 {
		t.Errorf("chartHeader = %v, %d, %v", start, dropped, err)
	}
	for _, notAChart := range []string{"cat: /run/log/bootchart-*.svg: No such file or directory",
		"Log start time: 0.152s", "dropped 3 samples"} {
		if _, _, err := chartHeader(notAChart); err == nil {
			t.Errorf("%q read as a chart", notAChart)
		}
	}
}

// The command that stops the chart runs in a shell, says where it ran on the kernel's clock, and
// names no sampler where none runs, as on this host.
func TestStoppingAChartWithNoSamplerSaysOnlyTheTime(t *testing.T) {
	out, err := exec.Command("sh", "-c", stopChart("")).Output()
	if f := strings.Fields(string(out)); err != nil || len(f) != 1 {
		t.Errorf("stopChart said %q, %v", out, err)
	}
}

// A tree without the bootchart edit has nothing to draw: its measurements are refused, said once,
// and none is taken as a chart.
func TestBootchartRefusesATreeWithoutItsEdit(t *testing.T) {
	boot := []string{"release", "disk", "memory", "cpus", "max-memory", "max-cpus", "hotplug-disks", "vsock-cid", "append", "qmp"}
	tree := treeWith(t, map[string][]string{"boot": boot, "memory": {"qmp", "to"}})
	r, err := Experiment{Probe: Probes["bootchart"], Trees: []*Tree{tree}, Reps: 2, Scratch: t.TempDir(), Keep: t.TempDir(), Log: io.Discard}.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range r.Samples {
		if !strings.Contains(s.Missed, "no bootchart edit") {
			t.Errorf("repetition %d: missed %q", s.Rep, s.Missed)
		}
	}
	if d := (bootchart{}).Detail(r); strings.Contains(d, ".svg") {
		t.Errorf("charts named for measurements that drew none:\n%s", d)
	}
}

// A measurement's files are named for the person who downloads them: the chart, its samples beside
// it under the chart's name, the plot, and the dependency view.
func TestBootchartNamesTheFilesItKept(t *testing.T) {
	d := (bootchart{}).Detail(&Result{Samples: []Sample{{Tree: "fork", Rep: 2}}})
	if !strings.Contains(d, "fork-2.bootchart.svg, fork-2.bootchart.json, fork-2.plot.svg, fork-2.chain.txt, fork-2.order.dot, fork-2.order.svg\n") {
		t.Errorf("Detail = %q", d)
	}
}

// The first login is drawn on the plot where it ran: on v20261122.04's plot, whose first login ran
// at 340 ms of the kernel's clock (release run 45041820698), at 2 px a millisecond from the
// kernel's start, across every unit, inside the plot. A plot whose firmware took time of its own
// starts the kernel later, and the login moves with it.
func TestTheFirstLoginIsDrawnOnThePlotWhereItRan(t *testing.T) {
	b, err := os.ReadFile("../../docs/releases/v20261122.04-plot.svg")
	if err != nil {
		t.Fatal(err)
	}
	plot, err := markLogin(string(b), 0.3404)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`<g transform="translate(20.000,100)">`,
		`<line x1="680.800" y1="0" x2="680.800" y2="1280.000"`, `<text x="684.800" y="-20"`, `>first login 340 ms</text>`} {
		if !strings.Contains(plot[len(b)-len("</svg>\n"):], want) {
			t.Errorf("the mark after the plot's units has no %s", want)
		}
	}
	if !strings.HasSuffix(plot, "</svg>\n") || !strings.HasPrefix(plot, string(b[:len(b)-len("</svg>\n")])) {
		t.Errorf("the plot is not the same plot with the mark added before its end")
	}

	firmware := `<g transform="translate(20,100)">
<rect class="box" x="0" y="0" width="900" height="40" />
<line class="sec01" x1="200.000" y1="0" />
<rect class="kernel" x="100.000" y="0.000" />
</svg>`
	plot, err = markLogin(firmware, 0.1)
	if err != nil || !strings.Contains(plot, `<line x1="300.000" y1="0" x2="300.000" y2="40.000"`) {
		t.Errorf("a plot whose kernel starts at 50 ms marked 100 ms of the kernel's clock as %q, %v", plot, err)
	}
	for part, without := range map[string]string{
		"box":    strings.Replace(firmware, `class="box"`, `class="b"`, 1),
		"kernel": strings.Replace(firmware, `class="kernel"`, `class="k"`, 1),
		"tenth":  strings.Replace(firmware, `class="sec01"`, `class="s"`, 1),
		"end":    strings.Replace(firmware, `</svg>`, ``, 1),
	} {
		if _, err := markLogin(without, 0.1); err == nil || !strings.Contains(err.Error(), "not a systemd-analyze plot") {
			t.Errorf("a plot with no %s was marked: %v", part, err)
		}
	}
}

// The ordering graph is asked for among the chain's units and nothing else, each named to
// systemd-analyze as the guest's shell hands it over, an escaped device name as well.
func TestTheOrderingGraphIsAskedForAmongTheChainsUnits(t *testing.T) {
	bin := t.TempDir()
	write(t, filepath.Join(bin, "systemd-analyze"), []byte("#!/bin/sh\nfor a; do echo \"$a\"; done\n"))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	units := []string{"basic.target", `systemd-fsck@dev-disk-by\x2dlabel-root.service`}
	out, err := exec.Command("sh", "-c", orderAmong(units)).Output()
	if err != nil {
		t.Fatal(err)
	}
	want := "dot\n--order\n--from-pattern=basic.target\n--from-pattern=systemd-fsck@dev-disk-by\\x2dlabel-root.service\n" +
		"--to-pattern=basic.target\n--to-pattern=systemd-fsck@dev-disk-by\\x2dlabel-root.service\n"
	if string(out) != want {
		t.Errorf("systemd-analyze was handed\n%s\nwant\n%s", out, want)
	}
}

// The units on a critical chain are read off systemd-analyze's tree, each once, and nothing of
// its header or times is taken for one: v20261122.06's chains to spin's login (lab 45070438471,
// bench, spin's shape, its second boot), the system manager's, before the login user's own.
func TestTheUnitsOnACriticalChainAreReadOffItsTree(t *testing.T) {
	b, err := os.ReadFile("testdata/chain-45070438471.txt")
	if err != nil {
		t.Fatal(err)
	}
	system, _, _ := strings.Cut(string(b), "$ systemd-analyze --user")
	want := []string{"spin-lab-forward.service", "basic.target", "sockets.target", "docker.socket", "sysinit.target",
		"systemd-tmpfiles-setup.service", "systemd-journald.service", "systemd-journald.socket", "-.mount", "-.slice",
		"ssh.service", "spin-machine-sshd-keygen.service", "user@0.service", "user-runtime-dir@0.service",
		"systemd-logind.service", "systemd-logind-varlink.socket"}
	if got := chainUnits(system); !slices.Equal(got, want) {
		t.Errorf("chainUnits =\n%q\nwant\n%q", got, want)
	}
}

// guestAnswering is a login to a guest that prints, for each command, the answer of the first
// prefix in answers that starts it, and fails a command none starts, as systemd-analyze fails on a
// boot that has not finished.
func guestAnswering(asked *[]string, answers map[string]string) func(string) (string, error) {
	return func(command string) (string, error) {
		*asked = append(*asked, command)
		for prefix, answer := range answers {
			if strings.HasPrefix(command, prefix) {
				return answer, nil
			}
		}
		return "", errors.New("Bootup is not yet finished")
	}
}

// dependencyMeasurement is a measurement of repetition 1 on spin's path that keeps its files in a
// directory of its own and its log in log, with dot on the PATH as script, or no dot when script
// is "".
func dependencyMeasurement(t *testing.T, log *strings.Builder, script string) *Measurement {
	t.Helper()
	bin := t.TempDir()
	if script != "" {
		write(t, filepath.Join(bin, "dot"), []byte("#!/bin/sh\n"+script+"\n"))
	}
	t.Setenv("PATH", bin)
	return &Measurement{Tree: treeWith(t, nil), Keep: t.TempDir(), Key: &Key{SpinsPath: true}, log: log}
}

// The dependency view keeps the chains as the guest printed them under the commands that printed
// them, asks for the ordering among the system chain's units, keeps it as dot, and keeps graphviz's
// drawing of exactly that dot: v20261122.06's chains (lab 45070438471).
func TestTheDependencyViewKeepsTheChainsAndTheirOrderingGraph(t *testing.T) {
	b, err := os.ReadFile("testdata/chain-45070438471.txt")
	if err != nil {
		t.Fatal(err)
	}
	system, user, _ := strings.Cut(strings.TrimPrefix(string(b), "$ "), "\n$ systemd-analyze --user critical-chain --no-pager\n")
	_, system, _ = strings.Cut(system, "\n")
	var log strings.Builder
	m := dependencyMeasurement(t, &log, `echo '<svg>'; exec /bin/cat`)
	var asked []string
	order := "digraph systemd {\n\t\"spin-lab-forward.service\"->\"basic.target\" [color=\"green\"];\n}\n"
	if err := keepDependencies(m, guestAnswering(&asked, map[string]string{
		"systemd-analyze critical-chain": system, "systemd-analyze --user critical-chain": user, "systemd-analyze dot --order": order,
	})); err != nil {
		t.Fatal(err)
	}
	chainFile, orderFile, graphFile := DependencyFiles(m.Tree.Name(), 1)
	// The run asked for the forwarder by its name then, spin-lab-forward.service.
	want := strings.Replace(string(b), "spin-lab-forward.service ssh.service", supervisorUnit+" ssh.service", 1)
	if got := read(t, filepath.Join(m.Keep, chainFile)); got != want {
		t.Errorf("the chains kept are not the guest's under their commands:\n%s", got)
	}
	if len(asked) != 3 || !strings.Contains(asked[2], "--from-pattern='user@0.service'") || !strings.Contains(asked[2], "--to-pattern='-.slice'") {
		t.Errorf("the guest was asked %q", asked)
	}
	if got := read(t, filepath.Join(m.Keep, orderFile)); got != order {
		t.Errorf("the ordering kept is %q", got)
	}
	if got := read(t, filepath.Join(m.Keep, graphFile)); got != "<svg>\n"+order {
		t.Errorf("the drawing kept is not dot's of the ordering: %q", got)
	}
	if log.Len() != 0 {
		t.Errorf("a whole view logged %q", log.String())
	}
}

// What the guest or graphviz cannot give is left out and the log says why, and what was given is
// still kept; a view that cannot be written fails the measurement.
func TestTheDependencyViewKeepsWhatItWasGiven(t *testing.T) {
	chain := "ssh.service +16ms\n└─spin-machine-sshd-keygen.service @64ms +37ms\n"
	for _, c := range []struct {
		name, dot string
		answers   map[string]string
		kept      []bool // chain, order, graph
		said      string
	}{
		{"no chain", `exec /bin/cat`, map[string]string{}, []bool{false, false, false}, "no critical chain"},
		{"no user chain", `exec /bin/cat`, map[string]string{"systemd-analyze critical-chain": chain, "systemd-analyze dot": "digraph {}"}, []bool{true, true, true}, "no critical chain of the login user's manager"},
		{"no unit", `exec /bin/cat`, map[string]string{"systemd-analyze critical-chain": "Bootup is not yet finished.\n", "systemd-analyze --user": ""}, []bool{true, false, false}, "names no unit"},
		{"no ordering", `exec /bin/cat`, map[string]string{"systemd-analyze critical-chain": chain, "systemd-analyze --user": ""}, []bool{true, false, false}, "no ordering graph"},
		{"no graphviz", "", map[string]string{"systemd-analyze critical-chain": chain, "systemd-analyze --user": "", "systemd-analyze dot": "digraph {}"}, []bool{true, true, false}, "kept as dot only"},
	} {
		var log strings.Builder
		m := dependencyMeasurement(t, &log, c.dot)
		var asked []string
		if err := keepDependencies(m, guestAnswering(&asked, c.answers)); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		chainFile, orderFile, graphFile := DependencyFiles(m.Tree.Name(), 1)
		for i, f := range []string{chainFile, orderFile, graphFile} {
			if _, err := os.Stat(filepath.Join(m.Keep, f)); (err == nil) != c.kept[i] {
				t.Errorf("%s: %s kept is %v, want %v", c.name, f, err == nil, c.kept[i])
			}
		}
		if !strings.Contains(log.String(), c.said) {
			t.Errorf("%s: the log says %q, not %q", c.name, log.String(), c.said)
		}
	}

	var log strings.Builder
	m := dependencyMeasurement(t, &log, `exec /bin/cat`)
	m.Keep = filepath.Join(m.Keep, "gone")
	var asked []string
	if err := keepDependencies(m, guestAnswering(&asked, map[string]string{"systemd-analyze": chain})); err == nil || !strings.Contains(err.Error(), "keeping the critical chain") {
		t.Errorf("a chain that could not be written: %v", err)
	}
}
