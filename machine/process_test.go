// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/spin-stack/machine/qmp"
)

// fakeMode is the variable that makes this test binary a QEMU, and says which: Start is held to
// what a process does - exits, refuses, ignores a button, stops answering - which only a process
// can do.
const fakeMode = "MACHINE_FAKE_QEMU"

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeMode); mode != "" {
		os.Exit(fakeQEMUMain(mode, os.Args[1:]))
	}
	os.Exit(m.Run())
}

// fakeQEMUMain is QEMU as Start runs it, in mode:
//
//   - obeys: the power button ends a running guest, quit ends QEMU, each with its SHUTDOWN.
//   - ignores-button: quit ends QEMU; the button is answered and does nothing.
//   - ignores-all: the button and quit are answered and do nothing.
//   - busy: the handshake is answered and nothing after it, as while a state loads.
//   - silent: no monitor is ever accepted.
//   - refuses: it says why on stderr and exits 1, as QEMU refusing its command line does.
//   - panics: the console says the kernel panicked, and QEMU exits as panic=1 has it.
func fakeQEMUMain(mode string, args []string) int {
	if strings.HasPrefix(mode, "swtpm") {
		return fakeSWTPMMain(mode, args)
	}
	if mode == "refuses" {
		fmt.Fprintln(os.Stderr, "qemu-system-x86_64: warning: something on the way")
		fmt.Fprintln(os.Stderr, "qemu-system-x86_64: -cpu Fancy: Host doesn't support requested features")
		return 1
	}
	if mode == "silent" {
		time.Sleep(time.Minute)
		return 0
	}
	status := os.Getenv("FAKE_STATUS")
	if status == "" {
		status = "running"
	}
	f := &fake{mode: mode, status: status, exit: make(chan int, 1)}
	for i, a := range args {
		if i == 0 {
			continue
		}
		switch args[i-1] {
		case "-chardev":
			// The TPM's control channel: what swtpm reads first is QEMU's, and QEMU's exit closes it.
			if m := regexp.MustCompile(`^socket,id=tpm,fd=(\d+)$`).FindStringSubmatch(a); m != nil {
				n, _ := strconv.Atoi(m[1])
				_, _ = os.NewFile(uintptr(n), "tpm").WriteString("TPM2_Startup\n")
			}
			// A QEMU that panics never serves its monitors: one served first would answer the
			// handshake before the console is written.
			if m := regexp.MustCompile(`^socket,id=\w+,fd=(\d+),server=on`).FindStringSubmatch(a); m != nil && mode != "panics" {
				n, _ := strconv.Atoi(m[1])
				l, err := net.FileListener(os.NewFile(uintptr(n), "monitor"))
				if err != nil {
					fmt.Fprintln(os.Stderr, "fake: a monitor descriptor that does not listen:", err)
					return 2
				}
				go f.accept(l)
			}
		case "-add-fd":
			if m := regexp.MustCompile(`^fd=(\d+),set=\d+,opaque=console$`).FindStringSubmatch(a); m != nil && mode == "panics" {
				n, _ := strconv.Atoi(m[1])
				c := os.NewFile(uintptr(n), "console")
				_, _ = c.WriteString("[    0.9] Run /sbin/init as init process\n[    1.0] Kernel pa")
				_, _ = c.WriteString("nic - not syncing: Attempted to kill init! exitcode=0x00000100\n")
				return 0
			}
		}
	}
	return <-f.exit
}

// fakeSWTPMMain is swtpm as Start runs it: its state directory and its control channel, descriptor 3,
// from its command line. It writes into the directory the line QEMU sent on the channel, and exits
// once QEMU's end is closed, as swtpm does: 0, or, as swtpm-fails, 1 having said why.
func fakeSWTPMMain(mode string, args []string) int {
	want := []string{"socket", "--tpm2", "--tpmstate", "", "--ctrl", "type=unixio,clientfd=3"}
	if len(args) != len(want) || !strings.HasPrefix(args[3], "dir=") || !strings.HasSuffix(args[3], ",mode=0600") {
		fmt.Fprintf(os.Stderr, "fake swtpm: a command line swtpm would not be given: %q\n", args)
		return 2
	}
	for i, w := range want {
		if w != "" && args[i] != w {
			fmt.Fprintf(os.Stderr, "fake swtpm: argument %d is %q, not %q\n", i, args[i], w)
			return 2
		}
	}
	dir := strings.TrimSuffix(strings.TrimPrefix(args[3], "dir="), ",mode=0600")
	said, err := io.ReadAll(os.NewFile(3, "control"))
	if err != nil {
		return 2
	}
	if err := os.WriteFile(filepath.Join(dir, "said"), said, 0o600); err != nil {
		return 2
	}
	if mode == "swtpm-fails" {
		fmt.Fprintln(os.Stderr, "swtpm: Could not open lockfile: Resource temporarily unavailable")
		return 1
	}
	return 0
}

