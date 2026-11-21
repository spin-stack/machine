// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"cmp"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// outputDir is the release tree the QEMU tests ask. The default is the tree beside this
// package; the variable is what `spin-machine-dev verify args` passes, because OUTPUT_DIR can move and
// a check that looked at the old location would answer about a QEMU nobody is building any
// more.
func outputDir(t *testing.T) string {
	t.Helper()
	out, err := filepath.Abs(cmp.Or(os.Getenv("SPIN_MACHINE_OUTPUT"), "../_output"))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// qemuVM is a QEMU this test started and kills when it ends.
type qemuVM struct {
	qemu string
	args []string
	out  *strings.Builder
	done chan struct{}
	exit error
}

// startQEMU runs qemu with files at descriptors 3 onwards.
//
// done is closed rather than sent to, so that both a caller and the cleanup can wait on
// it. Sent to, the first receive took the value and the second blocked for the whole test
// timeout — which is how a rejected argument presented as a hang instead of the error QEMU
// had already printed.
func startQEMU(t *testing.T, qemu string, args []string, files ...*os.File) *qemuVM {
	t.Helper()
	vm := &qemuVM{qemu: qemu, args: args, out: &strings.Builder{}, done: make(chan struct{})}
	cmd := exec.Command(qemu, args...) // #nosec G204 -- the binary and arguments this test built
	cmd.Stderr, cmd.Stdout = vm.out, vm.out
	cmd.ExtraFiles = files
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting %s: %v", qemu, err)
	}
	go func() { vm.exit = cmd.Wait(); close(vm.done) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-vm.done
	})
	return vm
}

// fail names the argument that was rejected, because a check whose failure says "exit
// status 1" costs more than it saves. QEMU names it itself — "-device
// virtio-balloon-pci,...: Property '...' not found" — so its output is reproduced whole,
// under the command line that produced it, one argument per line.
func (vm *qemuVM) fail(t *testing.T, what string, err error) {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %v\n\n%s", what, err, vm.qemu)
	for _, a := range vm.args {
		fmt.Fprintf(&b, " \\\n  %s", a)
	}
	if out := strings.TrimSpace(vm.out.String()); out != "" {
		fmt.Fprintf(&b, "\n\nQEMU said:\n%s", out)
	} else {
		b.WriteString("\n\nQEMU said nothing.")
	}
	t.Fatal(b.String())
}

// qmp is a monitor of a qemuVM, past the greeting and the capabilities handshake.
type qmp struct {
	t   *testing.T
	vm  *qemuVM
	enc *json.Encoder
	dec *json.Decoder
}

