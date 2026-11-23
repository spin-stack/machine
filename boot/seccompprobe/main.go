// SPDX-License-Identifier: Apache-2.0

// seccompprobe makes system calls under the seccomp filters of the unit that runs it, and prints
// what each one returned: boot:seccomp runs it as ExecStartPost= of journald, logind and user@,
// which the executor starts with the unit's own sandbox, and compares the table with the one
// boot/testdata/seccomp-table.txt keeps (#637). The kernel has no IA32 emulation and no x32 ABI,
// so a native call is all a process here can make; the x32 numbers are native calls with
// __X32_SYSCALL_BIT, which seccomp still sees.
//
// Every call is made in a child, which a filter may kill. The calls a unit's settings decide by
// argument (RestrictAddressFamilies=, RestrictNamespaces=, RestrictRealtime=, RestrictSUIDSGID=,
// LockPersonality=) are made first, with the unit's capabilities; the rest are made with every
// argument zero and no capabilities, so that one a filter allows does nothing.
//
// Diagnostic input, never part of a release.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

type call struct {
	nr   uint64
	args [6]uint64
	caps bool // made with the unit's capabilities
}

func (c call) String() string {
	s := strconv.FormatUint(c.nr, 10)
	for _, a := range c.args {
		s += " " + strconv.FormatUint(a, 16)
	}
	return s
}

const x32Bit = 0x40000000

// sysvIPC are the System V IPC calls, left out of the sweep: with every argument zero they make
// and use the system's queues, semaphores and segments, whose ids then depend on what ran
// before, and the three units' probes run beside each other.
var sysvIPC = map[uint64]bool{
	unix.SYS_SHMGET: true, unix.SYS_SHMAT: true, unix.SYS_SHMCTL: true, unix.SYS_SHMDT: true,
	unix.SYS_SEMGET: true, unix.SYS_SEMOP: true, unix.SYS_SEMCTL: true, unix.SYS_SEMTIMEDOP: true,
	unix.SYS_MSGGET: true, unix.SYS_MSGSND: true, unix.SYS_MSGRCV: true, unix.SYS_MSGCTL: true,
}

func calls() []call {
	var cs []call
	arg := func(nr uint64, i int, v uint64) {
		c := call{nr: nr, caps: true}
		c.args[i] = v
		cs = append(cs, c)
	}
	for fam := uint64(0); fam < 46; fam++ {
		arg(unix.SYS_SOCKET, 0, fam)
		arg(unix.SYS_SOCKETPAIR, 0, fam)
	}
	for _, f := range []uint64{unix.CLONE_NEWNS, unix.CLONE_NEWUTS, unix.CLONE_NEWIPC,
		unix.CLONE_NEWUSER, unix.CLONE_NEWPID, unix.CLONE_NEWNET, unix.CLONE_NEWCGROUP, unix.CLONE_NEWTIME} {
		arg(unix.SYS_UNSHARE, 0, f)
		arg(unix.SYS_SETNS, 1, f)
	}
	for _, p := range []uint64{1, 2, 3, 5, 6} { // FIFO, RR, BATCH, IDLE, DEADLINE
		arg(unix.SYS_SCHED_SETSCHEDULER, 1, p)
	}
	for _, p := range []uint64{0, 8, 0x0008, 0x0400000, 0xffffffff} {
		arg(unix.SYS_PERSONALITY, 0, p)
	}
	for _, m := range []uint64{0o4755, 0o2755, 0o755} {
		arg(unix.SYS_CHMOD, 1, m)
		arg(unix.SYS_FCHMOD, 1, m)
		arg(unix.SYS_FCHMODAT, 2, m)
		arg(unix.SYS_MKDIR, 1, m)
		arg(unix.SYS_MKNOD, 1, m)
		arg(unix.SYS_CREAT, 1, m)
		c := call{nr: unix.SYS_OPENAT, caps: true}
		c.args[2], c.args[3] = unix.O_CREAT, m
		cs = append(cs, c)
	}
	for nr := uint64(0); nr < 470; nr++ {
		if !sysvIPC[nr] {
			cs = append(cs, call{nr: nr})
		}
	}
	for nr := uint64(0); nr < 548; nr++ {
		cs = append(cs, call{nr: x32Bit | nr})
	}
	return cs
}