type fake struct {
	mode, status string
	exit         chan int
	mu           sync.Mutex
	clients      []*bufio.Writer
}

func (f *fake) accept(l net.Listener) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go f.serve(c)
	}
}

// broadcast sends an event to every monitor, and ends QEMU with code.
func (f *fake) end(event string, code int) {
	f.mu.Lock()
	for _, w := range f.clients {
		_, _ = w.WriteString(event + "\n")
		_ = w.Flush()
	}
	f.mu.Unlock()
	f.exit <- code
}

func (f *fake) serve(c net.Conn) {
	w := bufio.NewWriter(c)
	f.mu.Lock()
	f.clients = append(f.clients, w)
	_, _ = w.WriteString(`{"QMP": {"version": {"qemu": {"major": 11, "minor": 2, "micro": 0}, "package": ""}, "capabilities": []}}` + "\n")
	_ = w.Flush()
	f.mu.Unlock()
	in := bufio.NewScanner(c)
	for in.Scan() {
		var req struct {
			Execute   string          `json:"execute"`
			Arguments map[string]any  `json:"arguments"`
			ID        json.RawMessage `json:"id"`
		}
		_ = json.Unmarshal(in.Bytes(), &req)
		reply := `{"return": {}}`
		switch {
		case req.Execute == "qmp_capabilities":
		case f.mode == "busy":
			continue
		case req.Execute == "query-status":
			reply = `{"return": {"status": "` + f.status + `"}}`
		default:
			if r, ok := recordAnswers(call{Command: req.Execute, Args: req.Arguments}); ok {
				reply = r
			}
		}
		f.mu.Lock()
		_, _ = w.WriteString(reply[:len(reply)-1] + `, "id": ` + string(req.ID) + "}\n")
		_ = w.Flush()
		f.mu.Unlock()
		switch {
		case req.Execute == "system_powerdown" && f.mode == "obeys":
			f.end(`{"event": "SHUTDOWN", "data": {"guest": true, "reason": "guest-shutdown"}}`, 0)
		case req.Execute == "quit" && (f.mode == "obeys" || f.mode == "ignores-button"):
			fmt.Fprintln(os.Stderr, "qemu-system-x86_64: terminating on signal from QMP")
			f.end(`{"event": "SHUTDOWN", "data": {"guest": false, "reason": "host-qmp-quit"}}`, 0)
		}
	}
}

// startFake is Start of a fake QEMU in mode, with a Spec that validates and opens no device.
func startFake(t *testing.T, ctx context.Context, mode string, s Spec, o StartOptions) (*Process, error) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if s.Kernel == "" {
		s.Kernel = bzImageStub(t)
	}
	s.QEMU, s.Firmware, s.Accel = self, t.TempDir(), "tcg"
	s.BootCPUs, s.Memory.BaseMB = 1, 128
	shape := o.Cmd
	o.Cmd = func(c *exec.Cmd) {
		// Under -race a process sleeps a second on its way out (atexit_sleep_ms), which a QEMU does
		// not, and which Stop would wait out as a QEMU that did not quit.
		c.Env = append(os.Environ(), fakeMode+"="+mode, "GORACE=atexit_sleep_ms=0")
		if shape != nil {
			shape(c)
		}
	}
	p, err := Start(ctx, s, o)
	if err == nil {
		t.Cleanup(func() { p.Kill() })
	}
	return p, err
}

func signalOf(e Exit) syscall.Signal {
	ws := e.State.Sys().(syscall.WaitStatus)
	if !ws.Signaled() {
		return 0
	}
	return ws.Signal()
}

