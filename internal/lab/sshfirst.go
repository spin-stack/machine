// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	xssh "golang.org/x/crypto/ssh"

	"github.com/spin-stack/spin-machine/boot"
)

// sshFirst is the goal's first number: when a workspace can first be logged into, from the host,
// over SSH through vsock, from the moment before QEMU is exec'd - the thing a workspace waits for,
// where the serial console's prompt is something nothing a workspace does waits for.
//
// The machine gets a vsock device, and the image's systemd-ssh-generator binds sshd to AF_VSOCK
// port 22 when it finds one. The host tries to connect every 2 ms; the first connection accepted
// is sshd_accepts, and `true` run as spin over SSH is first_login. What first_login holds beyond
// sshd_accepts is the first login's own chain: sshd's host keys, the key exchange, PAM, logind,
// the user's manager. The console's prompt is timed in the same boot, the getty an echo.
type sshFirst struct{}

func init() { register(sshFirst{}) }

func (sshFirst) Name() string { return "ssh-first" }
func (sshFirst) About() string {
	return "ms from the moment before QEMU is exec'd to sshd accepting on vsock, and to `true` run over SSH, as spin or, on spin's login, root"
}
func (sshFirst) Options() []Option     { return shape }
func (sshFirst) Rows(Options) []string { return []string{"boot"} }
func (sshFirst) Phases() int           { return 1 }

func (sshFirst) Needs(o Options) []Need {
	return append(append(bootNeeds, Need{"boot", "vsock-cid"}, Need{"boot", "console"}, Need{"boot", "append"}), plugNeeds(o)...)
}

func (sshFirst) Metrics() []Metric {
	return []Metric{
		{"sshd_accepts", "ms", "the first vsock connection accepted"},
		{"first_login", "ms", "`true` run as spin over SSH, mlkem768x25519"},
		{"accept_to_login", "ms", "first_login less sshd_accepts: the first login's own chain"},
		{"second_login", "ms", "a second login right after the first, from its connect: what any login costs"},
		{"console_prompt", "ms", "the serial console's prompt, the getty an echo"},
		{"curve25519_in", "count", "1 when a client offering only curve25519 got in, which the image refuses"},
		{"qemu_rss", "MiB", "QEMU's resident memory after the second login: a diagnostic, not a target"},
		{"qemu_rss_anon", "MiB", "of qemu_rss, anonymous memory: each machine's own"},
		{"qemu_rss_file", "MiB", "of qemu_rss, mapped files: shared between the machines that map them"},
		{"qemu_rss_shmem", "MiB", "of qemu_rss, shared memory, such as guest RAM in a memfd"},
	}
}

// mutate-lane: TestEveryProbeMeasuresATree
func (sshFirst) Measure(_ context.Context, m *Measurement) error {
	overlay, err := m.Overlay("overlay.qcow2", m.Key.Edits()...)
	if err != nil {
		return err
	}
	cid := NewCID()
	console, w := io.Pipe()
	// t0 before the exec, so QEMU's start and the firmware are inside what is measured.
	t0 := time.Now()
	qmp := filepath.Join(m.Socks, "m.qmp")
	args := append([]string{"--vsock-cid", fmt.Sprint(cid), "--console", "file:/dev/stdout", "--append", m.Tree.Cmdline()}, m.qmpToPlug(qmp)...)
	mc, err := m.Launch(w, m.BootArgs(overlay, args...)...)
	if err != nil {
		return err
	}
	plugged := m.Plug(qmp)
	defer func() { _ = console.Close(); mc.Stop(); _ = w.Close() }()

	var mu sync.Mutex
	var prompt time.Duration
	go func() {
		run, _ := boot.Watch(console, t0, time.Now, boot.Usable)
		mu.Lock()
		prompt = run.At[boot.Usable]
		mu.Unlock()
		_, _ = io.Copy(io.Discard, console)
	}()

	deadline := t0.Add(60 * time.Second)
	var accepts time.Duration
	for time.Now().Before(deadline) {
		if VsockAccepts(cid, 22) {
			accepts = time.Since(t0)
			break
		}
		if err := mc.Exited(); err != nil {
			return err
		}
		time.Sleep(2 * time.Millisecond)
	}
	if accepts == 0 {
		return Miss("sshd never accepted on vsock; spin-machine: %s", strings.TrimSpace(mc.Stderr.String()))
	}
	var login time.Duration
	var why error
	for time.Now().Before(deadline) {
		if _, why = m.Key.Login(cid, xssh.KeyExchangeMLKEM768X25519); why != nil {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		login = time.Since(t0)
		break
	}
	if login == 0 {
		return Miss("sshd accepted and no login came: %v", why)
	}
	if err := plugged(); err != nil {
		return err
	}
	m.RecordMs("sshd_accepts", accepts)
	m.RecordMs("first_login", login)
	m.RecordMs("accept_to_login", login-accepts)
	if d, err := m.Key.Login(cid, xssh.KeyExchangeMLKEM768X25519); err == nil {
		m.RecordMs("second_login", d)
	}
	// spin-machine boot execs QEMU in its own process, so the machine's pid is QEMU's.
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", mc.PID())); err == nil {
		for metric, field := range map[string]string{"qemu_rss": "VmRSS", "qemu_rss_anon": "RssAnon",
			"qemu_rss_file": "RssFile", "qemu_rss_shmem": "RssShmem"} {
			if mib, ok := statusMiB(string(b), field); ok {
				m.Record(metric, mib)
			}
		}
	}
	// Whether sshd turns away a client with no ML-KEM, as the image's drop-in has it do. A release
	// from before it lets one in, so this is counted rather than failed.
	_, err = m.Key.Login(cid, xssh.KeyExchangeCurve25519)
	m.Record("curve25519_in", map[bool]float64{true: 1, false: 0}[err == nil])
	// The prompt is usually there by now; it is waited for so the column is not empty.
	for time.Now().Before(deadline) {
		mu.Lock()
		p := prompt
		mu.Unlock()
		if p != 0 {
			m.RecordMs("console_prompt", p)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	m.Logf("sshd accepts %v, first login %v", accepts.Round(time.Millisecond), login.Round(time.Millisecond))
	return nil
}
