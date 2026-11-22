// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	xssh "golang.org/x/crypto/ssh"
)

// setUp makes e in the directory root as EditOverlay makes it in a mounted overlay, without sudo:
// its files written, then its setup run with MNT set.
func setUp(t *testing.T, e Edit, root string) error {
	t.Helper()
	for path, body := range e.Files {
		write(t, filepath.Join(root, path), []byte(body))
	}
	cmd := exec.Command("sh", "-euc", e.Setup)
	cmd.Env = append(os.Environ(), "MNT="+root)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}

// spin's handoff puts a fresh Ed25519 key where the drop-in tells sshd to read it, and the image's
// keygen, as this checkout builds it, does not run when that key is there.
func TestSpinsHandoffKeyIsTheOneTheKeygenStepsAsideFor(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("no ssh-keygen here")
	}
	root := t.TempDir()
	if err := setUp(t, spinHandoffEdit, root); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "usr/local/lib/spin-lab/ssh_host_ed25519_key"))
	if err != nil {
		t.Fatal(err)
	}
	signer, err := xssh.ParsePrivateKey(b)
	if err != nil || signer.PublicKey().Type() != xssh.KeyAlgoED25519 {
		t.Fatalf("the handed-over key: %v, %v", signer, err)
	}
	if fi, err := os.Stat(filepath.Join(root, spinHandoffInit)); err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("the handoff's init is not executable: %v, %v", fi, err)
	}
	const key = "/run/spin/ssh/ssh_host_ed25519_key"
	init := spinHandoffEdit.Files[spinHandoffInit]
	if dropIn := spinHandoffEdit.Files["/etc/ssh/sshd_config.d/00-spin-host-key.conf"]; dropIn != "HostKey "+key+"\n" ||
		!strings.Contains(init, "install -m 0600 /usr/local/lib/spin-lab/ssh_host_ed25519_key "+key+"\n") ||
		!strings.HasSuffix(init, "exec /sbin/init\n") {
		t.Errorf("the handoff places its key somewhere sshd is not told to read: %q, %q", dropIn, init)
	}

	e, err := keygenEdit(checkoutRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	unit := e.Files["/etc/systemd/system/spin-machine-sshd-keygen.service"]
	if !strings.Contains(unit, "\nConditionPathExists=!"+key+"\n") {
		t.Errorf("the image's keygen runs whether or not spin handed over its key:\n%s", unit)
	}
	release := t.TempDir()
	write(t, filepath.Join(release, "usr/lib/systemd/system/spin-machine-sshd-keygen.service"), []byte("[Unit]\n"))
	if err := setUp(t, e, release); err != nil {
		t.Errorf("a release with another keygen unit: %v", err)
	}
	write(t, filepath.Join(release, "usr/lib/systemd/system/spin-machine-sshd-keygen.service"), []byte(unit))
	if err := setUp(t, e, release); err == nil || !strings.Contains(err.Error(), "is this checkout's") {
		t.Errorf("a release whose keygen unit is this checkout's is measured twice: %v", err)
	}
}

// carry-keygen leaves the keys to ssh.service on a release from before #515, which wants the unit
// from ssh.service, and on one from after it, which does not.
func TestCarryKeygenLeavesTheKeysToSSHServiceWhicheverReleaseItEdits(t *testing.T) {
	const unit = "/usr/lib/systemd/system/spin-machine-sshd-keygen.service"
	for name, wanted := range map[string]bool{"before #515": true, "after #515": false} {
		root := t.TempDir()
		sysinit := filepath.Join(root, "etc/systemd/system/sysinit.target.wants/spin-machine-sshd-keygen.service")
		ssh := filepath.Join(root, "etc/systemd/system/ssh.service.wants/spin-machine-sshd-keygen.service")
		links := []string{sysinit}
		if wanted {
			links = append(links, ssh)
		}
		for _, l := range links {
			if err := os.MkdirAll(filepath.Dir(l), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(unit, l); err != nil {
				t.Fatal(err)
			}
		}
		if err := setUp(t, carryKeygenEdit, root); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := os.Lstat(sysinit); !os.IsNotExist(err) {
			t.Errorf("%s: sysinit.target still wants the keygen: %v", name, err)
		}
		if got, err := os.Readlink(ssh); err != nil || got != unit {
			t.Errorf("%s: ssh.service wants %q, %v", name, got, err)
		}
	}
	if err := setUp(t, keygenUnwantedEdit, t.TempDir()); err == nil {
		t.Error("keygen-unwanted edited a release with no ssh.service want")
	}
}
