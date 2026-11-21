// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// --cpu is baseline or nothing: a model named elsewhere is --boot --cpu=NAME, said so.
func TestATreesCPUIsBaselineOrNothing(t *testing.T) {
	base := fakeRelease(t, "k1")
	_, err := MakeTree(filepath.Join(t.TempDir(), "t"), TreeSpec{Name: "t", From: base, CPU: "GraniteRapids-v1"}, src(t))
	if err == nil || !strings.Contains(err.Error(), "--boot --cpu=NAME") {
		t.Errorf("a tree asking --cpu GraniteRapids-v1: %v, want refused, pointing at --boot", err)
	}
}

// serveQMP answers on l as a held QEMU on a nested Xeon would answer the questions CPUModels asks:
// which named models run here, and each one's features.
func serveQMP(l net.Listener) {
	c, err := l.Accept()
	if err != nil {
		return
	}
	defer func() { _ = c.Close() }()
	w := json.NewEncoder(c)
	_ = w.Encode(map[string]any{"QMP": map[string]any{"version": map[string]any{}, "capabilities": []any{}}})
	r := bufio.NewScanner(c)
	for r.Scan() {
		var req struct {
			Execute   string          `json:"execute"`
			Arguments map[string]any  `json:"arguments"`
			ID        json.RawMessage `json:"id"`
		}
		_ = json.Unmarshal(r.Bytes(), &req)
		var ret any = map[string]any{}
		switch req.Execute {
		case "query-cpu-definitions":
			ret = []map[string]any{
				// Refused here: the nested host hides features it names.
				{"name": "GraniteRapids-v1", "unavailable-features": []string{"la57", "rtm"}, "migration-safe": true},
				{"name": "Cascadelake-Server-v5", "unavailable-features": []string{}, "migration-safe": true},
				{"name": "Skylake-Server-v4", "unavailable-features": []string{}, "migration-safe": true},
				{"name": "host", "unavailable-features": []string{}, "migration-safe": false},
			}
		case "query-cpu-model-expansion":
			props := map[string]any{"sse2": true, "avx2": true}
			if req.Arguments["model"].(map[string]any)["name"] == "Cascadelake-Server-v5" {
				props["avx512vnni"] = true // a later generation: its predecessor's features and more
			}
			ret = map[string]any{"model": map[string]any{"name": "x", "props": props}}
		}
		_ = w.Encode(map[string]any{"return": ret, "id": req.ID})
	}
}

// The baseline is the newest model QEMU says this host runs with nothing missing - not one the
// host's CPU name suggests and the nested host refuses.
func TestTheBaselineIsTheNewestModelThisHostRuns(t *testing.T) {
	dir, err := shortDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "q")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go serveQMP(l)
	got, err := baselineOn(sock)
	if err != nil || got != "Cascadelake-Server-v5" {
		t.Errorf("baselineOn = %q, %v; want Cascadelake-Server-v5", got, err)
	}
	if _, err := baselineOn(filepath.Join(dir, "nothing")); err == nil {
		t.Error("a monitor that is not there gave a model")
	}
}

// TestHelperSpinMachine is not a test: it is the spin-machine of a tree a test makes, run as this
// binary. `boot --held --qmp PATH` serves QMP there as serveQMP does, with a child of its own beside
// it whose pid it writes to $LAB_FAKE_CHILD - to show that the whole process group goes with it.
func TestHelperSpinMachine(t *testing.T) {
	if os.Getenv("LAB_FAKE_SPIN_MACHINE") != "1" {
		t.Skip("run as a tree's spin-machine")
	}
	args := os.Args[slices.Index(os.Args, "--")+1:]
	qmp := args[slices.Index(args, "--qmp")+1]
	child := exec.Command("sleep", "60")
	if err := child.Start(); err != nil {
		os.Exit(2)
	}
	_ = os.WriteFile(os.Getenv("LAB_FAKE_CHILD"), []byte(strconv.Itoa(child.Process.Pid)), 0o644)
	l, err := net.Listen("unix", qmp)
	if err != nil {
		os.Exit(2)
	}
	serveQMP(l)
	os.Exit(0)
}

