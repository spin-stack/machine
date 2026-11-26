// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/spin-stack/machine/qmp"
)

// fakeMachine is what a QEMU answers about itself, path by path: a small machine with a device of
// each kind the record reads or leaves out.
var fakeMachine = map[string]string{
	"/machine": `[{"name": "type", "type": "string", "value": "pc-q35-11.2-machine"},
		{"name": "smp", "type": "SMPConfiguration", "value": {"cpus": 1, "maxcpus": 16}},
		{"name": "memory", "type": "MemorySizeConfiguration", "value": {"size": 134217728}},
		{"name": "hpet", "type": "bool", "value": false}, {"name": "acpi", "type": "OnOffAuto", "value": "on"},
		{"name": "sata", "type": "bool", "value": false}, {"name": "smbus", "type": "bool", "value": false},
		{"name": "peripheral", "type": "child<container>", "value": "/machine/peripheral"}]`,
	// qom-list answers in no order of its own.
	"/machine/q35/pcie.0": `[{"name": "child[2]", "type": "link<virtio-blk-pci>", "value": "/machine/peripheral/disk0"},
		{"name": "child[0]", "type": "link<mch>", "value": "/machine/q35/mch"},
		{"name": "hotplug-handler", "type": "link<hotplug-handler>", "value": "/machine/q35"},
		{"name": "child[1]", "type": "link<virtio-scsi-pci>", "value": "/machine/peripheral/scsi0"},
		{"name": "child[3]", "type": "link<virtio-balloon-pci>", "value": "/machine/peripheral-anon/device[1]"}]`,
	"isa.0": `[{"name": "child[10]", "type": "link<i8257>", "value": "/lpc/dma[1]"},
		{"name": "child[2]", "type": "link<i8257>", "value": "/lpc/dma[0]"},
		{"name": "child[3]", "type": "link<isa-serial>", "value": "/serial"}]`,
	"/machine/peripheral": `[{"name": "type", "type": "string", "value": "container"},
		{"name": "vcpu1", "type": "child<host-x86_64-cpu>", "value": "/machine/peripheral/vcpu1"},
		{"name": "scsi0", "type": "child<virtio-scsi-pci>", "value": "/machine/peripheral/scsi0"},
		{"name": "disk0", "type": "child<virtio-blk-pci>", "value": "/machine/peripheral/disk0"}]`,
	"/machine/peripheral-anon": `[{"name": "device[1]", "type": "child<virtio-balloon-pci>", "value": "/machine/peripheral-anon/device[1]"},
		{"name": "device[0]", "type": "child<vmgenid>", "value": "/machine/peripheral-anon/device[0]"}]`,

	"/machine/q35/mch": `[{"name": "type", "type": "string", "value": "mch"}, {"name": "addr", "type": "str", "value": 0},
		{"name": "hotplugged", "type": "bool", "value": false}, {"name": "unreadable", "type": "str"}]`,
	"/machine/peripheral/scsi0": `[{"name": "type", "type": "string", "value": "virtio-scsi-pci"}, {"name": "addr", "type": "str", "value": 208},
		{"name": "max_target", "type": "uint32", "value": 15}, {"name": "parent_bus", "type": "link<bus>", "value": "/machine/q35/pcie.0"},
		{"name": "iothread-vq-mapping", "type": "list", "value": [{"iothread": "a", "vqs": [0, 1]}]}]`,
	"/machine/peripheral/disk0": `[{"name": "type", "type": "string", "value": "virtio-blk-pci"}, {"name": "addr", "type": "str", "value": 40}]`,
	"/machine/peripheral/vcpu1": `[{"name": "type", "type": "string", "value": "host-x86_64-cpu"}, {"name": "hotplugged", "type": "bool", "value": true}]`,
	"/machine/peripheral-anon/device[0]": `[{"name": "type", "type": "string", "value": "vmgenid"},
		{"name": "guid", "type": "str", "value": "44e7ea8c-e2ea-46b4-a3e2-edcc112e8753"}]`,
	// At function 1 of its slot (33 is 04.1), so that the function is read, not taken to be 0.
	"/machine/peripheral-anon/device[1]": `[{"name": "type", "type": "string", "value": "virtio-balloon-pci"}, {"name": "addr", "type": "str", "value": 33},
		{"name": "deflate-on-oom", "type": "bool", "value": true}, {"name": "guest-stats-polling-interval", "type": "int", "value": 2},
		{"name": "max-bytes", "type": "uint64", "value": 9223372036854775807}, {"name": "id", "type": "str", "value": "x"}]`,
	"/lpc/dma[0]": `[{"name": "type", "type": "string", "value": "i8257"}, {"name": "base", "type": "int32", "value": 0}]`,
	"/lpc/dma[1]": `[{"name": "type", "type": "string", "value": "i8257"}, {"name": "base", "type": "int32", "value": 192}]`,
	"/serial": `[{"name": "type", "type": "string", "value": "isa-serial"}, {"name": "chardev", "type": "str", "value": "serial0"},
		{"name": "iobase", "type": "uint32", "value": 1016}]`,

	"/machine/unattached/device[0]": `[{"name": "type", "type": "string", "value": "host-x86_64-cpu"},
		{"name": "vendor", "type": "string", "value": "GenuineIntel"}, {"name": "family", "type": "uint64", "value": 6},
		{"name": "sse2", "type": "bool", "value": true}, {"name": "avx512f", "type": "bool", "value": true},
		{"name": "vmx", "type": "bool", "value": false}, {"name": "sse4-1", "type": "bool", "value": true},
		{"name": "migratable", "type": "bool", "value": true}, {"name": "x-consistent-cache", "type": "bool", "value": false},
		{"name": "x-legacy-cpuid2-cache", "type": "bool", "value": false},
		{"name": "l3-cache", "type": "bool", "value": true}, {"name": "phys-bits", "type": "uint32", "value": 46},
		{"name": "model-id", "type": "string", "value": "Intel(R) Xeon(R) Platinum 8375C CPU @ 2.90GHz"}]`,
}

