// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"crypto/ed25519"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"strings"
	"time"

	xssh "golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

// Key is what the probes log in with: the private half held here, the public half written into
// every overlay a login probe boots.
type Key struct {
	Signer xssh.Signer
	public []byte
	// SpinsPath is a login the way spin makes one (the login option's "spin", the default) rather
	// than the lane's: see spinsPath.
	SpinsPath bool
}

// NewKey makes a key for one experiment.
func NewKey() (*Key, error) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, fmt.Errorf("making a login key: %w", err)
	}
	signer, err := xssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, fmt.Errorf("making a login key: %w", err)
	}
	sshPub, err := xssh.NewPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("making a login key: %w", err)
	}
	return &Key{Signer: signer, public: xssh.MarshalAuthorizedKey(sshPub)}, nil
}

// User is who logs in: root on spin's path, whose certificates are issued for root (spin's
// cmd/cli/cmd/ssh.go; execserver's loginUser), and spin on the lane.
func (k *Key) User() string {
	if k.SpinsPath {
		return "root"
	}
	return "spin"
}

// spinsPath is the login as spin makes it, stood in for (spin-stack/spin at 1bd3414, 2026-11-20).
// spin turns systemd's vsock sshd off (systemd.ssh_auto=0, internal/runner/qemu/kernel_cmdline.go)
// and its supervisor forwards vsock to 127.0.0.1:22 (internal/guest/supervisor/forward), where the
// image's ssh.socket hands every connection to one long-running sshd, ssh.service. The supervisor
// lets SSH through once it has installed the user CA, and installing it runs
// `systemctl reload-or-restart ssh.service` (internal/guest/supervisor/handler/userca.go): sshd
// is running before the first connection reaches it, as it is here. socat stands for the
// forwarder, with TCP_NODELAY on the loopback side as Go's dialer sets it by default: without it,
// Nagle's algorithm against the delayed ACK held each login ~40 ms (laptop, 69 against 26 ms warm,
// 2026-11-20). The login is root's, through authorized_keys rather than spin's certificate.
//
// sshd_accepts on this path is the forwarder listening, which follows sshd's start; when spin's
// supervisor listens is its own and is not measured here.
var spinsPath = Edit{
	Mask: []string{"sshd-vsock.socket"},
	Files: map[string]string{
		"/etc/systemd/system/spin-lab-forward.service": `[Unit]
Description=Stands for spin's supervisor forwarding vsock port 22 to 127.0.0.1:22
Wants=ssh.service
After=ssh.service
[Service]
ExecStart=/usr/bin/socat VSOCK-LISTEN:22,fork,reuseaddr TCP:127.0.0.1:22,nodelay
`,
	},
	Links: map[string]string{
		"/etc/systemd/system/multi-user.target.wants/spin-lab-forward.service": "/etc/systemd/system/spin-lab-forward.service",
	},
	Setup: `test -x "$MNT/usr/bin/socat" && test -e "$MNT/usr/lib/systemd/system/ssh.socket"
install -d -m 700 "$MNT/root/.ssh"
cp "$MNT/home/spin/.ssh/authorized_keys" "$MNT/root/.ssh/authorized_keys"
chmod 600 "$MNT/root/.ssh/authorized_keys"
`,
}

// GettyEcho is the serial getty replaced by an echo, for a boot whose prompt is timed: agetty
// sleeps a second before it prints (util-linux's "let the line settle", strace'd 2026-09-10), which
// nothing configures away and which is larger than anything compared. An empty ExecStart= first:
// a drop-in appends otherwise, and the line would run two gettys.
const GettyEcho = "ExecStart=\nExecStart=-/bin/sh -c 'echo SPIN-READY-login: ; sleep infinity'\n"

// GettyDropin is body as a drop-in of the serial getty's instance, which beats the image's own
// drop-in on the template.
func GettyDropin(body string) map[string]string {
	return map[string]string{
		"/etc/systemd/system/serial-getty@ttyS0.service.d/zz-bench.conf": "[Service]\n" + body,
	}
}

