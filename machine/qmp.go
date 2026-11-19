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
	"slices"
	"syscall"
	"time"
)

// qmpClient is the smallest QMP client the machine needs: one command at a time, a descriptor sent
// with a command where QEMU takes one (add-fd), and the one event anything here waits on kept
// when it arrives in between.
type qmpClient struct {
	c  *net.UnixConn
	in *bufio.Reader
	// deleted is the id of every device QEMU has reported DEVICE_DELETED for and nothing has
	// waited on yet.
	deleted []string
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

func newQMP(c *net.UnixConn) (*qmpClient, error) {
	q := &qmpClient{c: c, in: bufio.NewReader(c)}
	ctx, cancel := context.WithTimeout(context.Background(), handshakeTimeout)
	defer cancel()
	if err := q.deadline(ctx); err != nil {
		return nil, err
	}
	var greeting struct {
		QMP json.RawMessage `json:"QMP"`
	}
	line, err := q.in.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("reading QEMU's greeting: %w", err)
	}
	if err := json.Unmarshal(line, &greeting); err != nil || greeting.QMP == nil {
		return nil, fmt.Errorf("the monitor's first line is not QEMU's greeting: %q", line)
	}
	if err := q.run(ctx, "qmp_capabilities", nil, nil); err != nil {
		return nil, err
	}
	return q, nil
}

func (q *qmpClient) deadline(ctx context.Context) error {
	d, ok := ctx.Deadline()
	if !ok {
		d = time.Time{}
	}
	if err := q.c.SetDeadline(d); err != nil {
		return fmt.Errorf("bounding the monitor by the caller's deadline: %w", err)
	}
	return nil
}

// run sends one command and reads until its reply, decoding the reply's return into out when
// out is not nil.
func (q *qmpClient) run(ctx context.Context, command string, args, out any) error {
	return q.send(ctx, command, args, nil, out)
}

// send is run with f sent beside the command as SCM_RIGHTS: QEMU takes it as the descriptor
// the command (add-fd, getfd) is about, and keeps its own copy.
func (q *qmpClient) send(ctx context.Context, command string, args any, f *os.File, out any) error {
	if err := q.deadline(ctx); err != nil {
		return err
	}
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
		return fmt.Errorf("sending %s: %w", command, err)
	}
	for {
		line, err := q.in.ReadBytes('\n')
		if err != nil {
			return fmt.Errorf("reading the reply to %s: %w", command, err)
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
		case m.Event == "DEVICE_DELETED":
			q.deleted = append(q.deleted, m.Data.Device)
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

// waitDeleted returns once QEMU has reported the device gone. The event may have arrived with
// an earlier reply, so it is looked for there first.
func (q *qmpClient) waitDeleted(ctx context.Context, id string) error {
	for {
		if i := slices.Index(q.deleted, id); i >= 0 {
			q.deleted = slices.Delete(q.deleted, i, i+1)
			return nil
		}
		// A query to read what QEMU has sent meanwhile, events included.
		if err := q.run(ctx, "query-status", nil, nil); err != nil {
			return fmt.Errorf("waiting for %s to go: %w", id, err)
		}
		if err := sleep(ctx, pollInterval); err != nil {
			return fmt.Errorf("waiting for %s to go: %w", id, err)
		}
	}
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

// addFD hands f to QEMU as a descriptor set of its own, and returns the set's id. opaque is what
// query-fdsets says the descriptor is.
func (q *qmpClient) addFD(ctx context.Context, f *os.File, opaque string) (int, error) {
	if f == nil {
		return 0, errors.New("no file to hand over")
	}
	var r struct {
		Set int `json:"fdset-id"`
	}
	if err := q.send(ctx, "add-fd", map[string]any{"opaque": opaque}, f, &r); err != nil {
		return 0, fmt.Errorf("handing %s to QEMU: %w", opaque, err)
	}
	return r.Set, nil
}

// addFDTo adds f to the descriptor set set, under the same opaque as the rest of it.
func (q *qmpClient) addFDTo(ctx context.Context, set int, f *os.File, opaque string) error {
	if err := q.send(ctx, "add-fd", map[string]any{"fdset-id": set, "opaque": opaque}, f, nil); err != nil {
		return fmt.Errorf("handing a read-only copy of %s to QEMU: %w", opaque, err)
	}
	return nil
}