// fakeRAMBlocks is x-query-ramblock's answer for fakeMachine.
const fakeRAMBlocks = `              Block Name    PSize              Offset               Used              Total                HVA  RO
                  pc.ram    4 KiB  0x0000000000000000 0x0000000008000000 0x0000000008000000 0x0000757daf000000  rw
    /rom@etc/acpi/tables    4 KiB  0x0000000200080000 0x0000000000020000 0x0000000000200000 0x0000757dad200000  ro
                 pc.bios    4 KiB  0x0000000200000000 0x0000000000010000 0x0000000000010000 0x0000757daee00000  ro
`

// recordAnswers answers what Target asks of fakeMachine, or nothing for any other command.
func recordAnswers(c call) (string, bool) {
	switch c.Command {
	case "query-cpus-fast":
		return ret(`[{"qom-path": "/machine/unattached/device[0]"}, {"qom-path": "/machine/peripheral/vcpu1"}]`), true
	case "query-cpu-model-expansion":
		// The flags of the model, whatever this vCPU has of them.
		return ret(`{"model": {"name": "base", "props": {"vendor": "GenuineIntel", "sse2": true, "avx512f": false, "vmx": true, "family": 6}}}`), true
	case "query-accelerators":
		return ret(`{"enabled": "kvm", "present": ["kvm", "tcg"]}`), true
	case "x-query-ramblock":
		b, _ := json.Marshal(map[string]string{"human-readable-text": fakeRAMBlocks})
		return ret(string(b)), true
	case "qom-list-get":
		return path("", "")(c)
	}
	return "", false
}

// answering is a fake that answers a record read and ok to anything else.
func answering(c call) string {
	if r, ok := recordAnswers(c); ok {
		return r
	}
	return ok
}