func TestStopEscalatesUntilQEMUEnds(t *testing.T) {
	for _, c := range []struct {
		mode, status string
		code         int
		signal       syscall.Signal
		end          ShutdownCause
		// asked is the commands Stop sent, in order.
		asked string
	}{
		{"obeys", "running", 0, 0, GuestShutdown, "query-status system_powerdown"},
		// A guest that is not running cannot answer its button: QEMU is asked to quit.
		{"obeys", "prelaunch", 0, 0, "host-qmp-quit", "query-status quit"},
		{"ignores-button", "running", 0, 0, "host-qmp-quit", "query-status system_powerdown quit"},
		{"ignores-all", "running", -1, syscall.SIGKILL, "", "query-status system_powerdown quit"},
		// A QEMU loading a state answers nothing, and is killed once the status goes unanswered.
		{"busy", "running", -1, syscall.SIGKILL, "", "query-status"},
	} {
		t.Run(c.mode+" "+c.status, func(t *testing.T) {
			t.Setenv("FAKE_STATUS", c.status)
			var asked []string
			var mu sync.Mutex
			p, err := startFake(t, ctx(t), c.mode, Spec{}, StartOptions{Observer: qmp.Observer{
				Command: func(ctx context.Context, name string) (context.Context, func(error)) {
					mu.Lock()
					asked = append(asked, name)
					mu.Unlock()
					return ctx, func(error) {}
				},
			}})
			if err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			asked = nil // the handshake's
			mu.Unlock()
			start := time.Now()
			e := p.Stop(context.Background())
			took := time.Since(start)
			if e.State.ExitCode() != c.code || signalOf(e) != c.signal {
				t.Errorf("Stop ended QEMU with %s, want code %d, signal %v", e.State, c.code, c.signal)
			}
			switch {
			case c.end == "" && e.End != nil:
				t.Errorf("Exit.End %+v of a QEMU that sent no SHUTDOWN", *e.End)
			case c.end != "" && (e.End == nil || e.End.Reason != c.end):
				t.Errorf("Exit.End %+v, want %s", e.End, c.end)
			}
			mu.Lock()
			if got := strings.Join(asked, " "); got != c.asked {
				t.Errorf("Stop asked %q, want %q", got, c.asked)
			}
			mu.Unlock()
			// Each step is bounded: a QEMU deaf to all of it is killed after the button's and
			// quit's waits, and one that answers nothing after the status's.
			if limit := powerOffWait + quitWait + 3*answerWait; took > limit {
				t.Errorf("Stop took %v, over every step's bound %v", took, limit)
			}
			if again := p.Stop(context.Background()); again.State != e.State {
				t.Errorf("a second Stop said %s, the first %s", again.State, e.State)
			}
			select {
			case <-p.Exited():
			default:
				t.Error("Exited is open once Stop has returned")
			}
		})
	}
}

func TestStopKillsOnceItsContextIsDone(t *testing.T) {
	p, err := startFake(t, ctx(t), "ignores-all", Spec{}, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	e := p.Stop(c)
	if signalOf(e) != syscall.SIGKILL {
		t.Errorf("Stop ended QEMU with %s, want SIGKILL", e.State)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("Stop took %v with 100 ms to stop in", took)
	}
}

func TestAQEMUThatEndsBeforeItAnswersFailsTheStartAtOnce(t *testing.T) {
	var stderr bytes.Buffer
	start := time.Now()
	_, err := startFake(t, ctx(t), "refuses", Spec{}, StartOptions{Stderr: &stderr})
	if err == nil {
		t.Fatal("Start of a QEMU that exited succeeded")
	}
	for _, want := range []string{"QEMU ended before its monitor answered: exit status 1", "Host doesn't support requested features", "warning: something on the way"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Start's error %q does not say %q", err, want)
		}
	}
	if !strings.Contains(stderr.String(), "Host doesn't support") {
		t.Errorf("the caller's stderr got %q", stderr.String())
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("Start took %v to see QEMU had exited", took)
	}
}

func TestAMonitorThatNeverAnswersFailsTheStartAndQEMUGoes(t *testing.T) {
	c, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := startFake(t, c, "silent", Spec{}, StartOptions{})
	if err == nil || !strings.Contains(err.Error(), "did not answer") || !strings.Contains(err.Error(), "signal: killed") {
		t.Errorf("Start of a QEMU that never greets: %v, want its monitor not answering and QEMU killed", err)
	}
}

