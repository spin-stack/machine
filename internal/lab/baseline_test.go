// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
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

// fakeMonitor serves QMP on a socket under dir as a held QEMU on a nested Xeon would answer the
// questions Models asks: which named models run here, and each one's features.
func fakeMonitor(t *testing.T, dir string) string {
	t.Helper()
	sock := filepath.Join(dir, "q")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
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
				Execute   string         `json:"execute"`
				Arguments map[string]any `json:"arguments"`
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
			_ = w.Encode(map[string]any{"return": ret})
		}
	}()
	return sock
}

// The baseline is the newest model QEMU says this host runs with nothing missing - not one the
// host's CPU name suggests and the nested host refuses.
func TestTheBaselineIsTheNewestModelThisHostRuns(t *testing.T) {
	dir, err := shortDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	got, err := baselineOn(fakeMonitor(t, dir))
	if err != nil || got != "Cascadelake-Server-v5" {
		t.Errorf("baselineOn = %q, %v; want Cascadelake-Server-v5", got, err)
	}
	if _, err := baselineOn(filepath.Join(dir, "nothing")); err == nil {
		t.Error("a monitor that is not there gave a model")
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
