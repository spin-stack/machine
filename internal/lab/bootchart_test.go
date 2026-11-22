// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
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
// it under the chart's name, and the plot.
func TestBootchartNamesTheFilesItKept(t *testing.T) {
	d := (bootchart{}).Detail(&Result{Samples: []Sample{{Tree: "fork", Rep: 2}}})
	if !strings.Contains(d, "fork-2.bootchart.svg, fork-2.bootchart.json, fork-2.plot.svg") {
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
