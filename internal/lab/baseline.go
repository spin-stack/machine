// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spin-stack/machine/machine"
	"github.com/spin-stack/machine/qmp"
)

// hostBaseline is the newest named CPU model this host can run a machine of with the tree at dir:
// its QEMU, started held with no disk, asked which models it can run here with nothing missing
// (machine.QEMU.CPUModels), and machine.Baseline over this one host.
//
// On the host that measures, not where the experiment is written: the bench fleet is nested, and
// its L0 hides features the silicon has - la57, TSX, fsrm - so a model chosen by the CPU's name
// (GraniteRapids-v1 for a Xeon 6) is one QEMU refuses there, which cost a lab run 50 minutes of
// boots timing out (44657973748, 2026-11-20).
func hostBaseline(dir string) (string, error) {
	var best string
	err := onHeld(dir, nil, func(socket string) error {
		var err error
		best, err = baselineOn(socket)
		return err
	})
	return best, err
}

// onHeld starts the tree at dir's machine held, with no disk and the given boot flags, and hands
// ask its monitor's socket. The machine is killed once ask returns.
func onHeld(dir string, flags []string, ask func(socket string) error) error {
	socks, err := shortDir()
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(socks) }()
	qmp := filepath.Join(socks, "cpu.qmp")
	args := append([]string{"boot", "--release", dir, "--held", "--qmp", qmp,
		"--disk", "-", "--memory", "256", "--console", "none"}, flags...)
	cmd := exec.Command(filepath.Join(dir, "bin", "spin-machine"), args...) // #nosec G204 -- the tree's own binary
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting a held machine to ask about this host's CPU: %w", err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	defer func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); <-exited }()
	// Until the monitor is there, or the machine is gone saying why: a refusal is not waited out.
	for deadline := time.Now().Add(30 * time.Second); !fileExists(qmp); time.Sleep(5 * time.Millisecond) {
		select {
		case <-exited:
			return fmt.Errorf("the held machine asked about this host's CPU exited: %s", strings.TrimSpace(stderr.String()))
		default:
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the held machine asked about this host's CPU opened no monitor in 30s: %s", strings.TrimSpace(stderr.String()))
		}
	}
	return ask(qmp)
}

// baselineOn asks the machine on the monitor at socket which named models it can run, and picks this
// one host's baseline among them.
func baselineOn(socket string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	m, err := machine.Dial(ctx, socket, qmp.Observer{})
	if err != nil {
		return "", fmt.Errorf("asking which CPU models this host runs: %w", err)
	}
	defer func() { _ = m.Close() }()
	models, err := m.CPUModels(ctx)
	if err != nil {
		return "", err
	}
	best, err := machine.Baseline([][]machine.CPUModel{models})
	if err != nil {
		return "", fmt.Errorf("choosing this host's CPU model: %w", err)
	}
	return best.Name, nil
}