func TestExitSaysAPanicOnlyFromAConsoleStartReads(t *testing.T) {
	var console bytes.Buffer
	p, err := startFake(t, ctx(t), "panics", Spec{}, StartOptions{Console: &console})
	if err == nil {
		e := p.Wait()
		t.Fatalf("Start of a QEMU that panicked before its monitor answered succeeded: %s", e)
	}
	if !strings.Contains(err.Error(), "the guest having panicked: Kernel panic - not syncing: Attempted to kill init! exitcode=0x00000100") {
		t.Errorf("Start's error %q does not carry the panic's line", err)
	}
	if !strings.Contains(console.String(), "Run /sbin/init") {
		t.Errorf("the caller's console got %q", console.String())
	}
}

func TestStartBindsTheCallersMonitorsBeforeQEMURuns(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "pt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "q")
	// A socket a machine before left behind is replaced.
	if l, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"}); err == nil {
		l.SetUnlinkOnClose(false)
		_ = l.Close()
	}
	p, err := startFake(t, ctx(t), "obeys", Spec{Monitors: []Monitor{{Socket: sock}}}, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := connect(conn)
	if err != nil {
		t.Fatalf("the caller's monitor: %v", err)
	}
	defer func() { _ = other.Close() }()
	// The machine's own monitor ends it, and the caller's sees it go.
	if err := p.Quit(ctx(t)); err != nil {
		t.Fatal(err)
	}
	if e := p.Wait(); e.State.ExitCode() != 0 || e.End == nil {
		t.Errorf("a quit QEMU ended %s", e)
	}
	select {
	case <-other.Done():
	case <-time.After(5 * time.Second):
		t.Error("the caller's monitor outlived QEMU")
	}
	if _, err := os.Lstat(sock); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the monitor's socket is still there once QEMU has exited: %v", err)
	}
}

// A socket bound at the monitor's path while QEMU was still to be reaped is the next machine's,
// and stays once QEMU has gone (#507).
func TestTheMonitorsSocketBoundAgainOutlivesTheQEMUBefore(t *testing.T) {
	for _, c := range []struct {
		name string
		// again makes the next machine's socket at path.
		again func(t *testing.T, path string)
	}{
		{"at a new inode", func(t *testing.T, path string) {
			// The next machine's Start replaces a socket left at its path, as listen does.
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			next, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = next.Close() })
		}},
		{"at the freed inode, as ext4 hands one out again", func(t *testing.T, path string) {
			// A new socket at the old one's inode differs from it in when it was made.
			when := time.Now().Add(time.Hour)
			if err := os.Chtimes(path, when, when); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "pt")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			sock := filepath.Join(dir, "q")
			p, err := startFake(t, ctx(t), "obeys", Spec{Monitors: []Monitor{{Socket: sock}}}, StartOptions{})
			if err != nil {
				t.Fatal(err)
			}
			c.again(t, sock)
			if e := p.Kill(); signalOf(e) != syscall.SIGKILL {
				t.Fatalf("a killed QEMU ended %s", e)
			}
			if _, err := os.Lstat(sock); err != nil {
				t.Errorf("the next machine's socket, once the QEMU before it was reaped: %v", err)
			}
		})
	}
}

// A file as Stderr is QEMU's own: what QEMU writes reaches it with nothing copied in the caller's
// process, so it still does once the caller has closed its copy, as a caller that restarted has.
func TestAFileStderrIsQEMUsOwn(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	p, err := startFake(t, ctx(t), "obeys", Spec{}, StartOptions{Stderr: w})
	_ = w.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Quit(ctx(t)); err != nil {
		t.Fatal(err)
	}
	e := p.Wait()
	if e.State.ExitCode() != 0 {
		t.Errorf("a quit QEMU ended %s", e)
	}
	said, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(said), "terminating on signal from QMP") {
		t.Errorf("the caller's file got %q, want what QEMU wrote once the caller had closed its copy", said)
	}
	if e.Stderr != "" {
		t.Errorf("Exit kept %q of a stderr that went to the caller's file", e.Stderr)
	}
}

// The caller's descriptors come first, from 3, and Start's are numbered after them.
func TestStartsDescriptorsComeAfterTheCallers(t *testing.T) {
	tap, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tap.Close() }()
	p, err := startFake(t, ctx(t), "obeys", Spec{}, StartOptions{Files: []*os.File{tap, tap}})
	if err != nil {
		t.Fatalf("a machine handed two descriptors: %v", err)
	}
	p.Kill()
}

