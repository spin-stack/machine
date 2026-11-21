// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spin-stack/machine/machine"
)

// serve is the scripted monitor: its greeting, then one reply per request read. It
// returns what it was asked once the client has closed the connection, and an error
// if the client is still holding it a second after the script ran out.
func serve(c net.Conn, greeting string, replies []string) ([]qmpRequest, error) {
	defer func() { _ = c.Close() }()
	if _, err := io.WriteString(c, greeting+"\n"); err != nil {
		return nil, err
	}
	var asked []qmpRequest
	r := bufio.NewReader(c)
	for _, reply := range replies {
		line, err := r.ReadString('\n')
		if err != nil {
			return asked, err
		}
		var req qmpRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			return asked, err
		}
		asked = append(asked, req)
		// The reply's last line answers the request, by its id; any lines before it are events.
		end := strings.LastIndexByte(reply, '}')
		if _, err := io.WriteString(c, reply[:end]+`, "id": `+string(req.ID)+reply[end:]+"\n"); err != nil {
			return asked, err
		}
	}
	// A pipe the client has already closed refuses a deadline with ErrClosedPipe,
	// which is the answer being waited for.
	if err := c.SetReadDeadline(time.Now().Add(time.Second)); errors.Is(err, io.ErrClosedPipe) {
		return asked, nil
	} else if err != nil {
		return asked, err
	}
	if _, err := r.ReadByte(); !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
		return asked, errors.New("the client kept the monitor after the conversation ended")
	}
	return asked, nil
}

// How a command ends. Only main ends the process: a command that exited on its own
// took the test binary with it, and skipped whatever its caller would have done next.
func TestHowACommandEnds(t *testing.T) {
	for _, tc := range []struct {
		name string
		do   func() error
		// is reports whether the error is the one this row expects.
		is func(error) bool
	}{{
		name: "help is asked for, not a failure",
		do:   func() error { return run([]string{"save", "-h"}) },
		is:   func(err error) bool { return errors.Is(err, flag.ErrHelp) },
	}, {
		name: "an unknown command is an error",
		do:   func() error { return run([]string{"frobnicate"}) },
		is:   func(err error) bool { return err != nil && !errors.Is(err, flag.ErrHelp) },
	}, {
		// Returned once, naming the command, and not also printed by the flag package
		// under a usage listing.
		name: "a flag nobody defined names its command",
		do:   func() error { return run([]string{"save", "--nope"}) },
		is: func(err error) bool {
			return err != nil && strings.HasPrefix(err.Error(), "save: flag provided but not defined")
		},
	}, {
		// Refused before a monitor is dialled: the socket here does not exist, so an
		// error about it would mean the serial was never looked at.
		name: "a detach that names no disk",
		do:   func() error { return run([]string{"detach", "--qmp", "/nonexistent"}) },
		is:   func(err error) bool { return err != nil && strings.Contains(err.Error(), "--disk-serial are required") },
	}, {
		name: "a held machine with no monitor to boot it through",
		do:   func() error { return boot(machine.Spec{}, machineFlags{held: true}, nil) },
		is:   func(err error) bool { return err != nil && strings.Contains(err.Error(), "none was given") },
	}} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.do(); !tc.is(err) {
				t.Errorf("returned %v (%T)", err, err)
			}
		})
	}
}

// boot becomes QEMU. Run as a child of this test, because a process that execs is not
// there afterwards to report: the child's pid is the one QEMU writes down, and its exit
// status is QEMU's, with nothing in between to lose either.
func TestBootBecomesQEMU(t *testing.T) {
	if qemu := os.Getenv("SPIN_MACHINE_TEST_QEMU"); qemu != "" {
		dir := filepath.Dir(qemu)
		// Held, so that nothing is started beside QEMU to boot it.
		err := boot(machine.Spec{QEMU: qemu, Kernel: filepath.Join(dir, "bzImage"), Firmware: dir, BootCPUs: 1,
			Memory: machine.Memory{BaseMB: 64}, Monitors: []machine.Monitor{{Socket: "/q"}}},
			machineFlags{held: true, qmp: "/q"}, nil)
		// Reached only if the exec failed.
		fmt.Fprintln(os.Stderr, err)
		os.Exit(100)
	}

	dir := t.TempDir()
	kernelIn(t, dir)
	qemu := filepath.Join(dir, "qemu")
	pidFile := filepath.Join(dir, "pid")
	script := "#!/bin/sh\necho $$ > " + pidFile + "\nexit 7\n"
	if err := os.WriteFile(qemu, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestBootBecomesQEMU$")
	cmd.Env = append(os.Environ(), "SPIN_MACHINE_TEST_QEMU="+qemu)
	err := cmd.Run()

	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatalf("boot ended with %v, want QEMU's own exit status 7", err)
	}
	pid, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(pid)), strconv.Itoa(cmd.Process.Pid); got != want {
		t.Errorf("QEMU ran as pid %s and spin-machine as %s: a child, not an exec", got, want)
	}
}

type qmpRequest struct {
	Execute   string          `json:"execute"`
	Arguments map[string]any  `json:"arguments"`
	ID        json.RawMessage `json:"id"`
}