// fakeTarget is what a QEMU answering recordAnswers says it is.
func fakeTarget(t *testing.T) Target {
	t.Helper()
	tg, err := (&fakeQEMU{answer: answering}).attach(t).Target(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	return tg
}

// What Target reads of a machine: each device it was started with by its slot or its type, with
// every property but what is one VM's, what changes while it runs, its links and its children;
// none of what was hot-plugged and no disk's place; the machine's own options; every RAM block
// but fw_cfg's; the features its vCPU has on, by their canonical names; and its properties that
// shape CPUID, but none of its other switches.
func TestTargetIsWhatTheMachineRuns(t *testing.T) {
	f := &fakeQEMU{answer: answering}
	m := f.attach(t)
	got, err := m.Target(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	wantExact := []part{
		{"accel", "kvm"},
		{"device 00.0", "mch"},
		{"device 00.0 addr", "0"},
		{"device 00.0 hotplugged", "false"},
		{"device 04.1", "virtio-balloon-pci"},
		{"device 04.1 addr", "33"},
		{"device 04.1 deflate-on-oom", "true"},
		{"device 04.1 max-bytes", "9223372036854775807"},
		{"device 1a.0", "virtio-scsi-pci"},
		{"device 1a.0 addr", "208"},
		{"device 1a.0 iothread-vq-mapping", `[{"iothread":"a","vqs":[0,1]}]`},
		{"device i8257/1", "i8257"},
		{"device i8257/1 base", "0"},
		{"device i8257/2", "i8257"},
		{"device i8257/2 base", "192"},
		{"device isa-serial", "isa-serial"},
		{"device isa-serial iobase", "1016"},
		{"device vmgenid", "vmgenid"},
		{"machine acpi", "on"},
		{"machine hpet", "false"},
		{"machine memory.size", "134217728"},
		{"machine sata", "false"},
		{"machine smbus", "false"},
		{"machine smp.cpus", "1"},
		{"machine smp.maxcpus", "16"},
		{"machine type", "pc-q35-11.2"},
		{"ram pc.bios", "65536"},
		{"ram pc.ram", "134217728"},
	}
	want := Target{
		VMM:     "qemu 11.2.0",
		Formats: []string{FormatMappedRAM, FormatStream},
		CPU: CPU{Vendor: "GenuineIntel", Model: "host", Features: []string{"avx512f", "sse2"}, Properties: map[string]string{
			"family": "6", "l3-cache": "true", "model-id": "Intel(R) Xeon(R) Platinum 8375C CPU @ 2.90GHz", "phys-bits": "46",
			"x-consistent-cache": "false", "x-legacy-cpuid2-cache": "false",
		}},
		Hardware: Hardware{exact: wantExact, atLeast: []count{{"device 1a.0 max_target", 15}}},
	}
	if !reflect.DeepEqual(got, want) {
		g, _ := json.MarshalIndent(got, "", " ")
		w, _ := json.MarshalIndent(want, "", " ")
		t.Errorf("Target\n%s\nwant\n%s", g, w)
	}
	if got := f.sent("query-cpu-model-expansion")[0]; !reflect.DeepEqual(got, map[string]any{"type": "static", "model": map[string]any{"name": "host"}}) {
		t.Errorf("expanded %v, want the model's static expansion, which names each feature once", got)
	}
	// Asked once: nothing it reads changes while the process lives.
	asked := len(f.recorded())
	if again, err := m.Target(ctx(t)); err != nil || !reflect.DeepEqual(again, got) || len(f.recorded()) != asked {
		t.Errorf("a second Target asked QEMU %d more times (%v)", len(f.recorded())-asked, err)
	}
}

// A bus's children are in the order QEMU put them on it, by the number in their names, which
// names "device i8257/1" and "device i8257/2": a name with no number goes before the first one,
// and one whose number is not one is a name like any other.
func TestChildrenAreInTheirBussOrder(t *testing.T) {
	order := []string{"child", "child[1]", "child[2]", "child[10]", "child[x]", "rtc"}
	for i := range len(order) - 1 {
		if a, b := order[i], order[i+1]; compareChildren(a, b) >= 0 || compareChildren(b, a) <= 0 {
			t.Errorf("%s and %s compare %d and %d, want %s first", a, b, compareChildren(a, b), compareChildren(b, a), a)
		}
	}
}

// What Target refuses to make a record of: a machine it cannot read whole is not a machine whose
// checkpoints it can vouch for.
func TestTargetRefusesWhatItCannotRead(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer func(c call) (string, bool)
		want   string
	}{
		{"no accelerator", fails("query-accelerators"), "asking the machine's accelerator"},
		{"no vCPU to read", failsReading("/machine/unattached/device[0]"), "reading the machine's vCPU"},
		{"no list of devices", failsReading("/machine"), "listing the machine's devices"},
		{"no device to read", failsReading("/machine/q35/mch"), "reading the machine's devices"},
		{"no RAM blocks", fails("x-query-ramblock"), "listing the machine's RAM blocks"},
		{"a machine that is not a machine type", path("/machine", `[{"name": "type", "type": "string", "value": "none"}]`), `"none", not a machine type`},
		{"an option it cannot read back", path("/machine", `[{"name": "type", "type": "string", "value": "pc-q35-11.2-machine"}]`), "no smp to read back"},
		{"a device on the root complex with no slot", path("/machine/q35/mch", `[{"name": "type", "type": "string", "value": "mch"}]`), "has no slot"},
		{"a count that is not one", path("/machine/peripheral/scsi0", `[{"name": "type", "type": "string", "value": "virtio-scsi-pci"},
			{"name": "addr", "type": "str", "value": 208}, {"name": "max_target", "type": "str", "value": "many"}]`), "not a count"},
		{"two devices in one slot", path("/machine/peripheral/scsi0", `[{"name": "type", "type": "string", "value": "virtio-scsi-pci"},
			{"name": "addr", "type": "str", "value": 0}]`), `names "device 00.0" twice`},
		{"another RAM block table", ramblocks("Name Size\npc.ram 1\n"), `"Name Size", not its table`},
		{"a line that is no block", ramblocks("Block Name PSize Offset Used Total HVA RO\npc.ram 4 KiB\n"), "is not a RAM block"},
		{"a length that is not one", ramblocks("Block Name PSize Offset Used Total HVA RO\npc.ram 4 KiB 0x0 0x0 0xzz 0x0 rw\n"), "invalid syntax"},
		{"a length not in hex", ramblocks("Block Name PSize Offset Used Total HVA RO\npc.ram 4 KiB 0x0 0x0 1000 0x0 rw\n"), "is not a RAM block"},
		{"no block but fw_cfg's", ramblocks("Block Name PSize Offset Used Total HVA RO\n/rom@etc/x 4 KiB 0x0 0x0 0x1 0x0 ro\n"), "no RAM block"},
		{"no vCPU", answer("query-cpus-fast", `[]`), "no vCPU"},
		{"a vCPU of another architecture", path("/machine/unattached/device[0]", `[{"name": "type", "type": "string", "value": "cortex-a72-arm-cpu"}]`), "not an x86_64 CPU model"},
		{"a model QEMU will not expand", fails("query-cpu-model-expansion"), "expanding the CPU model host"},
		{"an answer for another number of paths", answer("qom-list-get", `[]`), "0 objects for 1 paths"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeQEMU{answer: func(c call) string {
				if r, ok := tc.answer(c); ok {
					return r
				}
				return answering(c)
			}}
			if _, err := f.attach(t).Target(ctx(t)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Target = %v, want %q", err, tc.want)
			}
		})
	}
}

