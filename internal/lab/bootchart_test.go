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

// checkoutPinning is a checkout whose versions.yaml pins systemd-bootchart to body, served by a
// server of the test's own.
func checkoutPinning(t *testing.T, body string, pin string) string {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
	t.Cleanup(srv.Close)
	// The server's certificate trusted, as the archive's is.
	saved := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = saved })
	checkout := t.TempDir()
	write(t, filepath.Join(checkout, "versions.yaml"), []byte(`- name: systemd-bootchart
  kind: download
  source: `+srv.URL+`/systemd-bootchart_{version}_amd64.deb
  version: 235-2build1
  pin: `+pin+`
  track: page https://packages.ubuntu.com/resolute/amd64/systemd-bootchart/download
`))
	return checkout
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// The bootchart edit installs the package its checkout pins, fetched into the tree, and only that:
// a download that is not the pin's makes no edit and leaves nothing in the tree.
func TestTheBootchartEditInstallsThePinnedPackage(t *testing.T) {
	dir := t.TempDir()
	e, err := namedEdit("bootchart", checkoutPinning(t, "the package", sha("the package")), dir)
	if err != nil {
		t.Fatal(err)
	}
	deb := filepath.Join(dir, "bootchart", "systemd-bootchart_235-2build1_amd64.deb")
	if b, err := os.ReadFile(deb); err != nil || string(b) != "the package" {
		t.Errorf("the tree's package = %q, %v", b, err)
	}
	if !strings.Contains(e.Setup, sha("the package")+"  "+deb) || !strings.Contains(e.Setup, `ln -sfn /usr/lib/systemd/systemd-bootchart "$MNT/usr/sbin/init"`) {
		t.Errorf("the edit neither checks the package nor boots it:\n%s", e.Setup)
	}
	if !strings.Contains(e.Files["/etc/systemd/bootchart.conf"], "Frequency=200") {
		t.Errorf("bootchart.conf = %q", e.Files["/etc/systemd/bootchart.conf"])
	}

	dir = t.TempDir()
	if _, err := namedEdit("bootchart", checkoutPinning(t, "another package", sha("the package")), dir); err == nil ||
		!strings.Contains(err.Error(), "versions.yaml pins "+sha("the package")) {
		t.Errorf("a package that is not the pin's: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "bootchart", "systemd-bootchart_235-2build1_amd64.deb")); err == nil {
		t.Error("a package that is not the pin's was left in the tree")
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
