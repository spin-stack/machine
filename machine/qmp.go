// SPDX-License-Identifier: Apache-2.0

package machine

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"time"

	"github.com/spin-stack/machine/qmp"
)

// qmpClient is the smallest QMP client the machine needs: one command at a time, a descriptor sent
// with a command where QEMU takes one (add-fd), and the one event anything here waits on noted
// when it arrives in between.
type qmpClient struct {
	c  *net.UnixConn
	in *bufio.Reader
	// removing is the device unplug last removed, and removed whether QEMU has reported it
	// gone. A DEVICE_DELETED for any other device is dropped: nothing can wait on it, and QEMU
	// is not trusted to send a bounded number of them.
	removing string
	removed  bool
}

// qmpError is QEMU refusing a command, in its own words.
type qmpError struct {
	Class string `json:"class"`
	Desc  string `json:"desc"`
}

func (e *qmpError) Error() string { return e.Class + ": " + e.Desc }

// handshakeTimeout bounds the greeting and qmp_capabilities: QEMU writes the greeting as soon as
// a client connects, so one that has not arrived in this long is a socket nobody serves.
const handshakeTimeout = 5 * time.Second

// maxQMPLine bounds one line from QEMU. A tenant who escapes the guest controls QEMU, and a line
// that never ends is otherwise memory the client grows until it is killed. The largest answers
// the machine reads are qom-list-get's in a checkpoint's record, 125 KB and 47 KB on QEMU
// 11.2.0-rc0, so 4 MiB is ample, and it is the bound spin's own client keeps.
const maxQMPLine = 4 << 20

func newQMP(c *net.UnixConn) (*qmpClient, error) {
	q := &qmpClient{c: c, in: bufio.NewReader(c)}
	ctx, cancel := context.WithTimeout(context.Background(), handshakeTimeout)
	defer cancel()
	release := q.bound(ctx)
	line, err := q.readLine()
	release()
	if err != nil {
		return nil, fmt.Errorf("reading QEMU's greeting: %w", cause(ctx, err))
	}
	var greeting struct {
		QMP json.RawMessage `json:"QMP"`
	}
	if err := json.Unmarshal(line, &greeting); err != nil || greeting.QMP == nil {
		return nil, fmt.Errorf("the monitor's first line is not QEMU's greeting: %q", line)
	}
	if err := q.run(ctx, "qmp_capabilities", nil, nil); err != nil {
		return nil, err
	}
	return q, nil
}

// bound ends whatever read or write of the monitor is waiting when ctx ends, by setting a deadline
// in the past: a deadline taken from ctx would leave a ctx that is cancelled, or has none, nothing
// to end a read with. The returned func lets go of ctx, and waits for an interruption that has
// begun, so that it cannot land on the next command.
func (q *qmpClient) bound(ctx context.Context) (release func()) {
	// A closed connection refuses the deadline, and says so again on the read or write.
	_ = q.c.SetDeadline(time.Time{})
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = q.c.SetDeadline(time.Unix(1, 0)) // mutate-exempt: any time in the past ends the read alike
		close(interrupted)
	})
	return func() {
		if !stop() {
			<-interrupted
		}
	}
}

// cause is err in the caller's words when the caller's ctx is what ended the read or write: the
// deadline bound set says only "i/o timeout".
func cause(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// run sends one command and reads until its reply, decoding the reply's return into out when
// out is not nil.
func (q *qmpClient) run(ctx context.Context, command string, args, out any) error {
	return q.send(ctx, command, args, nil, out)
}

// send is run with f sent beside the command as SCM_RIGHTS: QEMU takes it as the descriptor
// the command (add-fd, getfd) is about, and keeps its own copy.
func (q *qmpClient) send(ctx context.Context, command string, args any, f *os.File, out any) error {
	defer q.bound(ctx)()
	msg := map[string]any{"execute": command}
	if args != nil {
		msg["arguments"] = args
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("encoding %s: %w", command, err)
	}
	b = append(b, '\n')
	var oob []byte
	if f != nil {
		oob = syscall.UnixRights(int(f.Fd()))
	}
	if _, _, err := q.c.WriteMsgUnix(b, oob, nil); err != nil {
		return fmt.Errorf("sending %s: %w", command, cause(ctx, err))
	}
	for {
		line, err := q.readLine()
		if err != nil {
			return fmt.Errorf("reading the reply to %s: %w", command, cause(ctx, err))
		}
		var m struct {
			Event string `json:"event"`
			Data  struct {
				Device string `json:"device"`
			} `json:"data"`
			Return json.RawMessage `json:"return"`
			Error  *qmpError       `json:"error"`
		}
		if err := json.Unmarshal(line, &m); err != nil {
			return fmt.Errorf("reading the reply to %s: %w", command, err)
		}
		switch {
		case m.Event == "DEVICE_DELETED" && m.Data.Device == q.removing:
			q.removed = true
		case m.Event != "":
		case m.Error != nil:
			return fmt.Errorf("%s: %w", command, m.Error)
		case out != nil:
			if err := json.Unmarshal(m.Return, out); err != nil {
				return fmt.Errorf("reading what %s returned: %w", command, err)
			}
			return nil
		default:
			return nil
		}
	}
}

// readLine is the next line QEMU wrote, refused once it runs past maxQMPLine.
func (q *qmpClient) readLine() ([]byte, error) {
	var line []byte
	for {
		chunk, err := q.in.ReadSlice('\n')
		if len(line)+len(chunk) > maxQMPLine {
			return nil, fmt.Errorf("QEMU sent a line longer than %d bytes", maxQMPLine)
		}
		line = append(line, chunk...)
		if !errors.Is(err, bufio.ErrBufferFull) {
			return line, err
		}
	}
}

// do sends a defined command over the machine's own client, as qmp.Do will once every file of the
// machine sends defined commands.
func do[A, R any](ctx context.Context, q *qmpClient, cmd qmp.Command[A, R], args A) (R, error) {
	var r R
	var a any = args
	if _, none := a.(qmp.None); none {
		a = nil
	}
	var f *os.File
	if fd, ok := a.(qmp.AddFDArgs); ok {
		f, _ = fd.File.(*os.File)
	}
	return r, q.send(ctx, cmd.Name(), a, f, &r)
}

// pollInterval is how often a wait asks QEMU again. Whatever is left of it when the thing waited
// for happens is added to the wait, and a resume waits on one: a poll is one round trip on a
// local socket, so 1 ms costs nothing worth having.
const pollInterval = time.Millisecond

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