// fails is QEMU refusing command.
func fails(command string) func(c call) (string, bool) {
	return func(c call) (string, bool) { return refused("no"), c.Command == command }
}

// answer is QEMU answering command with v.
func answer(command, v string) func(c call) (string, bool) {
	return func(c call) (string, bool) { return ret(v), c.Command == command }
}

// failsReading is QEMU refusing a qom-list-get whose first path is p.
func failsReading(p string) func(c call) (string, bool) {
	return func(c call) (string, bool) {
		return refused("no"), c.Command == "qom-list-get" && c.Args["paths"].([]any)[0] == p
	}
}

// path is fakeMachine with the object at p replaced by props.
func path(p, props string) func(c call) (string, bool) {
	return func(c call) (string, bool) {
		if c.Command != "qom-list-get" {
			return "", false
		}
		var objs []string
		for _, q := range c.Args["paths"].([]any) {
			o := fakeMachine[q.(string)]
			if q == p {
				o = props
			}
			objs = append(objs, `{"properties": `+o+`}`)
		}
		// On one line, as QEMU writes each message, with the space QEMU puts after each comma and
		// colon, which a record's value does not keep.
		return `{"return": [` + strings.Join(strings.Fields(strings.Join(objs, ", ")), " ") + `]}`, true
	}
}

