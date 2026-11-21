// SPDX-License-Identifier: Apache-2.0

package qmp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spin-stack/machine/internal/qmpdef"
)

// The commands and events these tests send and wait on, defined as the module defines its own.
type (
	echoArgs struct {
		N int `json:"n"`
	}
	echoed struct {
		N int `json:"n"`
	}
	deleted struct {
		Device string `json:"device"`
	}
)

var (
	echo    = qmpdef.Define[echoArgs, echoed]("x-echo")
	nothing = qmpdef.Define[None, None]("x-nothing")
	gone    = qmpdef.DefineEvent[deleted]("X_DELETED")
	stopped = qmpdef.DefineEvent[None]("X_STOP")
)

// withFile is arguments that carry a descriptor, as add-fd's do.
type withFile struct {
	f File
}

func (a withFile) file() File { return a.f }

var handOver = qmpdef.Define[withFile, None]("x-hand-over")

// named is a File with nothing to pass but its name.
type named string

func (n named) Name() string { return string(n) }

// monitor is the QEMU end of a Conn as a test drives it: what the client writes arrives on sent,
// and what QEMU says is written to the client's input.
type monitor struct {
	in     *io.PipeReader // the client's input
	out    *io.PipeWriter // QEMU's output
	sent   chan sent
	closed chan struct{}
	once   sync.Once
}

// sent is one line the client wrote, decoded, and the File beside it.
type sent struct {
	Execute   string          `json:"execute"`
	Arguments json.RawMessage `json:"arguments"`
	ID        uint64          `json:"id"`
	line      []byte
	file      File
}

func newMonitor() *monitor {
	in, out := io.Pipe()
	return &monitor{in: in, out: out, sent: make(chan sent, 128), closed: make(chan struct{})}
}

func (m *monitor) Read(p []byte) (int, error) { return m.in.Read(p) }

func (m *monitor) WriteLine(ctx context.Context, line []byte, f File) error {
	s := sent{line: bytes.Clone(line), file: f}
	if err := json.Unmarshal(line, &s); err != nil {
		return err
	}
	select {
	case m.sent <- s:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-m.closed:
		return io.ErrClosedPipe
	}
}

func (m *monitor) Close() error {
	m.once.Do(func() {
		close(m.closed)
		_ = m.in.Close()
	})
	return nil
}

// isClosed is whether the client closed its end.
func (m *monitor) isClosed() bool {
	select {
	case <-m.closed:
		return true
	default:
		return false
	}
}

// say writes line to the client, which has read it when say returns, or says the client stopped
// reading.
func (m *monitor) say(line string) error {
	wrote := make(chan error, 1)
	go func() {
		_, err := io.WriteString(m.out, line+"\n")
		wrote <- err
	}()
	select {
	case err := <-wrote:
		return err
	case <-time.After(2 * time.Second):
		return errors.New("the client stopped reading")
	}
}

// next is the next line the client wrote, or false when none came within a second: a line that
// should have come and did not is a failure, not a hang until the test binary's timeout.
func (m *monitor) next() (sent, bool) {
	select {
	case s := <-m.sent:
		return s, true
	case <-time.After(time.Second):
		return sent{}, false
	}
}

// quiet is whether the client wrote nothing for a moment.
func (m *monitor) quiet() bool {
	select {
	case <-m.sent:
		return false
	case <-time.After(50 * time.Millisecond):
		return true
	}
}

func (m *monitor) answer(s sent, ret string) error {
	return m.say(fmt.Sprintf(`{"return": %s, "id": %d}`, ret, s.ID))
}

const greeting = `{"QMP": {"version": {"qemu": {"micro": 90, "minor": 1, "major": 11}, "package": "spin"}, "capabilities": ["oob"]}}`

// greet is QEMU's side of the handshake: its greeting, and its answer to qmp_capabilities.
func (m *monitor) greet() error {
	if err := m.say(greeting); err != nil {
		return err
	}
	s, ok := m.next()
	if !ok || s.Execute != "qmp_capabilities" {
		return fmt.Errorf("the client's first command was %q, not qmp_capabilities", s.Execute)
	}
	return m.answer(s, "{}")
}

// start is a client through its handshake with a monitor the test then drives.
func start(t *testing.T, o Observer) (*Client, *monitor) {
	t.Helper()
	m := newMonitor()
	greeted := make(chan error, 1)
	go func() { greeted <- m.greet() }()
	c, err := New(bounded(t), m, o)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-greeted; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, m
}

// bounded is a context the test ends, and that ends on its own if the test hangs.
func bounded(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// do is Do on its own goroutine, its result on the channel returned.
func do[A, R any](ctx context.Context, c *Client, cmd Command[A, R], args A) <-chan result[R] {
	out := make(chan result[R], 1)
	go func() {
		r, err := Do(ctx, c, cmd, args)
		out <- result[R]{r, err}
	}()
	return out
}

type result[R any] struct {
	r   R
	err error
}

// await is what a call started with do returned. A call that never returns fails the test, rather
// than hanging it until the test binary's timeout.
func await[R any](t *testing.T, c <-chan result[R]) result[R] {
	t.Helper()
	select {
	case r := <-c:
		return r
	case <-time.After(2 * time.Second):
		t.Fatal("the call never returned")
		return result[R]{}
	}
}

// ended waits for the client's connection to end.
func ended(t *testing.T, c *Client) {
	t.Helper()
	select {
	case <-c.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the connection never ended")
	}
}

// sync is a round trip: once it returns, the client has handled every line QEMU said before it.
func (m *monitor) sync(t *testing.T, c *Client) {
	t.Helper()
	got := do(t.Context(), c, nothing, None{})
	s, ok := m.next()
	if !ok {
		t.Fatal("the round trip's command never reached QEMU")
	}
	if err := m.answer(s, "{}"); err != nil {
		t.Fatal(err)
	}
	if r := await(t, got); r.err != nil {
		t.Fatal(r.err)
	}
}

// padded is an answer to id exactly n bytes long, its newline included.
func padded(id uint64, n int) string {
	head, tail := fmt.Sprintf(`{"return": {}, "id": %d, "pad": "`, id), `"}`
	return head + strings.Repeat("x", n-len(head)-len(tail)-1) + tail
}
