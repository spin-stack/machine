// SPDX-License-Identifier: Apache-2.0

// Package qmp is QEMU's machine protocol as this module speaks it: one client per monitor
// connection, safe for every goroutine of its caller, and commands that are values with Go types
// for what they send and what they answer.
//
// A command is defined once, in this module, beside the code that understands it (Command says
// how), and a caller outside the module sends what the module defines. `spin-machine-dev verify
// qemu` holds every definition to the schema of the QEMU it verified, so a bump that renames,
// drops or deprecates a member fails there, not in a VM weeks later.
//
// The package reads no clock and opens nothing. The caller's context bounds each call, and the
// caller hands over the connection, which is how spin's storage agent, which may hold no socket
// or clock of its own, speaks QMP over a transport it injects.
package qmp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/spin-stack/machine/internal/qmpdef"
)

// Command is one QMP command: its name and the Go types of its arguments and its answer. Only
// this module defines commands; outside it, a Command is one the module gave out, or the zero
// value, which Do refuses.
type Command[A, R any] = qmpdef.Command[A, R]

// None is the arguments of a command that takes none, sent as no arguments at all, and the
// answer of one that answers {}.
type None struct{}

// capabilities leaves QMP's negotiation mode, which is all a client may do before it. No
// capability is asked for: out-of-band commands would let QEMU answer out of order, which ids
// would survive and nothing here needs.
var capabilities = qmpdef.Define[None, None]("qmp_capabilities")

// Client is a connection to one QEMU monitor. It is safe for concurrent use: each command carries
// an id, and one reader goroutine owns the connection's input and hands each answer to the call
// with that id, and each event to Last, to whoever Expects it, and to the Observer.
//
// The reader is the only goroutine a Client starts, and it ends when the connection does.
type Client struct {
	conn    Conn
	obs     Observer
	version Version
	// turn is held by the call whose line is on the wire, so lines and their descriptors do not
	// interleave. A channel, not a mutex, so a call waiting for its turn still ends with its ctx.
	turn chan struct{}

	mu      sync.Mutex
	next    uint64
	pending map[uint64]chan answer // each removed by its call when it returns: a late answer is dropped
	// heldFor is the command, by id, that sent a descriptor QEMU may not have taken yet. It holds
	// turn until QEMU answers it, and the reader lets turn go then; 0 is none.
	heldFor uint64
	waiters []*waiter // Expect's, each removed when matched or stopped
	last    map[string]json.RawMessage
	closing bool

	done chan struct{} // closed when the reader ends
	err  error         // why it ended, set before done is closed
}

// answer is what the reader hands a call: what QEMU returned, or its refusal.
type answer struct {
	ret json.RawMessage
	err *Error
}

// Version is the QEMU at the other end, as its greeting says.
type Version struct {
	Major, Minor, Micro int
	Package             string
}

// New reads QEMU's greeting from c and leaves negotiation mode, both within ctx: a connection
// still silent when ctx ends is closed, which bounds the handshake without a clock here. ctx
// bounds only the handshake; the client lives until Close or the connection's end. On an error, c
// is closed.
func New(ctx context.Context, c Conn, o Observer) (*Client, error) {
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	in := bufio.NewReader(c)
	line, err := readLine(in)
	if err != nil {
		stop()
		_ = c.Close()
		return nil, fmt.Errorf("reading QEMU's greeting: %w", cause(ctx, err))
	}
	var greeting struct {
		QMP *struct {
			Version struct {
				QEMU    Version `json:"qemu"`
				Package string  `json:"package"`
			} `json:"version"`
		} `json:"QMP"`
	}
	if err := json.Unmarshal(line, &greeting); err != nil || greeting.QMP == nil {
		stop()
		_ = c.Close()
		return nil, fmt.Errorf("the monitor's first line is not QEMU's greeting: %.80q", line)
	}
	q := &Client{
		conn:    c,
		obs:     o,
		version: greeting.QMP.Version.QEMU,
		turn:    make(chan struct{}, 1),
		pending: map[uint64]chan answer{},
		last:    map[string]json.RawMessage{},
		done:    make(chan struct{}),
	}
	q.version.Package = greeting.QMP.Version.Package
	go q.read(in)
	if _, err := Do(ctx, q, capabilities, None{}); err != nil {
		stop()
		_ = q.Close()
		return nil, fmt.Errorf("leaving QMP's negotiation mode: %w", cause(ctx, err))
	}
	if !stop() {
		// ctx ended as the handshake did, and has closed c.
		return nil, fmt.Errorf("leaving QMP's negotiation mode: %w", ctx.Err())
	}
	return q, nil
}