// Edits are what a boot logged into with k needs in its overlay: Edit, and on spin's path
// spinsPath after it.
func (k *Key) Edits() []Edit {
	if k.SpinsPath {
		return []Edit{k.Edit(), spinsPath}
	}
	return []Edit{k.Edit()}
}

// Edit is what a boot logged into with k needs in its overlay: k in spin's authorized_keys,
// owned by spin (sshd's StrictModes refuses one it cannot trust, which reads as a login that never
// comes), the echo getty, and sshd@ ordered after the image's host key generation - which the image
// carries since 2026-11-12, and is written here as well because the lab boots published releases,
// and one from before it cannot be logged into over vsock at all.
func (k *Key) Edit() Edit {
	files := GettyDropin(GettyEcho)
	files["/etc/systemd/system/sshd@.service.d/keygen.conf"] = "[Unit]\nWants=spin-machine-sshd-keygen.service\nAfter=spin-machine-sshd-keygen.service\n"
	return Edit{Files: files, Setup: `ids=$(awk -F: '$1=="spin"{print $3":"$4}' "$MNT/etc/passwd"); test -n "$ids"
install -d -m 700 -o "${ids%:*}" -g "${ids#*:}" "$MNT/home/spin/.ssh"
printf '%s' '` + string(k.public) + `' > "$MNT/home/spin/.ssh/authorized_keys"
chown "${ids%:*}:${ids#*:}" "$MNT/home/spin/.ssh/authorized_keys"
chmod 600 "$MNT/home/spin/.ssh/authorized_keys"
`}
}

// NewCID is a vsock context id for one machine. They are the host's: two machines with one fail
// to start, so this is drawn from a range nothing else here uses.
func NewCID() uint32 {
	return uint32(100000 + rand.IntN(1<<20)) // mutate-exempt: where the range starts; any start clear of the host's own context ids (0-2) and its other machines' holds
}

// DialVsock connects to cid's port 22. The connect timeout is short because a machine not yet
// started is the common answer, and the default is two seconds of not knowing.
// mutate-lane: TestEveryProbeMeasuresATree
func DialVsock(cid uint32) (net.Conn, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("opening a vsock socket: %w", err)
	}
	tv := unix.NsecToTimeval((50 * time.Millisecond).Nanoseconds())
	_ = unix.SetsockoptTimeval(fd, unix.AF_VSOCK, unix.SO_VM_SOCKETS_CONNECT_TIMEOUT, &tv)
	if err := unix.Connect(fd, &unix.SockaddrVM{CID: cid, Port: 22}); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("connecting to vsock %d:22: %w", cid, err)
	}
	// Non-blocking, so that os.NewFile puts it on Go's poller: a blocking descriptor is closed
	// only once nothing reads it, and the SSH client's reader blocks on it until the guest sends
	// something, which it never does. Every login stayed open in the guest - its forwarder's
	// child, sshd-session and logind's session - and a checkpoint saved after a login carried
	// it, to be torn down on the guest's one vCPU beside the restored login (laptop, 2026-11-21).
	if err := unix.SetNonblock(fd, true); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("making the vsock socket non-blocking: %w", err)
	}
	return vsockConn{os.NewFile(uintptr(fd), "vsock")}, nil
}

// Login runs `true` as k.User over SSH on cid's port 22, offering only the key exchange kex, and says
// how long it took from the connect. The client offers mlkem768x25519-sha256 because it is the one
// key exchange the image's sshd accepts; the runners' OpenSSH 9.6 has no ML-KEM and is refused.
// mutate-lane: TestEveryProbeMeasuresATree
func (k *Key) Login(cid uint32, kex string) (time.Duration, error) {
	d, _, err := k.Run(cid, kex, "true")
	return d, err
}

// LoginOn is Login over conn, a connection to cid's port 22 already made, which it closes.
// mutate-lane: TestEveryProbeMeasuresATree
func (k *Key) LoginOn(conn net.Conn, cid uint32, kex string) error {
	_, err := k.session(conn, cid, kex, "true")
	return err
}