func main() {
	if len(os.Args) == 3 && os.Args[1] == "child" {
		child(os.Args[2])
		return
	}
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: seccompprobe OUTPUT")
		os.Exit(2)
	}
	tmp := os.Args[1] + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	out := bufio.NewWriter(f)
	cs := calls()
	fmt.Fprintf(out, "filters %s\n", seccompFilters())
	for from := 0; from < len(cs); {
		from = run(out, cs, from)
	}
	if err := out.Flush(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := f.Close(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.Rename(tmp, os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func seccompFilters() string {
	b, _ := os.ReadFile("/proc/self/status")
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "Seccomp_filters:"); ok {
			return strings.TrimSpace(v)
		}
	}
	return "?"
}

// run starts a child at cs[from], prints each result it reports, and returns where the next
// child starts: after the call the child died in or stopped answering on.
func run(out io.Writer, cs []call, from int) int {
	r, w, err := os.Pipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "pipe: %v\n", err)
		os.Exit(1)
	}
	cmd := exec.Command("/proc/self/exe", "child", strconv.Itoa(from))
	cmd.ExtraFiles = []*os.File{w}
	cmd.Env = []string{"GOMAXPROCS=1", "GODEBUG=asyncpreemptoff=1"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "starting a child: %v\n", err)
		os.Exit(1)
	}
	_ = w.Close()
	lines := make(chan string)
	go func() {
		s := bufio.NewScanner(r)
		for s.Scan() {
			lines <- s.Text()
		}
		close(lines)
	}()
	next := from
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				err := cmd.Wait()
				if next < len(cs) {
					fmt.Fprintf(out, "%s = %s\n", cs[next], died(err))
					next++
				}
				return next
			}
			fmt.Fprintf(out, "%s = %s\n", cs[next], l)
			next++
		case <-time.After(300 * time.Millisecond):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
			for range lines {
			}
			fmt.Fprintf(out, "%s = no answer\n", cs[next])
			return next + 1
		}
	}
}

func died(err error) string {
	if ee, ok := err.(*exec.ExitError); ok {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return "killed " + ws.Signal().String()
		}
		return fmt.Sprintf("exited %d", ee.ExitCode())
	}
	return "exited 0"
}

func child(start string) {
	runtime.LockOSThread()
	from, err := strconv.Atoi(start)
	if err != nil {
		os.Exit(100)
	}
	pid, _, _ := syscall.RawSyscall(unix.SYS_GETPID, 0, 0, 0)
	cs := calls()
	dropped := false
	buf := make([]byte, 0, 32)
	for _, c := range cs[from:] {
		if !c.caps && !dropped {
			dropCaps()
			dropped = true
		}
		r, _, e := syscall.RawSyscall6(uintptr(c.nr), uintptr(c.args[0]), uintptr(c.args[1]),
			uintptr(c.args[2]), uintptr(c.args[3]), uintptr(c.args[4]), uintptr(c.args[5]))
		if p, _, _ := syscall.RawSyscall(unix.SYS_GETPID, 0, 0, 0); p != pid {
			syscall.RawSyscall(unix.SYS_EXIT_GROUP, 0, 0, 0)
		}
		buf = buf[:0]
		if e != 0 {
			buf = append(buf, "errno "...)
			buf = strconv.AppendInt(buf, int64(e), 10)
		} else {
			buf = append(buf, "ok"...)
			_ = r
		}
		buf = append(buf, '\n')
		syscall.RawSyscall(unix.SYS_WRITE, 3, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	}
	syscall.RawSyscall(unix.SYS_EXIT_GROUP, 0, 0, 0)
}

// dropCaps empties the child's capability sets, so a call a filter allows with every argument
// zero cannot change anything but the child.
func dropCaps() {
	hdr := struct {
		version uint32
		pid     int32
	}{0x20080522, 0}
	var data [2]struct{ effective, permitted, inheritable uint32 }
	syscall.RawSyscall(unix.SYS_CAPSET, uintptr(unsafe.Pointer(&hdr)), uintptr(unsafe.Pointer(&data[0])), 0)
}
