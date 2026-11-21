// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// restorer is a probe that says it restores (Needs restore) and boots nothing: it keeps the
// record each measurement was handed, by tree.
type restorer struct {
	*recorder
	handed []string
}

func (p *restorer) Needs(Options) []Need { return []Need{{"restore", "disk"}} }

func (p *restorer) Measure(ctx context.Context, m *Measurement) error {
	record := "none"
	if m.Target != "" {
		b, err := os.ReadFile(m.Target)
		if err != nil {
			return err
		}
		record = strings.TrimSpace(string(b))
	}
	p.handed = append(p.handed, m.Tree.Name()+": "+record)
	return p.recorder.Measure(ctx, m)
}

// recordTree is a tree named name whose spin-machine writes each command line it runs to asked,
// takes restore --disk, and with target also restore --target and target, which prints a record
// once the machine's monitor is there. Its boot runs boot.
func recordTree(t *testing.T, name string, target bool, boot string) (tree *Tree, asked string) {
	t.Helper()
	asked = filepath.Join(t.TempDir(), "asked")
	restore, record := "echo 'Usage of restore:'; echo '  -disk string'", "exit 1"
	if target {
		restore += "; echo '  -target string'"
		record = "echo 'Usage of target:'; echo '  -qmp string'"
	}
	tree = fakeSpinMachine(t, `echo "$@" >> `+asked+`
case "$1 $2" in
"restore -h") `+restore+` ;;
"target -h") `+record+` ;;
"target --qmp") [ -S "$3" ] && echo '{"vmm": "qemu 11.2.0"}' ;;
boot*) `+boot+` ;;
esac`)
	tree.Manifest.Name = name
	return tree, asked
}

// held is a boot that holds a machine: TestHelperSpinMachine serving the monitor on --qmp.
func held(t *testing.T) string {
	return "LAB_FAKE_SPIN_MACHINE=1 LAB_FAKE_CHILD=" + filepath.Join(t.TempDir(), "child") + " exec " + os.Args[0] +
		` -test.run='^TestHelperSpinMachine$' -- "$@"`
}

// lines are the command lines of command that asked records, its usage asked for left out.
func lines(t *testing.T, asked, command string) []string {
	t.Helper()
	b, _ := os.ReadFile(asked)
	var out []string
	for l := range strings.Lines(string(b)) {
		if strings.HasPrefix(l, command+" ") && strings.TrimSpace(l) != command+" -h" {
			out = append(out, strings.TrimSpace(l))
		}
	}
	return out
}

// A probe that restores is handed each tree's record, read once before anything is measured from
// a machine of the tree held for a checkpoint in the experiment's shape, as spin reads its host's
// once; a tree whose spin-machine cannot print one or take one boots nothing for it and restores
// as before; and a probe that does not restore reads no record.
func TestEveryRestoreIsHandedItsTreesRecordReadOnce(t *testing.T) {
	now, nowAsked := recordTree(t, "now", true, held(t))
	before, beforeAsked := recordTree(t, "before", false, held(t))
	p := &restorer{recorder: &recorder{phases: 2, rows: []string{"x"}}}
	var log strings.Builder
	if _, err := (Experiment{Probe: p, Trees: []*Tree{now, before}, Reps: 2, Scratch: t.TempDir(), Log: &log}).Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, said := range []string{"now, before measuring, repetition 1: the machine's record read, and handed to every restore\n",
		"before, before measuring, repetition 1: its spin-machine cannot print the machine's record or take one: each restore asks the machine it restores into\n"} {
		if !strings.Contains(log.String(), said) {
			t.Errorf("the log does not say %q:\n%s", said, log.String())
		}
	}
	want := slices.Repeat([]string{`now: {"vmm": "qemu 11.2.0"}`, "before: none"}, 4)
	if !slices.Equal(p.handed, want) {
		t.Errorf("handed %v, want %v", p.handed, want)
	}
	boots, targets := lines(t, nowAsked, "boot"), lines(t, nowAsked, "target")
	if len(boots) != 1 || !strings.Contains(boots[0], "--restore") || !strings.Contains(boots[0], "--memory 128 --cpus 1 --max-memory 8192 --max-cpus 16 --hotplug-disks 16") {
		t.Errorf("booted %q, want one machine held for a checkpoint, in spin's shape", boots)
	}
	if len(targets) != 1 || !strings.Contains(boots[0], strings.TrimPrefix(targets[0], "target ")) {
		t.Errorf("asked %q for the record, want once, of the machine it held: %q", targets, boots)
	}
	if b := lines(t, beforeAsked, "boot"); len(b) != 0 {
		t.Errorf("a tree that cannot take a record booted %q for one", b)
	}

	plain, plainAsked := recordTree(t, "plain", true, held(t))
	r := &recorder{phases: 1, rows: []string{"x"}}
	if _, err := (Experiment{Probe: r, Trees: []*Tree{plain}, Reps: 1, Scratch: t.TempDir(), Log: io.Discard}).Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if b := lines(t, plainAsked, "boot"); len(b) != 0 {
		t.Errorf("a probe that restores nothing booted %q for a record", b)
	}
}

