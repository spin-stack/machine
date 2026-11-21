// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// qmpCommand is one QMP command as QEMU's trace shows it. QMP's I/O runs on the monitor's own
// thread, which reads a command and queues it whenever it arrives; QEMU's main loop takes it from
// the queue, runs it, and answers. Arrived to Taken is the main loop busy elsewhere (still making
// the machine, or another monitor's command), Taken to Answered is the command itself, and the
// answer to the next command's arrival is the client's turn.
type qmpCommand struct {
	Name     string // as the trace names it: migrate_incoming, not migrate-incoming
	Monitor  string // the monitor's address in QEMU, which tells two connections apart
	Arrived  time.Time
	Taken    time.Time
	Answered time.Time
}

// qmpTrace is what a trace of QMP and the run state says about one QEMU.
type qmpTrace struct {
	Commands []qmpCommand // in the order the main loop answered them
	Running  time.Time    // the run state first set to running: the guest let go
}

// qmpTraceEvents are the trace events parseQMPTrace reads. qmp_enter_* names each command; the
// monitor's events place it in time.
const qmpTraceEvents = `qmp_enter_*
runstate_set
monitor_qmp_in_band_enqueue
monitor_qmp_in_band_dequeue
monitor_qmp_respond
`

// parseQMPTrace reads a QEMU trace written with the log backend and -msg timestamp=on: one event
// a line, its host time first. Lines it does not know, or cannot read the time of, are skipped.
func parseQMPTrace(trace string) qmpTrace {
	var t qmpTrace
	type queued struct {
		monitor string
		at      time.Time
	}
	pending := map[string]queued{} // a queued request's address, until the main loop takes it
	var cur *qmpCommand
	for line := range strings.Lines(trace) {
		stamp, rest, _ := strings.Cut(strings.TrimSpace(line), " ")
		event, args, _ := strings.Cut(rest, " ")
		at, err := time.Parse("2006-01-02T15:04:05.000000Z", stamp)
		if err != nil {
			continue
		}
		var req, mon string
		switch {
		case event == "monitor_qmp_in_band_enqueue":
			if _, err := fmt.Sscanf(args, "%s mon %s", &req, &mon); err == nil {
				pending[req] = queued{monitor: mon, at: at}
			}
		case event == "monitor_qmp_in_band_dequeue":
			_, _ = fmt.Sscanf(args, "%s", &req)
			if q, ok := pending[req]; ok {
				delete(pending, req)
				cur = &qmpCommand{Monitor: q.monitor, Arrived: q.at, Taken: at}
			}
		case strings.HasPrefix(event, "qmp_enter_") && cur != nil && cur.Name == "":
			cur.Name = strings.TrimPrefix(event, "qmp_enter_")
		case event == "monitor_qmp_respond" && cur != nil && answers(args):
			// The main loop runs one command at a time, so the answer is cur's: another monitor's
			// greeting or event, sent from its own thread meanwhile, is not an answer.
			cur.Answered = at
			t.Commands = append(t.Commands, *cur)
			cur = nil
		case event == "runstate_set" && t.Running.IsZero() && strings.HasSuffix(args, "(running)"):
			t.Running = at
		}
	}
	return t
}

// answers is whether a monitor_qmp_respond's arguments are a command's answer, and not an event or the
// greeting, which the monitor sends the same way.
func answers(args string) bool {
	_, resp, ok := strings.Cut(args, "resp: ")
	return ok && (strings.HasPrefix(resp, `{"return"`) || strings.HasPrefix(resp, `{"error"`))
}

// conversation is the commands of the monitor that sent a command named name, in order, through
// the first such command and after it: one connection's whole exchange, which another monitor
// (the probe sizing the guest) may have interleaved with. Nil when no monitor sent one.
func (t qmpTrace) conversation(name string) []qmpCommand {
	monitor := ""
	for _, c := range t.Commands {
		if c.Name == name {
			monitor = c.Monitor
			break
		}
	}
	if monitor == "" {
		return nil
	}
	var out []qmpCommand
	for _, c := range t.Commands {
		if c.Monitor == monitor {
			out = append(out, c)
		}
	}
	return out
}

// qmpSpan is a stretch of one conversation the guest waits on: from its first command's taking to
// the given command, the commands in it, and how much of it the main loop sat waiting for the
// client's next command.
type qmpSpan struct {
	Took     time.Duration
	Commands int
	Client   time.Duration
}

// span is cs from its first command up to the first command named until: taken to answered when
// through, or to taken when not (migrate_incoming starts the load, which is not QMP). ok is false
// when cs has no such command.
func span(cs []qmpCommand, until string, through bool) (qmpSpan, bool) {
	for i, c := range cs {
		if c.Name != until {
			continue
		}
		end := c.Taken
		if through {
			end = c.Answered
		}
		s := qmpSpan{Took: end.Sub(cs[0].Taken), Commands: i + 1}
		for j := 1; j <= i; j++ {
			s.Client += cs[j].Arrived.Sub(cs[j-1].Answered)
		}
		return s, true
	}
	return qmpSpan{}, false
}

// tracedQEMU is a QEMU for m's tree that writes its QMP trace to dir/qemu-PID.log, and the boot
// flag that runs it. A shell execs the tree's QEMU with the trace, so the PID is the machine's
// (Machine.PID). The shell's exec is inside a cold boot's clock: a traced boot is for the split,
// and its total compares only with another traced boot.
func tracedQEMU(m *Measurement, dir string) (string, error) {
	events := filepath.Join(dir, "qmp-events")
	if err := os.WriteFile(events, []byte(qmpTraceEvents), 0o600); err != nil {
		return "", fmt.Errorf("writing the trace's events: %w", err)
	}
	qemu, err := filepath.EvalSymlinks(filepath.Join(m.Tree.Dir, "bin", "qemu-system-x86_64"))
	if err != nil {
		return "", fmt.Errorf("finding the tree's QEMU: %w", err)
	}
	wrapper := filepath.Join(dir, "qemu-traced")
	script := fmt.Sprintf("#!/bin/sh\nexec %s -msg timestamp=on -D %s/qemu-$$.log -trace events=%s \"$@\"\n", qemu, dir, events)
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil { // #nosec G306 -- a script this process runs
		return "", fmt.Errorf("writing the traced QEMU: %w", err)
	}
	return "--qemu=" + wrapper, nil
}

// readQMPTrace is the trace the traced QEMU of pid wrote in dir, once the machine has ended.
func readQMPTrace(dir string, pid int) (qmpTrace, error) {
	b, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("qemu-%d.log", pid)))
	if err != nil {
		return qmpTrace{}, fmt.Errorf("reading QEMU's trace: %w", err)
	}
	return parseQMPTrace(string(b)), nil
}