// Run runs command as k.User over SSH on cid's port 22 and returns how long it took from the
// connect and what it wrote.
// mutate-lane: TestEveryProbeMeasuresATree
func (k *Key) Run(cid uint32, kex, command string) (time.Duration, string, error) {
	return k.run(cid, kex, command, func(c net.Conn) net.Conn { return c })
}

// run is Run over the connection wrap makes of the vsock one.
// mutate-lane: TestEveryProbeMeasuresATree
func (k *Key) run(cid uint32, kex, command string, wrap func(net.Conn) net.Conn) (time.Duration, string, error) {
	t0 := time.Now()
	conn, err := DialVsock(cid)
	if err != nil {
		return 0, "", err
	}
	out, err := k.session(wrap(conn), cid, kex, command)
	if err != nil {
		return 0, "", err
	}
	return time.Since(t0), out, nil
}

// session runs command as k.User over SSH on conn, a connection to cid's port 22, and closes it.
// mutate-lane: TestEveryProbeMeasuresATree
func (k *Key) session(conn net.Conn, cid uint32, kex, command string) (string, error) {
	defer func() { _ = conn.Close() }()
	c, chans, reqs, err := xssh.NewClientConn(conn, "vsock", k.config(kex))
	if err != nil {
		return "", fmt.Errorf("ssh to vsock %d:22: %w", cid, err)
	}
	client := xssh.NewClient(c, chans, reqs)
	defer func() { _ = client.Close() }()
	s, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("opening a session on vsock %d:22: %w", cid, err)
	}
	defer func() { _ = s.Close() }()
	out, err := s.Output(command)
	if err != nil {
		return "", fmt.Errorf("running %q on vsock %d:22: %w: %s", command, cid, err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// config is the client's configuration for a login as k.User, offering only the key exchange kex.
func (k *Key) config(kex string) *xssh.ClientConfig {
	return &xssh.ClientConfig{
		User:            k.User(),
		Auth:            []xssh.AuthMethod{xssh.PublicKeys(k.Signer)},
		HostKeyCallback: xssh.InsecureIgnoreHostKey(), // a fresh VM's keys, made in its boot
		Config:          xssh.Config{KeyExchanges: []string{kex}},
	}
}

// LoggedIn logs in on mc's cid until a login is done, and returns how long after t0 that was. A
// machine never logged into is a Miss; one that exits first says why at once (Machine.Exited), not
// when the wait runs out.
//
// It does not ask whether something accepts first and close that connection. On spin's path that
// connection reaches the forwarder, which opens one to sshd, and sshd starts a session for it on
// the guest's one vCPU beside the login measured. spin makes no such connection: its runner opens
// the user's, once. Without it, the restored login read −3.6 ms [−7.3, −1.7] (bench, spin's shape,
// 30 a side, lab 44977850524).
// mutate-lane: TestEveryProbeMeasuresATree
func (k *Key) LoggedIn(mc *Machine, cid uint32, t0 time.Time) (time.Duration, error) {
	deadline := t0.Add(60 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		if _, last = k.Login(cid, xssh.KeyExchangeMLKEM768X25519); last == nil {
			return time.Since(t0), nil
		}
		if err := mc.Exited(); err != nil {
			return 0, err
		}
		time.Sleep(2 * time.Millisecond)
	}
	return 0, Miss("never logged into on vsock: %v", last)
}

// vsockConn is a connected vsock socket as the net.Conn x/crypto/ssh takes: net.FileConn knows no
// AF_VSOCK address, and nothing here asks for one.
type vsockConn struct{ *os.File }

type vsockAddr struct{}

func (vsockAddr) Network() string { return "vsock" }
func (vsockAddr) String() string  { return "vsock" }

func (vsockConn) LocalAddr() net.Addr  { return vsockAddr{} }
func (vsockConn) RemoteAddr() net.Addr { return vsockAddr{} }

// shortDir is a directory for a machine's sockets: a Unix socket's path fits in 108 bytes, and a
// scratch directory on a runner's disk need not leave room for it (AGENTS.md).
func shortDir() (string, error) {
	d, err := os.MkdirTemp("/tmp", "lab")
	if err != nil {
		return "", fmt.Errorf("making a directory for sockets: %w", err)
	}
	return d, nil
}