// A tree whose machine QEMU refuses here is refused when its record is read, at once: its
// measurements are lost with the reason and the other trees go on. A record that cannot be read
// otherwise stops the experiment, saying why.
func TestATreeRefusedWhenItsRecordIsReadIsRefusedAtOnce(t *testing.T) {
	refused, _ := recordTree(t, "refused", true, refusal)
	now, _ := recordTree(t, "now", true, held(t))
	p := &restorer{recorder: &recorder{phases: 1, rows: []string{"x"}}}
	t0 := time.Now()
	r, err := Experiment{Probe: p, Trees: []*Tree{refused, now}, Reps: 2, Scratch: t.TempDir(), Log: io.Discard}.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(t0); took > 10*time.Second {
		t.Errorf("took %v to refuse a machine that exited", took)
	}
	for _, s := range r.Samples {
		if lost := strings.HasPrefix(s.Missed, "refused: QEMU will not make this tree's machine on this host"); lost != (s.Tree == "refused") {
			t.Errorf("sample %s rep %d missed %q", s.Tree, s.Rep, s.Missed)
		}
	}
	if want := []string{`now: {"vmm": "qemu 11.2.0"}`, `now: {"vmm": "qemu 11.2.0"}`}; !slices.Equal(p.handed, want) {
		t.Errorf("handed %v, want %v", p.handed, want)
	}

	silent, _ := recordTree(t, "silent", true, held(t))
	if err := os.WriteFile(silent.SpinMachine(), []byte(strings.Replace(mustRead(t, silent.SpinMachine()),
		`[ -S "$3" ] && echo`, `echo "no monitor" >&2; exit 1; echo`, 1)), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = Experiment{Probe: p, Trees: []*Tree{silent}, Reps: 1, Scratch: t.TempDir(), Log: io.Discard}.Run(t.Context())
	if err == nil || !strings.Contains(err.Error(), "reading tree silent's record: spin-machine target") || !strings.Contains(err.Error(), "no monitor") {
		t.Errorf("a record target could not print: %v", err)
	}
}

// A restore names the tree's record when the experiment read one, and asks the machine otherwise.
func TestARestoreNamesTheRecordItIsHanded(t *testing.T) {
	c := &checkpoint{overlay: "/m/overlay.qcow2", state: "/m/state"}
	m := &Measurement{Tree: &Tree{Dir: "/tree"}}
	base := []string{"restore", "--release", "/tree", "--qmp", "/s/q", "--from", "/m/state", "--disk", "/m/overlay.qcow2"}
	if got, want := c.restoreArgs(m, "/s/q", "--channels", "8"), append(slices.Clone(base), "--channels", "8"); !slices.Equal(got, want) {
		t.Errorf("with no record: %q, want %q", got, want)
	}
	m.Target = "/m/record.json"
	if got, want := c.restoreArgs(m, "/s/q"), append(slices.Clone(base), "--target", "/m/record.json"); !slices.Equal(got, want) {
		t.Errorf("with a record: %q, want %q", got, want)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
