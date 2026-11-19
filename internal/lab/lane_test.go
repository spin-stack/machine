// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestEveryProbeMeasuresATree is the VM lane: every probe once over a tree made from a built
// release (SPIN_LAB_LANE, _output after `task build`), this checkout's spin-machine and the lane
// edit, each measurement whole. It holds what only a booted machine reaches - overlays edited
// through NBD, a machine launched, logged into over vsock, saved, restored, its ring buffer read -
// and needs KVM, /dev/vhost-vsock and sudo.
func TestEveryProbeMeasuresATree(t *testing.T) {
	from := os.Getenv("SPIN_LAB_LANE")
	if from == "" {
		t.Skip("set SPIN_LAB_LANE to a built release (_output): boots VMs, needs KVM, vhost-vsock and sudo")
	}
	if !CanSudo() {
		t.Fatal("the lane edits overlays through NBD, which needs sudo -n")
	}
	tree, err := MakeTree(filepath.Join(t.TempDir(), "lane"), TreeSpec{Name: "lane", From: from, SpinMachine: "here", Edits: []string{"lane"}},
		Sources{Checkout: checkoutRoot(t), Cache: t.TempDir(), Log: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	trees := []*Tree{tree}
	// A systemd experiment's tree too, when one is named: it is built here, which takes minutes.
	if patch := os.Getenv("SPIN_LAB_SYSTEMD"); patch != "" {
		st, err := MakeTree(filepath.Join(t.TempDir(), "systemd"), TreeSpec{Name: "systemd", From: from, Systemd: patch},
			Sources{Checkout: checkoutRoot(t), Cache: t.TempDir(), Log: io.Discard})
		if err != nil {
			t.Fatal(err)
		}
		trees = append(trees, st)
	}
	for _, name := range ProbeNames() {
		t.Run(name, func(t *testing.T) {
			p := Probes[name]
			o := Options{}
			// The fast load where QEMU's user may have userfaultfd; elsewhere the probe says how.
			if b, _ := os.ReadFile("/proc/sys/vm/unprivileged_userfaultfd"); name == "restore-first" && string(b) == "1\n" {
				o["fast"] = "true"
			}
			r, err := Experiment{Probe: p, Trees: trees, Reps: 1, Options: o, Scratch: t.TempDir(), Log: io.Discard}.Run(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if h := r.Host; h.CPU == "" || h.CPUs == 0 || h.Kernel == "" || h.Load == "" || h.ScratchDevice == "" {
				t.Errorf("the host is not all there: %+v", h)
			}
			for _, s := range r.Samples {
				if s.Missed != "" {
					t.Errorf("%s: lost: %s", s.Row, s.Missed)
				}
				if len(s.Values) == 0 {
					t.Errorf("%s: recorded nothing", s.Row)
				}
			}
			t.Log(r.Table())
		})
	}
}

// TestTheLabTalksToGitHub is the lane that reaches GitHub (SPIN_LAB_GITHUB=1, gh logged in): the
// newest release, lab.yml's runs, a completed run waited for, a lab run's result downloaded
// (SPIN_LAB_RUN, a lab.yml run that kept one).
func TestTheLabTalksToGitHub(t *testing.T) {
	if os.Getenv("SPIN_LAB_GITHUB") == "" {
		t.Skip("set SPIN_LAB_GITHUB=1: asks GitHub through gh")
	}
	gh := &GitHub{Repo: "spin-stack/spin-machine"}
	tag, err := gh.LatestRelease()
	if err != nil || tag == "" {
		t.Fatalf("LatestRelease = %q, %v", tag, err)
	}
	if tags, err := gh.Releases(3); err != nil || len(tags) == 0 || tags[0] != tag {
		t.Fatalf("Releases(3) = %v, %v; want the newest, %s, first", tags, err, tag)
	}
	runs, err := gh.LabRuns()
	if err != nil || len(runs) == 0 {
		t.Fatalf("LabRuns = %d runs, %v", len(runs), err)
	}
	dir := t.TempDir()
	if err := gh.DownloadRelease(tag, "machine.env", dir); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "machine.env")); err != nil || len(b) == 0 {
		t.Fatalf("release %s's machine.env: %d bytes, %v", tag, len(b), err)
	}
	src := Sources{Checkout: checkoutRoot(t), Cache: t.TempDir(), GitHub: gh}
	// The release itself, unpacked: 385 MB, so only when asked for.
	if os.Getenv("SPIN_LAB_RELEASE") != "" {
		d, source, err := src.from("release")
		if err != nil || source != "release "+tag || !fileExists(filepath.Join(d, partFiles["qemu"])) {
			t.Fatalf("from(release) = %s, %q, %v", d, source, err)
		}
		// Its spin-machine, built from the commit its machine.env names.
		env, err := readEnv(filepath.Join(d, "machine.env"))
		if err != nil {
			t.Fatal(err)
		}
		to := filepath.Join(t.TempDir(), "spin-machine")
		if source, err := src.releaseSpinMachine(d, to); err != nil || source != "built from "+env["source_commit"]+", "+tag+"'s" || !fileExists(to) {
			t.Fatalf("releaseSpinMachine = %q, %v", source, err)
		}
	}
	if id := os.Getenv("SPIN_LAB_RUN"); id != "" {
		d, err := src.run(id, "lab-*")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := one(d, "result.json"); err != nil {
			t.Fatal(err)
		}
		n, _ := strconv.ParseInt(id, 10, 64)
		r, err := gh.Wait(t.Context(), n)
		if err != nil || r.Status != "completed" {
			t.Fatalf("Wait(%d) = %+v, %v", n, r, err)
		}
		dir := t.TempDir()
		if err := gh.Artifact(id, "lab-"+id, dir); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadResult(filepath.Join(dir, "result.json")); err != nil {
			t.Fatal(err)
		}
	}
}

// TestTheScratchOnABenchRunnerIsItsInstanceStore is the bench runner's lane (SPIN_LAB_RUNNER=1 on
// one): the scratch is on the instance store, not the root volume. It may format an instance
// store nothing has mounted, which is why it only runs where asked.
func TestTheScratchOnABenchRunnerIsItsInstanceStore(t *testing.T) {
	if os.Getenv("SPIN_LAB_RUNNER") == "" {
		t.Skip("set SPIN_LAB_RUNNER=1 on a bench runner: may format its instance store")
	}
	d, err := runnerScratch(t.TempDir())
	if err != nil || !strings.HasSuffix(d, "/spin-lab-tmp") {
		t.Errorf("runnerScratch = %q, %v", d, err)
	}
}

// checkoutRoot is this repository's root, two directories up from the package.
func checkoutRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}