// ramblocks is x-query-ramblock answering text.
func ramblocks(text string) func(c call) (string, bool) {
	b, _ := json.Marshal(map[string]string{"human-readable-text": text})
	return answer("x-query-ramblock", string(b))
}

// A resume holds a checkpoint to the Target it is handed, and never asks QEMU what the machine
// is: one taken on another machine is refused, with every difference, before QEMU is asked
// anything; one taken on this machine loads.
func TestResumeRefusesAnotherMachine(t *testing.T) {
	this := fakeTarget(t)
	richer := fakeTarget(t)
	richer.CPU.Features = append(richer.CPU.Features, "amx-tile")
	for _, tc := range []struct {
		name string
		from Target
		want []Difference
	}{
		{"this machine", this, nil},
		{"a CPU this machine does not show", richer, []Difference{{Part: "cpu features", Checkpoint: "amx-tile", Rule: Same}}},
		{"no record", Target{}, []Difference{{Part: "vmm", Here: "qemu 11.2.0", Rule: AsNew}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeQEMU{}
			held, loaded := machineAnswers("inmigrate"), machineAnswers("running")
			f.answer = migrations(func(c call) string {
				if slices.Contains(f.commands(), "migrate-incoming") {
					return loaded(c)
				}
				return held(c)
			}, "RESUME", "completed")
			err := f.attach(t).Resume(ctx(t), image(t, "raw").File, Saved{Format: FormatMappedRAM, From: tc.from}, this, LoadOptions{})
			var r *Refusal
			switch {
			case tc.want == nil && err != nil:
				t.Fatalf("Resume = %v", err)
			case tc.want != nil && (!errors.As(err, &r) || !reflect.DeepEqual(r.Differences, tc.want)):
				t.Fatalf("Resume = %v, want a refusal of %+v", err, tc.want)
			}
			asked := f.commands()
			if tc.want != nil && len(asked) > 0 {
				t.Errorf("a refused checkpoint asked QEMU %v", asked)
			}
			if read := slices.Contains(asked, "migrate-incoming"); read != (tc.want == nil) {
				t.Errorf("the state was read: %t", read)
			}
			for _, c := range []string{"query-cpus-fast", "qom-list-get", "query-accelerators", "x-query-ramblock"} {
				if slices.Contains(asked, c) {
					t.Errorf("Resume asked QEMU %s, a part of the record it was handed", c)
				}
			}
		})
	}
}

