// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spin-stack/spin-machine/machine"
)

// hostBaseline is the newest named CPU model this host can run a machine of with the tree at dir:
// its QEMU, started held with no disk, asked which models it can run here with nothing missing
// (machine.Models), and machine.Baseline over this one host.
//
// On the host that measures, not where the experiment is written: the bench fleet is nested, and
// its L0 hides features the silicon has - la57, TSX, fsrm - so a model chosen by the CPU's name
// (GraniteRapids-v1 for a Xeon 6) is one QEMU refuses there, which cost a lab run 50 minutes of
// boots timing out (44657973748, 2026-11-20).
func hostBaseline(dir string) (string, error) {
	socks, err := shortDir()
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(socks) }()
	qmp := filepath.Join(socks, "cpu.qmp")
	cmd := exec.Command(filepath.Join(dir, "bin", "spin-machine"), "boot", "--release", dir, "--held", "--qmp", qmp,
		"--disk", "-", "--memory", "256", "--console", "none")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("starting a held machine to ask which CPU models this host runs: %w", err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	defer func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); <-exited }()
	// Until the monitor is there, or the machine is gone saying why: a refusal is not waited out.
	for deadline := time.Now().Add(30 * time.Second); !fileExists(qmp); time.Sleep(5 * time.Millisecond) {
		select {
		case <-exited:
			return "", fmt.Errorf("the held machine asked which CPU models this host runs exited: %s", strings.TrimSpace(stderr.String()))
		default:
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("the held machine asked which CPU models this host runs opened no monitor in 30s: %s", strings.TrimSpace(stderr.String()))
		}
	}
	return baselineOn(qmp)
}

// baselineOn asks the machine on the monitor at qmp which named models it can run, and picks this
// one host's baseline among them.
func baselineOn(qmp string) (string, error) {
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: qmp, Net: "unix"})
	if err != nil {
		return "", fmt.Errorf("asking which CPU models this host runs: %w", err)
	}
	m, err := machine.Attach(conn)
	if err != nil {
		return "", fmt.Errorf("asking which CPU models this host runs: %w", err)
	}
	defer func() { _ = m.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	models, err := m.Models(ctx)
	if err != nil {
		return "", err
	}
	best, err := machine.Baseline([][]machine.Model{models})
	if err != nil {
		return "", fmt.Errorf("choosing this host's CPU model: %w", err)
	}
	return best.Name, nil
}
