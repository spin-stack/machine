// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"crypto/ed25519"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"time"

	xssh "golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

// Key is what the probes log in with: the private half held here, the public half written into
// every overlay a login probe boots.
type Key struct {
	Signer xssh.Signer
	public []byte
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
func NewCID() uint32 { return uint32(100000 + rand.IntN(1<<20)) }

// VsockAccepts says whether something accepts a connection on cid:port now. The connect timeout
// is short because a machine not yet started is the common answer, and the default is two seconds
// of not knowing.
// mutate-lane: TestEveryProbeMeasuresATree
func VsockAccepts(cid, port uint32) bool {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return false
	}
	defer func() { _ = unix.Close(fd) }()
	tv := unix.NsecToTimeval((50 * time.Millisecond).Nanoseconds())
	_ = unix.SetsockoptTimeval(fd, unix.AF_VSOCK, unix.SO_VM_SOCKETS_CONNECT_TIMEOUT, &tv)
	return unix.Connect(fd, &unix.SockaddrVM{CID: cid, Port: port}) == nil
}

// Login runs `true` as spin over SSH on cid's port 22, offering only the key exchange kex, and says
// how long it took from the connect. The client offers mlkem768x25519-sha256 because it is the one
// key exchange the image's sshd accepts; the runners' OpenSSH 9.6 has no ML-KEM and is refused.
// mutate-lane: TestEveryProbeMeasuresATree
func (k *Key) Login(cid uint32, kex string) (time.Duration, error) {
	d, _, err := k.Run(cid, kex, "true")
	return d, err
}

// Run runs command as spin over SSH on cid's port 22 and returns how long it took from the
// connect and what it wrote.
// mutate-lane: TestEveryProbeMeasuresATree
func (k *Key) Run(cid uint32, kex, command string) (time.Duration, string, error) {
	t0 := time.Now()
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return 0, "", fmt.Errorf("opening a vsock socket: %w", err)
	}
	if err := unix.Connect(fd, &unix.SockaddrVM{CID: cid, Port: 22}); err != nil {
		_ = unix.Close(fd)
		return 0, "", fmt.Errorf("connecting to vsock %d:22: %w", cid, err)
	}
	conn := vsockConn{os.NewFile(uintptr(fd), "vsock")}
	defer func() { _ = conn.Close() }()
	c, chans, reqs, err := xssh.NewClientConn(conn, "vsock", &xssh.ClientConfig{
		User:            "spin",
		Auth:            []xssh.AuthMethod{xssh.PublicKeys(k.Signer)},
		HostKeyCallback: xssh.InsecureIgnoreHostKey(), // a fresh VM's keys, made in its boot
		Config:          xssh.Config{KeyExchanges: []string{kex}},
	})
	if err != nil {
		return 0, "", fmt.Errorf("ssh to vsock %d:22: %w", cid, err)
	}
	client := xssh.NewClient(c, chans, reqs)
	defer func() { _ = client.Close() }()
	s, err := client.NewSession()
	if err != nil {
		return 0, "", fmt.Errorf("opening a session on vsock %d:22: %w", cid, err)
	}
	defer func() { _ = s.Close() }()
	out, err := s.Output(command)
	if err != nil {
		return 0, "", fmt.Errorf("running %q on vsock %d:22: %w", command, cid, err)
	}
	return time.Since(t0), string(out), nil
}

// LoggedIn waits for sshd to accept on cid, then logs in, and returns how long after t0 the login
// was done. A machine that never accepts, or accepts and is never logged into, is a Miss.
// mutate-lane: TestEveryProbeMeasuresATree
func (k *Key) LoggedIn(cid uint32, t0 time.Time) (time.Duration, error) {
	deadline := t0.Add(60 * time.Second)
	for !VsockAccepts(cid, 22) {
		if time.Now().After(deadline) {
			return 0, Miss("sshd never accepted on vsock")
		}
		time.Sleep(2 * time.Millisecond)
	}
	var last error
	for time.Now().Before(deadline) {
		if _, last = k.Login(cid, xssh.KeyExchangeMLKEM768X25519); last == nil {
			return time.Since(t0), nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	return 0, Miss("sshd accepted but was never logged into: %v", last)
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