// cause is err in the caller's words when the caller's ctx is what ended it: a read cut by the
// connection closing says only that it was closed.
func cause(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// Version is the QEMU at the other end, from its greeting.
func (c *Client) Version() Version { return c.version }

// Close ends the connection, and returns once the reader has stopped: no Observer hook or match
// runs after it, so it must not be called from one. Every call and wait still in flight returns
// ErrClosed. The machine runs on: a monitor's client leaving is nothing to QEMU.
func (c *Client) Close() error {
	c.mu.Lock()
	closing := c.closing
	c.closing = true
	c.mu.Unlock()
	if closing {
		<-c.done
		return nil
	}
	err := c.conn.Close()
	<-c.done
	if !errors.Is(c.err, ErrClosed) {
		return nil // the connection had ended already, and closed itself
	}
	return err
}

// Done is closed when the connection has ended: by Close, by QEMU, or by a line QEMU sent that
// could not be read.
func (c *Client) Done() <-chan struct{} { return c.done }

// Err is why the connection ended, once Done is closed, and nil before: ErrClosed after Close,
// io.EOF when QEMU closed it, ErrLineTooLong, or a line that is not JSON.
func (c *Client) Err() error {
	select {
	case <-c.done:
		return c.err
	default:
		return nil
	}
}

// closed is the error of a call or wait the connection's end cut short.
func (c *Client) closed() error {
	if errors.Is(c.err, ErrClosed) {
		return ErrClosed
	}
	return fmt.Errorf("%w: %w", ErrClosed, c.err)
}

// Error is QEMU refusing a command, in its own words.
type Error struct {
	Command string
	Class   string // GenericError, CommandNotFound, DeviceNotFound, ...
	Desc    string
}

func (e *Error) Error() string { return e.Command + ": " + e.Class + ": " + e.Desc }

// ErrClosed is a call or wait the connection's end cut short. It wraps why the connection ended
// when that was not Close.
var ErrClosed = errors.New("qmp: the monitor connection ended")

// Do sends cmd with args and returns QEMU's answer, decoded. ctx bounds the wait, not QEMU: a call
// that gives up leaves its command to run, and its answer, when it comes, is dropped.
//
// The Observer's Command hook, when set, wraps the call. A command whose arguments carry a File
// sends its descriptor with the line.
func Do[A, R any](ctx context.Context, c *Client, cmd Command[A, R], args A) (R, error) {
	return Send(ctx, c, cmd, args).Wait(ctx)
}

// Pending is a command sent whose answer is still to come. Wait takes it, and every Pending is
// waited for: its answer is otherwise kept until the connection ends.
type Pending[R any] struct {
	name string
	sent context.Context // what the command was sent under, which bounds its wait too
	err  error           // the send's own
	wait func(context.Context) (json.RawMessage, error)
	end  func(error)
}

// Send sends cmd with args and returns once its line is written, without its answer. QEMU runs a
// monitor's commands in the order they arrive, so commands sent one after another run in that
// order while their answers are on the way: a client that waits for each answer before it sends
// the next command leaves QEMU idle for a round trip each time (#649). A command that carries a
// File still holds the connection until QEMU answers it (call), so the next Send waits for that.
//
// A command QEMU refuses does not stop the ones sent after it, which run regardless: send ahead
// only what is harmless to run after a refusal, and Wait for each answer in order.
func Send[A, R any](ctx context.Context, c *Client, cmd Command[A, R], args A) *Pending[R] {
	name := cmd.Name()
	p := &Pending[R]{name: name, end: func(error) {}}
	if name == "" {
		p.err = errors.New("qmp: a Command no definition made has no name to send")
		return p
	}
	if c.obs.Command != nil {
		ctx, p.end = c.obs.Command(ctx, name)
	}
	p.sent = ctx
	p.wait, p.err = c.send(ctx, name, args)
	return p
}

// Wait returns QEMU's answer to the command, decoded. ctx bounds the wait, not QEMU, and so does
// the context the command was sent under.
func (p *Pending[R]) Wait(ctx context.Context) (r R, err error) {
	defer func() { p.end(err) }()
	if p.err != nil {
		return r, p.err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(p.sent, cancel)()
	ret, err := p.wait(ctx)
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(ret, &r); err != nil {
		return r, fmt.Errorf("decoding what %s answered: %w", p.name, err)
	}
	return r, nil
}

// carriesFile is arguments that hand QEMU a descriptor with the command. Its method is unexported
// on purpose: add-fd is the one command that takes a descriptor, and both the machine and spin's
// storage agent send it, so its arguments are this package's to define.
type carriesFile interface{ file() File }

type request struct {
	Execute   string `json:"execute"`
	Arguments any    `json:"arguments,omitempty"`
	ID        uint64 `json:"id"`
}

// send writes the command's line and returns the wait for its answer.
func (c *Client) send(ctx context.Context, name string, args any) (func(context.Context) (json.RawMessage, error), error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	var f File
	if a, ok := args.(carriesFile); ok {
		f = a.file()
	}
	if _, ok := args.(None); ok {
		args = nil // QEMU refuses "arguments": null
	}

	c.mu.Lock()
	select {
	case <-c.done:
		c.mu.Unlock()
		return nil, fmt.Errorf("%s: %w", name, c.closed())
	default:
	}
	c.next++
	id := c.next
	got := make(chan answer, 1)
	c.pending[id] = got
	c.mu.Unlock()
	forget := func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}

	line, err := json.Marshal(request{Execute: name, Arguments: args, ID: id})
	if err != nil {
		forget()
		return nil, fmt.Errorf("encoding %s: %w", name, err)
	}
	select {
	case c.turn <- struct{}{}:
	case <-ctx.Done():
		forget()
		return nil, fmt.Errorf("%s: %w", name, ctx.Err())
	case <-c.done:
		forget()
		return nil, fmt.Errorf("%s: %w", name, c.closed())
	}
	if f == nil {
		err = c.conn.WriteLine(ctx, append(line, '\n'), nil)
		<-c.turn
	} else {
		// QEMU keeps the last descriptor it received on a connection, and closes it when the next
		// arrives (chardev/char-socket.c, tcp_chr_recv). A line cut in two by the socket may reach
		// QEMU's command parser together with the next line's descriptor, so nothing else is sent
		// until QEMU has answered this command and taken its own: the reader lets the turn go then,
		// even if this call has given up by then. Set before the write, which QEMU may answer
		// before WriteLine returns.
		c.mu.Lock()
		c.heldFor = id
		c.mu.Unlock()
		if err = c.conn.WriteLine(ctx, append(line, '\n'), f); err != nil {
			c.release(id)
		}
	}
	if err != nil {
		forget()
		return nil, c.unsent(name, err)
	}
	return func(ctx context.Context) (json.RawMessage, error) {
		defer forget()
		return c.wait(ctx, name, got)
	}, nil
}

// wait returns the answer the reader hands got.
func (c *Client) wait(ctx context.Context, name string, got <-chan answer) (json.RawMessage, error) {
	var a answer
	select {
	case a = <-got:
	case <-ctx.Done():
		return nil, fmt.Errorf("%s: %w", name, ctx.Err())
	case <-c.done:
		// QEMU answers quit and then goes: an answer that came before the end is the answer.
		select {
		case a = <-got:
		default:
			return nil, fmt.Errorf("%s: %w", name, c.closed())
		}
	}
	if a.err != nil {
		a.err.Command = name
		return nil, a.err
	}
	return a.ret, nil
}

// unsent is the error of a command whose line could not be written, which is ErrClosed beside the
// write's own when the connection has ended or is being closed, as Close promises.
func (c *Client) unsent(name string, err error) error {
	select {
	case <-c.done:
		return fmt.Errorf("sending %s: %w: %w", name, c.closed(), err)
	default:
	}
	c.mu.Lock()
	closing := c.closing
	c.mu.Unlock()
	if closing {
		return fmt.Errorf("sending %s: %w: %w", name, ErrClosed, err)
	}
	return fmt.Errorf("sending %s: %w", name, err)
}

// message is any line QEMU writes after its greeting: an answer, which carries the id of the
// command it answers, or an event.
type message struct {
	Event  string          `json:"event"`
	Data   json.RawMessage `json:"data"`
	ID     *uint64         `json:"id"`
	Return json.RawMessage `json:"return"`
	Error  *struct {
		Class string `json:"class"`
		Desc  string `json:"desc"`
	} `json:"error"`
}

// read owns the connection's input until it ends. Nothing it does queues: an event goes to Last,
// to the waits that match it and to the Observer before the next line is read, so a QEMU that
// floods the monitor slows the reader, and the socket holds QEMU back.
func (c *Client) read(in *bufio.Reader) {
	for {
		line, err := readLine(in)
		if err != nil {
			c.end(err)
			return
		}
		var m message
		if err := json.Unmarshal(line, &m); err != nil {
			c.end(fmt.Errorf("reading a line from QEMU: %w", err))
			return
		}
		switch {
		case m.Event != "" && m.Data == nil:
			// QEMU leaves data out of an event that has no members: STOP, RESUME, POWERDOWN.
			c.event(m.Event, json.RawMessage("{}"))
		case m.Event != "":
			c.event(m.Event, m.Data)
		case m.ID != nil:
			a := answer{ret: m.Return}
			if m.Error != nil {
				a = answer{err: &Error{Class: m.Error.Class, Desc: m.Error.Desc}}
			}
			c.answer(*m.ID, a)
		}
	}
}

func (c *Client) answer(id uint64, a answer) {
	c.release(id)
	c.mu.Lock()
	got, ok := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if ok {
		got <- a
	}
}

// release lets the turn go if the command id holds it for its descriptor. An answer with an id
// that holds nothing, which a hostile QEMU may send as often as it likes, takes nothing.
func (c *Client) release(id uint64) {
	c.mu.Lock()
	held := id != 0 && id == c.heldFor
	if held {
		c.heldFor = 0
	}
	c.mu.Unlock()
	if held {
		<-c.turn
	}
}

func (c *Client) end(err error) {
	c.mu.Lock()
	if c.closing {
		err = ErrClosed
	}
	c.err = err
	c.mu.Unlock()
	// A line over the bound, or one that is not JSON, ends a connection the caller still holds.
	// Closed before done, so whoever Done wakes finds the connection gone, not still open.
	_ = c.conn.Close()
	close(c.done)
}