func TestClosingTheMonitorIsNotQEMUsExit(t *testing.T) {
	p, err := startFake(t, ctx(t), "obeys", Spec{}, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_ = p.Close()
	select {
	case <-p.Exited():
		t.Fatal("Exited closed with the monitor")
	case <-time.After(100 * time.Millisecond):
	}
	// With no monitor to ask, Stop kills.
	if e := p.Stop(ctx(t)); signalOf(e) != syscall.SIGKILL || e.End != nil {
		t.Errorf("Stop of a QEMU whose monitor was closed: %s", e)
	}
}

func TestStartLeavesTheCallersSpecAlone(t *testing.T) {
	s := Spec{Monitors: make([]Monitor, 0, 4), FDSets: make([]FDSet, 0, 4)}
	var out bytes.Buffer
	p, err := startFake(t, ctx(t), "obeys", s, StartOptions{Console: &out})
	if err != nil {
		t.Fatal(err)
	}
	p.Kill()
	if got := s.Monitors[:1]; got[0] != (Monitor{}) {
		t.Errorf("Start wrote its monitor into the caller's array: %+v", got)
	}
	if got := s.FDSets[:1]; got[0].ID != 0 {
		t.Errorf("Start wrote its descriptor set into the caller's array: %+v", got)
	}
}

func TestHandoverOpensWhatTheSpecNeedsAndWasNotHanded(t *testing.T) {
	var opened []string
	open := func(path string) (*os.File, error) {
		opened = append(opened, path)
		return os.Open(os.DevNull)
	}
	h := &handover{first: 4}
	s := Spec{Accel: "kvm", VsockCID: 7, NICs: []NIC{{TapFD: 3}, {TapFD: 3, VhostFD: 9}, {TapFD: 3}},
		FDSets: []FDSet{{ID: 4}}}
	if err := h.devices(&s, open); err != nil {
		t.Fatal(err)
	}
	defer h.close()
	if got, want := strings.Join(opened, " "), "/dev/kvm /dev/vhost-vsock /dev/vhost-net /dev/vhost-net"; got != want {
		t.Errorf("opened %s, want %s", got, want)
	}
	if s.KVMFDSet != 5 || len(s.FDSets) != 2 || s.FDSets[1].FDs[0] != (FD{Num: 4, Opaque: "/dev/kvm"}) {
		t.Errorf("/dev/kvm is set %d of %+v, want set 5 holding descriptor 4", s.KVMFDSet, s.FDSets)
	}
	if s.VsockFD != 5 || s.NICs[0].VhostFD != 6 || s.NICs[1].VhostFD != 9 || s.NICs[2].VhostFD != 7 {
		t.Errorf("vsock at %d, NICs' vhost at %+v", s.VsockFD, s.NICs)
	}

	// What the caller handed, and TCG's, are opened by nobody.
	opened = nil
	h2 := &handover{first: 3}
	s = Spec{Accel: "tcg", VsockCID: 7, VsockFD: 8}
	if err := h2.devices(&s, open); err != nil || len(opened) != 0 {
		t.Errorf("opened %v (%v) for a machine handed everything", opened, err)
	}
	s = Spec{KVMFDSet: 1}
	if err := h2.devices(&s, open); err != nil || len(opened) != 0 {
		t.Errorf("opened %v (%v) for a machine handed /dev/kvm", opened, err)
	}

	// The caller's NICs are its own: the descriptors go to Start's copy.
	nics := []NIC{{TapFD: 3}}
	s = Spec{Accel: "tcg", NICs: nics}
	if err := h2.devices(&s, open); err != nil || nics[0].VhostFD != 0 || s.NICs[0].VhostFD == 0 {
		t.Errorf("the caller's NIC has vhost %d and Start's %d (%v)", nics[0].VhostFD, s.NICs[0].VhostFD, err)
	}
	h2.close()

	refused := errors.New("no")
	if err := h2.devices(&Spec{VsockCID: 3, Accel: "tcg"}, func(string) (*os.File, error) { return nil, refused }); !errors.Is(err, refused) {
		t.Errorf("a device that would not open: %v", err)
	}
}

func TestAFileConsoleIsOpenedForQEMU(t *testing.T) {
	path := filepath.Join(t.TempDir(), "console.log")
	if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &handover{first: 3}
	s := Spec{Console: "file:" + path}
	r, err := h.console(&s, nil)
	if err != nil || r != nil {
		t.Fatalf("console: %v, %v", r, err)
	}
	if s.Console != "" || s.ConsoleFDSet != 1 || s.FDSets[0].FDs[0].Opaque != path {
		t.Errorf("the console is %q, set %d of %+v", s.Console, s.ConsoleFDSet, s.FDSets)
	}
	_, _ = h.files[0].WriteString("after\n")
	h.close()
	if b, _ := os.ReadFile(path); string(b) != "before\nafter\n" {
		t.Errorf("the console file holds %q: it is appended to", b)
	}
	s = Spec{Console: "mon:stdio"}
	if r, err := h.console(&s, nil); r != nil || err != nil || s.Console != "mon:stdio" || len(h.files) != 0 {
		t.Errorf("a stdio console was handed over: %q, %v", s.Console, err)
	}
	s = Spec{Console: "file:" + filepath.Join(path, "x")}
	if _, err := h.console(&s, nil); err == nil {
		t.Error("a console that cannot be opened")
	}
}

func TestTheStderrTailIsItsEnd(t *testing.T) {
	var all bytes.Buffer
	tl := &tail{w: &all}
	_, _ = tl.Write(bytes.Repeat([]byte("a"), tailSize))
	_, _ = tl.Write([]byte("the end"))
	if got := tl.String(); len(got) != tailSize || !strings.HasSuffix(got, "aathe end") {
		t.Errorf("the tail is %d bytes ending %q", len(got), got[len(got)-10:])
	}
	if all.Len() != tailSize+7 {
		t.Errorf("the caller got %d bytes", all.Len())
	}
	if n, err := (&tail{}).Write([]byte("x")); n != 1 || err != nil {
		t.Errorf("a tail with nowhere to pass it on: %d, %v", n, err)
	}
	f := &failing{}
	broken := &tail{w: f}
	for range 2 {
		if n, err := broken.Write([]byte("x")); n != 1 || err != nil {
			t.Errorf("a failing writer stopped the tail: %d, %v", n, err)
		}
	}
	if broken.String() != "xx" || f.n != 1 {
		t.Errorf("the tail is %q and the failing writer was written %d times, want once", broken.String(), f.n)
	}
}

// failing is a writer that fails, counting how often it was asked.
type failing struct{ n int }

func (f *failing) Write([]byte) (int, error) { f.n++; return 0, errors.New("full") }

func TestThePanicWatchKeepsTheFirstPanicsLine(t *testing.T) {
	var out bytes.Buffer
	c := &panicWatch{w: &out}
	for _, s := range []string{"boot\n" + strings.Repeat("x", 600) + "Kernel panic hidden past the cut\n", "[1] Kernel panic - one", "\n[2] Kernel panic - two\n"} {
		if n, err := c.Write([]byte(s)); n != len(s) || err != nil {
			t.Errorf("Write(%d bytes) = %d, %v", len(s), n, err)
		}
	}
	if got := c.panicked(); got != "Kernel panic - one" {
		t.Errorf("the panic is %q", got)
	}
	if !strings.HasSuffix(out.String(), "two\n") {
		t.Error("the console was not passed on whole")
	}
	// A line is kept to 512 bytes, which the mark has to end within, from the line's start.
	for _, tc := range []struct {
		pad  int
		want string
	}{{500, "Kernel panic"}, {501, ""}} {
		c := &panicWatch{w: &out}
		_, _ = c.Write([]byte("a\n" + strings.Repeat("x", tc.pad) + "Kernel panic\n"))
		if got := c.panicked(); got != tc.want {
			t.Errorf("a mark after %d bytes: %q, want %q", tc.pad, got, tc.want)
		}
	}
	// The console arrives in pieces: a line ends at a newline written alone, and is cut at 512
	// bytes across the writes that make it.
	for _, tc := range []struct {
		writes []string
		want   string
	}{
		{[]string{"[1] Kernel panic - one", "\n", "x\n"}, "Kernel panic - one"},
		{[]string{"a\n" + strings.Repeat("x", 300), strings.Repeat("x", 201) + "Kernel panic\n"}, ""},
	} {
		c := &panicWatch{w: &out}
		for _, w := range tc.writes {
			_, _ = c.Write([]byte(w))
		}
		if got := c.panicked(); got != tc.want {
			t.Errorf("%q: %q, want %q", tc.writes, got, tc.want)
		}
	}
	f := &failing{}
	broken := &panicWatch{w: f}
	_, _ = broken.Write([]byte("boot\n"))
	if n, _ := broken.Write([]byte("Kernel panic\n")); n != 13 || broken.panicked() != "Kernel panic" || f.n != 1 {
		t.Errorf("a failing console stopped the watch: %d %q, or was written %d times, want once", n, broken.panicked(), f.n)
	}
}

func TestExitSaysHowQEMUEnded(t *testing.T) {
	p, err := startFake(t, ctx(t), "obeys", Spec{}, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	e := p.Stop(ctx(t))
	// A prefix: a test binary built for coverage warns on its stderr, which the Exit carries.
	if got, want := e.String(), "exit status 0, the machine having ended (guest-shutdown)"; !strings.HasPrefix(got, want) {
		t.Errorf("Exit says %q, want %q", got, want)
	}
	if p.Pid() <= 0 {
		t.Errorf("pid %d", p.Pid())
	}
}

// tpmSpec is a Spec with a TPM whose swtpm is this test binary, faking it in mode.
func tpmSpec(t *testing.T, mode string) (Spec, StartOptions, string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "tpm")
	return Spec{TPM: TPM{SWTPM: self, State: dir}}, StartOptions{TPMCmd: func(c *exec.Cmd) {
		c.Env = append(os.Environ(), fakeMode+"="+mode, "GORACE=atexit_sleep_ms=0")
	}}, dir
}

// A machine with a TPM runs swtpm on the caller's state directory, made for it, and on a control
// channel whose other end is QEMU's; swtpm has ended by the time Exited is closed, so the next
// machine of the workspace can take the directory, and what swtpm said on its way out is in Exit
// when it failed.
func TestStartRunsTheTPMBesideQEMUAndEndsItWithQEMU(t *testing.T) {
	for _, tc := range []struct {
		mode, said string
	}{{"swtpm", ""}, {"swtpm-fails", "swtpm exit status 1: swtpm: Could not open lockfile"}} {
		t.Run(tc.mode, func(t *testing.T) {
			s, o, dir := tpmSpec(t, tc.mode)
			p, err := startFake(t, ctx(t), "obeys", s, o)
			if err != nil {
				t.Fatal(err)
			}
			e := p.Stop(ctx(t))
			select {
			case <-p.Exited():
			default:
				t.Fatal("Stop returned before Exited closed")
			}
			if b, err := os.ReadFile(filepath.Join(dir, "said")); err != nil || string(b) != "TPM2_Startup\n" {
				t.Errorf("swtpm read %q from its control channel (%v), and QEMU wrote TPM2_Startup", b, err)
			}
			if got := e.Stderr; (tc.said == "") != !strings.Contains(got, "swtpm") || !strings.Contains(got, tc.said) {
				t.Errorf("Exit.Stderr is %q, want it to say %q", got, tc.said)
			}
		})
	}
}

// A QEMU that never starts takes its swtpm with it: Start's failure leaves no emulator holding the
// workspace's TPM.
func TestAStartThatFailsEndsTheTPM(t *testing.T) {
	s, o, dir := tpmSpec(t, "swtpm")
	if _, err := startFake(t, ctx(t), "refuses", s, o); err == nil {
		t.Fatal("a QEMU that refused its command line started")
	}
	if _, err := os.Stat(filepath.Join(dir, "said")); err != nil {
		t.Errorf("swtpm had not ended when Start returned: %v", err)
	}
}

// A swtpm that is not there fails Start before QEMU runs, naming it.
func TestAMissingSWTPMFailsTheStart(t *testing.T) {
	s, o, _ := tpmSpec(t, "swtpm")
	s.TPM.SWTPM = filepath.Join(t.TempDir(), "swtpm")
	if _, err := startFake(t, ctx(t), "obeys", s, o); err == nil || !strings.Contains(err.Error(), s.TPM.SWTPM) {
		t.Fatalf("Start with no swtpm = %v, want an error naming it", err)
	}
}