// A named model is read off the vCPU like host: the features it has on, of the model's own flags,
// and the properties it has of those the record holds: no host-cache-info, which only host's and
// max's vCPUs have.
func TestCPUOfANamedModel(t *testing.T) {
	vcpu := path("/machine/unattached/device[0]", `[{"name": "type", "type": "string", "value": "Skylake-Server-v4-x86_64-cpu"},
		{"name": "vendor", "type": "string", "value": "GenuineIntel"}, {"name": "avx512f", "type": "bool", "value": true},
		{"name": "avx2", "type": "bool", "value": false}, {"name": "family", "type": "uint64", "value": 6}]`)
	f := &fakeQEMU{answer: func(c call) string {
		if c.Command == "query-cpu-model-expansion" {
			return ret(`{"model": {"name": "base", "props": {"avx512f": true, "avx2": true, "model-id": "x"}}}`)
		}
		if r, ok := vcpu(c); ok {
			return r
		}
		return answering(c)
	}}
	got, err := f.attach(t).CPU(ctx(t))
	if want := (CPU{Vendor: "GenuineIntel", Model: "Skylake-Server-v4", Features: []string{"avx512f"}, Properties: map[string]string{"family": "6"}}); err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("CPU = %+v, %v, want %+v", got, err, want)
	}
	if got := f.sent("query-cpu-model-expansion")[0]["model"]; !reflect.DeepEqual(got, map[string]any{"name": "Skylake-Server-v4"}) {
		t.Errorf("expanded %v", got)
	}
}

