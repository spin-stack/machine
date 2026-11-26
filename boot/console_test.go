// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spin-stack/machine/machine"
)

// checkOnConsole boots a disposable 1 GiB guest of rel on root, whose unit prints ok or failed on
// the serial console, and fails the test unless ok comes first. It returns what the console said.
// Under TCG where there is no /dev/kvm: what it checks is behaviour, not time.
func checkOnConsole(t *testing.T, rel *machine.Release, root *rawRoot, ok, failed string) string {
	t.Helper()
	return checkGuest(t, rel, root, ok, failed, func(*machine.Spec) {}, func(*machine.QEMU, string) {})
}

// checkGuest is checkOnConsole for a check the host takes part in: shape changes the machine
// before it starts, and said is given the machine and the console so far each time the guest has
// written to it.
func checkGuest(t *testing.T, rel *machine.Release, root *rawRoot, ok, failed string, shape func(*machine.Spec), said func(m *machine.QEMU, console string)) string {
	t.Helper()
	return checkGuestFor(t, 2*time.Minute, rel, root, ok, failed, shape, said)
}

// checkGuestFor is checkGuest for a check that takes longer than two minutes, with more disks
// beside root, each at its serial.
func checkGuestFor(t *testing.T, limit time.Duration, rel *machine.Release, root *rawRoot, ok, failed string, shape func(*machine.Spec), said func(m *machine.QEMU, console string), more ...machine.DiskPath) string {
	t.Helper()
	accel := "kvm"
	if _, err := os.Stat("/dev/kvm"); err != nil {
		accel = "tcg"
		t.Log("TCG: checking functionality only")
	}
	spec, err := rel.Spec(accel)
	if err != nil {
		t.Fatal(err)
	}
	spec.BootCPUs = 2
	spec.Memory.BaseMB = 1024
	spec.Places = []machine.Place{{Serial: "root"}}
	c := machine.DefaultCmdline()
	c.Root = "/dev/vda"
	c.Init = "/sbin/init"
	spec.Cmdline = c
	for _, m := range more {
		spec.Places = append(spec.Places, machine.Place{Serial: m.Serial})
	}
	shape(&spec)
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	console := newScreen()
	p, err := machine.Start(ctx, spec, machine.StartOptions{Console: console})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Kill()
	d, err := machine.OpenDisk(machine.DiskPath{Serial: "root", Path: root.path, Format: "raw"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	disks := []machine.Disk{d}
	for _, m := range more {
		x, err := machine.OpenDisk(m)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = x.Close() }()
		disks = append(disks, x)
	}
	if err := p.Boot(ctx, disks...); err != nil {
		t.Fatalf("booting: %v", err)
	}
	for {
		select {
		case <-console.news:
		case <-p.Exited():
		case <-ctx.Done():
		}
		text := console.String()
		if strings.Contains(text, failed) || ctx.Err() != nil {
			t.Fatalf("guest check failed: %v\n%s\nQEMU %s", ctx.Err(), text, p.Kill())
		}
		// Before the end is seen, so what the guest printed with its last line is still answered
		// by a machine that runs.
		said(p.QEMU, text)
		if strings.Contains(text, ok) {
			return text
		}
		select {
		case <-p.Exited():
			t.Fatalf("guest check failed: QEMU %s\n%s", p.Wait(), text)
		default:
		}
	}
}

// oneshotAtBoot has root run script as a oneshot unit three seconds after boot, its output on
// the console.
func oneshotAtBoot(t *testing.T, root *rawRoot, name, script, env string) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", script))
	if err != nil {
		t.Fatal(err)
	}
	root.write("/"+script, string(content))
	root.write("/etc/systemd/system/"+name+".service", `[Unit]
Description=A check in a disposable guest
After=multi-user.target
[Service]
Type=oneshot
`+env+`
ExecStart=/bin/sh /`+script+`
StandardOutput=journal+console
StandardError=journal+console
`)
	root.write("/etc/systemd/system/"+name+".timer", `[Timer]
OnBootSec=3s
AccuracySec=100ms
`)
	root.link("/etc/systemd/system/timers.target.wants/"+name+".timer", "/etc/systemd/system/"+name+".timer")
}
