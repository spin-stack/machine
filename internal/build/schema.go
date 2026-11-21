// SPDX-License-Identifier: Apache-2.0

package build

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spin-stack/machine/internal/qmpdef"
	"github.com/spin-stack/machine/qmp"
)

// querySchema is what a QEMU says of its QMP: every command, event and type, which
// qmpdef.Check holds the module's definitions to.
var querySchema = qmpdef.Define[qmp.None, json.RawMessage]("query-qmp-schema")

// checkQEMU is what a QEMU tree is held to once it is built or when it is verified: VerifyQEMU,
// and then whether the binary knows every command and event the module defines, as the module
// defines them, so that a bump that renames, drops or deprecates one fails here and not in a
// machine weeks later. The TCG binary is the one asked, because it is the one that starts on a
// host without /dev/kvm; both are built from one source, and so have one schema.
func checkQEMU(o Options) error {
	if err := VerifyQEMU(o); err != nil {
		return err
	}
	tcg := filepath.Join(o.Out, "bin", "qemu-system-x86_64-tcg")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	schema, err := QMPSchema(ctx, tcg)
	if err != nil {
		return err
	}
	if err := qmpdef.Check(schema); err != nil {
		return fmt.Errorf("%s does not know the module's QMP as it is defined:\n%w", filepath.Base(tcg), err)
	}
	return nil
}

// QMPSchema is qemu's answer to query-qmp-schema. qemu is started with no machine and its monitor
// on one end of a socket pair: there is nothing to listen on and nothing for a guest to run, and
// it answers in tens of milliseconds.
func QMPSchema(ctx context.Context, qemu string) (json.RawMessage, error) {
	// mutate-exempt: an AF_UNIX socket takes protocol 0 or PF_UNIX, which is 1, for the same socket
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("making a socket pair for %s's monitor: %w", qemu, err)
	}
	ours, theirs := os.NewFile(uintptr(fds[0]), "qmp"), os.NewFile(uintptr(fds[1]), "qmp")
	conn, err := net.FileConn(ours)
	_ = ours.Close()
	if err != nil {
		_ = theirs.Close()
		return nil, fmt.Errorf("opening %s's monitor: %w", qemu, err)
	}
	var said strings.Builder
	cmd := exec.Command(qemu, "-machine", "none", "-nodefaults", "-display", "none", //nolint:gosec // the QEMU this build made
		"-chardev", "socket,id=qmp,fd=3", "-object", "monitor-qmp,id=mon,chardev=qmp")
	cmd.ExtraFiles = []*os.File{theirs}
	cmd.Stdout, cmd.Stderr = &said, &said
	err = cmd.Start()
	_ = theirs.Close() // qemu's now: ours closed, its exit ends the connection
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("starting %s: %w", qemu, err)
	}
	schema, err := ask(ctx, conn.(*net.UnixConn))
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if err != nil {
		return nil, fmt.Errorf("asking %s for its QMP schema: %w: %s", qemu, err, strings.TrimSpace(said.String()))
	}
	return schema, nil
}

func ask(ctx context.Context, conn *net.UnixConn) (json.RawMessage, error) {
	c, err := qmp.New(ctx, qmp.Unix(conn), qmp.Observer{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	return qmp.Do(ctx, c, querySchema, qmp.None{})
}