// helperSpinMachine makes path a spin-machine that runs TestHelperSpinMachine.
func helperSpinMachine(t *testing.T, path, childPID string) {
	t.Helper()
	script := "#!/bin/sh\nLAB_FAKE_SPIN_MACHINE=1 LAB_FAKE_CHILD=" + childPID + " exec " + os.Args[0] +
		" -test.run='^TestHelperSpinMachine$' -- \"$@\"\n"
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// A tree asked for --cpu baseline holds its own spin-machine's QEMU, takes this host's newest model
// from it, boots as that model and says so; and what the held machine started is all gone after.
func TestATreeAskedForTheBaselineBootsTheModelThisHostChose(t *testing.T) {
	base := fakeRelease(t, "k1")
	child := filepath.Join(t.TempDir(), "child")
	helperSpinMachine(t, filepath.Join(base, "bin", "spin-machine"), child)
	tr, err := MakeTree(filepath.Join(t.TempDir(), "t"), TreeSpec{Name: "t", From: base, CPU: "baseline", Boot: []string{"--max-cpus=4"}}, src(t))
	if err != nil {
		t.Fatal(err)
	}
	if tr.Manifest.CPU != "Cascadelake-Server-v5" || !slices.Equal(tr.Manifest.Boot, []string{"--max-cpus=4", "--cpu=Cascadelake-Server-v5"}) {
		t.Errorf("the tree's CPU %q and boot flags %v", tr.Manifest.CPU, tr.Manifest.Boot)
	}
	b, err := os.ReadFile(child)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(string(b))
	// Killed with its group, and reaped by init once its parent is gone: signal 0 finds no process.
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("the held machine's child %d outlived it", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// fakeSpinMachine is a tree whose spin-machine runs script, for the paths where QEMU never comes up.
func fakeSpinMachine(t *testing.T, script string) *Tree {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin", "spin-machine"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &Tree{Dir: dir, Manifest: Manifest{Name: "t"}}
}

const refusal = `echo "qemu-system-x86_64: Host doesn't support requested features" >&2; exit 1`

// A held machine that exits before it opens its monitor says why at once, not after 30 s.
func TestABaselineWhoseMachineExitsSaysWhyAtOnce(t *testing.T) {
	tr := fakeSpinMachine(t, refusal)
	t0 := time.Now()
	_, err := hostBaseline(tr.Dir)
	if err == nil || !strings.Contains(err.Error(), "Host doesn't support requested features") {
		t.Errorf("hostBaseline of a machine QEMU refused = %v, want QEMU's words", err)
	}
	if took := time.Since(t0); took > 10*time.Second {
		t.Errorf("hostBaseline took %v to say a machine exited", took)
	}
}

// A machine QEMU refuses is known as refused at its first boot: the login gives up at once and
// refuses the tree, instead of waiting out 60 s for sshd.
func TestALoginToAMachineThatExitedGivesUpAtOnce(t *testing.T) {
	m := &Measurement{Tree: fakeSpinMachine(t, refusal), Key: &Key{}}
	mc, err := m.Launch(nil, "boot")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for i := len(m.cleanup) - 1; i >= 0; i-- {
			m.cleanup[i]()
		}
	})
	if mc.PID() <= 0 {
		t.Errorf("a launched machine's PID is %d", mc.PID())
	}
	t0 := time.Now()
	_, err = m.Key.LoggedIn(mc, NewCID(), t0)
	var refused *RefusedError
	if !errors.As(err, &refused) || !strings.Contains(refused.Error(), "Host doesn't support requested features") {
		t.Errorf("LoggedIn to a machine QEMU refused = %v, want the tree refused", err)
	}
	if took := time.Since(t0); took > 10*time.Second {
		t.Errorf("LoggedIn took %v to give up on a machine that had exited", took)
	}
	if err := m.SpinMachine("save"); err == nil {
		t.Error("a spin-machine that exits 1 ran without an error")
	}
}

// What a failing program says last is its last line, whatever is around it.
func TestTheLastLineIsWhatAProgramSaidLast(t *testing.T) {
	for in, want := range map[string]string{
		"one":                "one",
		"warning\nrefused":   "refused",
		"\nrefused":          "refused",
		"a\nb\nc":            "c",
		"warning: x\nerr: y": "err: y",
	} {
		if got := lastLine(in); got != want {
			t.Errorf("lastLine(%q) = %q, want %q", in, got, want)
		}
	}
}