// ReadTarget hands back the record of a machine of the caller's Spec, held and never run, and
// ends it. What is one machine's and not the record's is not opened: the caller's monitor, its
// console file, a TAP or /dev/vhost-net; and a machine with a console still has its serial port.
func TestReadTargetReadsAMachineHeldForItAndEndsIt(t *testing.T) {
	t.Setenv("FAKE_STATUS", "prelaunch")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(t.TempDir(), "gone")
	for _, tc := range []struct {
		name, console string
		// serial is how the command line gives the machine its serial port.
		serial string
		// handed is how many descriptors QEMU is given: Start's monitor, and the console's pipe.
		handed int
	}{
		{"with a console", "file:" + filepath.Join(gone, "console"), "opaque=console", 2},
		{"with none", "", "-serial none", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := Spec{QEMU: self, Kernel: bzImageStub(t), Firmware: t.TempDir(), Accel: "tcg", BootCPUs: 1, Memory: Memory{BaseMB: 128},
				Monitors: []Monitor{{Socket: filepath.Join(gone, "qmp.sock")}},
				NICs:     []NIC{{MAC: "52:54:00:12:34:56", MTU: 1450}},
				Console:  tc.console}
			var args []string
			var handed int
			var asked []string
			var mu sync.Mutex
			got, err := ReadTarget(ctx(t), s, StartOptions{
				Cmd: func(c *exec.Cmd) {
					c.Env = append(os.Environ(), fakeMode+"=obeys", "GORACE=atexit_sleep_ms=0")
					args, handed = c.Args, len(c.ExtraFiles)
				},
				Observer: qmp.Observer{Command: func(ctx context.Context, name string) (context.Context, func(error)) {
					mu.Lock()
					asked = append(asked, name)
					mu.Unlock()
					return ctx, func(error) {}
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if want := []NIC{{MAC: "52:54:00:12:34:56", MTU: 1450}}; !reflect.DeepEqual(s.NICs, want) {
				t.Errorf("the caller's NICs became %+v", s.NICs)
			}
			if want := fakeTarget(t); !reflect.DeepEqual(got, want) {
				t.Errorf("ReadTarget = %+v, want what Target reads, %+v", got, want)
			}
			line := strings.Join(args, " ")
			if !strings.Contains(line, "-netdev hubport,id=net0,hubid=0 -device virtio-net-pci,netdev=net0,") ||
				!strings.Contains(line, ",host_mtu=1450") || !strings.Contains(line, tc.serial) || strings.Contains(line, gone) {
				t.Errorf("the machine held for its record was %s", line)
			}
			if handed != tc.handed {
				t.Errorf("QEMU was handed %d descriptors, want %d", handed, tc.handed)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(asked) == 0 || asked[len(asked)-1] != "quit" {
				t.Errorf("asked %v, want the machine quit once it had said", asked)
			}
		})
	}
}

// A machine that cannot be started says why, in QEMU's words.
func TestReadTargetSaysWhyTheMachineDidNotStart(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	s := Spec{QEMU: self, Kernel: bzImageStub(t), Firmware: t.TempDir(), Accel: "tcg", BootCPUs: 1, Memory: Memory{BaseMB: 128}}
	_, err = ReadTarget(ctx(t), s, StartOptions{Cmd: func(c *exec.Cmd) {
		c.Env = append(os.Environ(), fakeMode+"=refuses", "GORACE=atexit_sleep_ms=0")
	}})
	if err == nil || !strings.Contains(err.Error(), "reading the record") || !strings.Contains(err.Error(), "Host doesn't support requested features") {
		t.Errorf("ReadTarget = %v, want why QEMU refused", err)
	}
}

// The machine held for a record with a TPM has a TPM state of its own, made for it and removed: the
// caller's directory is a workspace's, which swtpm would lock and the machine write.
func TestReadTargetGivesItsMachineATPMOfItsOwn(t *testing.T) {
	t.Setenv("FAKE_STATUS", "prelaunch")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(t.TempDir(), "workspace-tpm")
	var used string
	s := Spec{QEMU: self, Kernel: bzImageStub(t), Firmware: t.TempDir(), Accel: "tcg", BootCPUs: 1, Memory: Memory{BaseMB: 128},
		TPM: TPM{SWTPM: self, State: workspace, Kernel: [32]byte{1}}}
	if _, err := ReadTarget(ctx(t), s, StartOptions{
		Cmd: func(c *exec.Cmd) { c.Env = append(os.Environ(), fakeMode+"=obeys", "GORACE=atexit_sleep_ms=0") },
		TPMCmd: func(c *exec.Cmd) {
			c.Env = append(os.Environ(), fakeMode+"=swtpm", "GORACE=atexit_sleep_ms=0")
			used = strings.TrimSuffix(strings.TrimPrefix(c.Args[4], "dir="), ",mode=0600")
		},
	}); err != nil {
		t.Fatal(err)
	}
	if used == "" || used == workspace {
		t.Errorf("the machine held for its record ran its TPM on %q, the caller's is %q", used, workspace)
	}
	for _, dir := range []string{used, workspace} {
		if _, err := os.Stat(dir); err == nil {
			t.Errorf("%s is left after ReadTarget", dir)
		}
	}
}

// The machine held for a record with a NIC on passt runs no passt: its NIC is on a hub port as a
// TAP's is, so the record is the same for both, and the guest's side of passt is not on its
// command line.
func TestReadTargetRunsNoPasst(t *testing.T) {
	t.Setenv("FAKE_STATUS", "prelaunch")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	nics := []NIC{{Passt: filepath.Join(t.TempDir(), "no-passt"), MAC: "52:54:00:12:34:56"}}
	s := Spec{QEMU: self, Kernel: bzImageStub(t), Firmware: t.TempDir(), Accel: "tcg", BootCPUs: 1, Memory: Memory{BaseMB: 128}, NICs: nics}
	var line string
	if _, err := ReadTarget(ctx(t), s, StartOptions{Cmd: func(c *exec.Cmd) {
		c.Env = append(os.Environ(), fakeMode+"=obeys", "GORACE=atexit_sleep_ms=0")
		line = strings.Join(c.Args, " ")
	}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, "-netdev hubport,id=net0,hubid=0 -device virtio-net-pci,netdev=net0,") || strings.Contains(line, "set_credential") {
		t.Errorf("the machine held for its record was %s", line)
	}
	if s.NICs[0].Passt != nics[0].Passt {
		t.Errorf("the caller's NIC became %+v", s.NICs[0])
	}
}