// dialQMP connects to one monitor of vm.
//
// The socket exists as soon as the chardev is created, which is before the CPU is created
// and long before any device is realized, so its existence proves nothing — a machine
// whose CPU model the accelerator refused left a listening socket behind and had already
// exited. An answer on it proves it, which is why the callers that ask whether a machine
// started send query-status and not only read the greeting.
func dialQMP(t *testing.T, socket string, vm *qemuVM) *qmp {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var conn net.Conn
	for {
		select {
		case <-vm.done:
			// The usual failure: QEMU rejected an argument and exited.
			vm.fail(t, "QEMU exited before answering on "+filepath.Base(socket), vm.exit)
		default:
		}
		c, err := net.Dial("unix", socket)
		if err == nil {
			conn = c
			break
		}
		if time.Now().After(deadline) {
			vm.fail(t, "QEMU never answered on "+filepath.Base(socket), err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		t.Fatal(err)
	}
	q := &qmp{t: t, vm: vm, enc: json.NewEncoder(conn), dec: json.NewDecoder(conn)}
	var greeting struct {
		QMP *struct{} `json:"QMP"`
	}
	if err := q.dec.Decode(&greeting); err != nil {
		vm.fail(t, "reading the QMP greeting", err)
	}
	if greeting.QMP == nil {
		vm.fail(t, "the monitor did not greet", fmt.Errorf("no QMP field"))
	}
	q.do("qmp_capabilities", nil)
	return q
}

// try sends one command and returns its reply, or QEMU's refusal as an error. Events in
// between are skipped.
func (q *qmp) try(command string, args any) (json.RawMessage, error) {
	q.t.Helper()
	req := map[string]any{"execute": command}
	if args != nil {
		req["arguments"] = args
	}
	if err := q.enc.Encode(req); err != nil {
		q.vm.fail(q.t, "sending "+command, err)
	}
	for {
		var reply struct {
			Return json.RawMessage `json:"return"`
			Event  string          `json:"event"`
			Error  *struct {
				Desc string `json:"desc"`
			} `json:"error"`
		}
		if err := q.dec.Decode(&reply); err != nil {
			q.vm.fail(q.t, "reading the reply to "+command, err)
		}
		if reply.Event != "" {
			continue
		}
		if reply.Error != nil {
			return nil, fmt.Errorf("%s", reply.Error.Desc)
		}
		return reply.Return, nil
	}
}

// do is try, failing the test on a refusal.
func (q *qmp) do(command string, args any) json.RawMessage {
	q.t.Helper()
	reply, err := q.try(command, args)
	if err != nil {
		q.vm.fail(q.t, command+" was refused", err)
	}
	return reply
}

// qemuTCG is the emulating build and the firmware beside it.
//
// The TCG binary and not the one a host runs: that one has no TCG compiled in
// (`spin-machine-dev verify qemu` asserts it) and refuses to start without /dev/kvm, which is a
// device CI does not have and a check must not require.
//
// The two paths out of a release tree, and not machine.OpenRelease, because it asks
// for a whole machine and this needs half of one: the lane that runs this check
// on every push has a QEMU and deliberately no kernel and no base image, both of
// which are tens of minutes to build.
func qemuTCG(t *testing.T) (qemu, firmware string) {
	t.Helper()

	out := outputDir(t)
	qemu = filepath.Join(out, qemuTCGName)
	if _, err := os.Stat(qemu); err != nil {
		t.Skipf("no QEMU at %s: %v\n\t`spin-machine-dev verify args` is the gate — it fetches one"+
			" and refuses rather than skipping", qemu, err)
	}
	return qemu, filepath.Join(out, firmwareDir)
}

// bzImageStub writes a kernel QEMU will load and never execute: a setup header with nothing
// behind it, the fields x86_load_linux reads (hw/i386/x86-common.c) and no code.
//
// A stub and not the kernel from _output/kernel, deliberately. What is under
// test is the command line, and requiring the real kernel would tie this check
// to the kernel build — tens of minutes — so it could only ever run in a lane
// that had one, which is the lane a machine/*.go change does not reach. QEMU
// still adds linuxboot_dma.bin out of the firmware directory for it, so -L and
// the option ROM that boot depends on are exercised either way.
//
// Without "HdrS" QEMU tries the file as a multiboot and then an ELF kernel, and
// exits with "Error loading uncompressed kernel without PVH ELF Note"; with fewer
// bytes than its setup sectors it exits with "invalid kernel header".
func bzImageStub(t *testing.T) string {
	t.Helper()

	const setupSects = 4
	le := binary.LittleEndian
	b := make([]byte, (setupSects+1)*512+512) // the setup sectors, the boot sector, a sector of payload
	b[0x1f1] = setupSects
	le.PutUint16(b[0x1fe:], 0xaa55) // boot_flag
	copy(b[0x202:], "HdrS")
	le.PutUint16(b[0x206:], 0x020f)     // boot protocol 2.15
	b[0x211] = 0x01                     // loadflags: LOADED_HIGH, the protected-mode code at 1 MiB
	le.PutUint32(b[0x22c:], 0x7fffffff) // initrd_addr_max
	b[0x234] = 1                        // relocatable_kernel
	le.PutUint16(b[0x236:], 0x0002)     // xloadflags: XLF_CAN_BE_LOADED_ABOVE_4G
	le.PutUint32(b[0x238:], 2048)       // cmdline_size
	b[(setupSects+1)*512] = 0xf4        // hlt, which -S means is never reached

	path := filepath.Join(t.TempDir(), "bzImage-stub")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// diskDir is a directory that supports O_DIRECT. The test's temporary directory does
// not when it is on tmpfs, which it is on hosts that mount /tmp that way, and QEMU then
// refuses cache.direct=on with EINVAL — a failure about the host, not the options. So a
// tmpfs one is swapped for one under this module's _output, which is on disk.
func diskDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		t.Fatal(err)
	}
	const tmpfsMagic = 0x01021994
	if st.Type != tmpfsMagic {
		return dir
	}
	if err := os.MkdirAll(filepath.Join("..", "_output"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(filepath.Join("..", "_output"), "direct-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// qcow2In creates a qcow2 in dir, over backing when it is not empty.
func qcow2In(t *testing.T, qemu, dir, name, backing string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	args := []string{"create", "-q", "-f", "qcow2"}
	if backing != "" {
		args = append(args, "-F", "qcow2", "-b", backing)
	}
	args = append(args, path, "16M")
	img := filepath.Join(filepath.Dir(qemu), "qemu-img")
	if out, err := exec.Command(img, args...).CombinedOutput(); err != nil { // #nosec G204 -- beside the binary under test
		t.Fatalf("%s %v: %v\n%s", img, args, err, out)
	}
	return path
}

// qmpSocket keeps the path short. A Unix socket address is 108 bytes including
// the terminator, and a test's temporary directory plus a socket name is close
// enough to it that QEMU has failed with "UNIX socket path is too long".
func qmpSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "qmp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "s")
}
