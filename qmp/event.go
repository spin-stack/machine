// SPDX-License-Identifier: Apache-2.0

package qmp

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/spin-stack/machine/internal/qmpdef"
)

// EventType is one QMP event a caller can wait on, and the Go type of its data. Like a Command,
// only this module defines one.
type EventType[T any] = qmpdef.EventType[T]

// Event is one event as QEMU sent it.
type Event struct {
	Name string
	Data json.RawMessage
	// Defined is whether this module defines an event of that name: a set a caller can count
	// events by, which a QEMU sending names of its own choosing cannot grow.
	Defined bool
}

// Observer is what a caller sees of a client's traffic. Both hooks are optional.
type Observer struct {
	// Command wraps every Do. It returns the context the call runs under, and a func that is
	// told the call's error, nil for an answer, when the call returns. spin's runner starts a span
	// and sets its per-command bound here: the client has no clock of its own.
	Command func(ctx context.Context, name string) (context.Context, func(error))
	// Event is called with each event, in the order QEMU sent them, on the client's reader. It
	// must not block, and must not use the client: the reader is what would answer it.
	Event func(Event)
}

// waiter is one Expect, as the reader offers it events.
type waiter struct {
	name string
	// match decodes an event's data and asks the caller's match of it. It runs on the reader.
	match   func(json.RawMessage) bool
	matched chan struct{} // closed by the reader when match accepted an event
}

// Expected is a wait for one event, from the moment Expect armed it.
type Expected[T any] struct {
	c    *Client
	w    *waiter
	data T // written by the reader before it closes w.matched
}

// Expect starts listening for the next e whose data match accepts; a nil match accepts the next e.
// Call it before the command that causes the event: one that arrives between the command's answer
// and a later Expect is missed, and Last is what has it. match runs on the client's reader, so it
// must not block or use the client. Stop it once it is no longer wanted.
func Expect[T any](c *Client, e EventType[T], match func(T) bool) *Expected[T] {
	x := &Expected[T]{c: c}
	x.w = &waiter{name: e.Name(), matched: make(chan struct{})}
	x.w.match = func(raw json.RawMessage) bool {
		var t T
		if json.Unmarshal(raw, &t) != nil || match != nil && !match(t) {
			return false
		}
		x.data = t
		return true
	}
	if x.w.name != "" {
		c.mu.Lock()
		c.waiters = append(c.waiters, x.w)
		c.mu.Unlock()
	}
	return x
}

// Wait returns the data of the event Expect armed for, ctx's error, or ErrClosed when the
// connection ends first. Once matched, it returns the same data every time.
func (x *Expected[T]) Wait(ctx context.Context) (T, error) {
	var zero T
	if x.w.name == "" {
		return zero, errors.New("qmp: an EventType no definition made has no name to wait on")
	}
	select {
	case <-x.w.matched:
		return x.data, nil
	case <-ctx.Done():
		return zero, ctx.Err()
	case <-x.c.done:
		select {
		case <-x.w.matched:
			return x.data, nil
		default:
			return zero, x.c.closed()
		}
	}
}

// Stop ends the listening, and lets go of what it holds. A matched Expected has stopped already.
func (x *Expected[T]) Stop() {
	x.c.mu.Lock()
	x.c.waiters = slices.DeleteFunc(x.c.waiters, func(w *waiter) bool { return w == x.w })
	x.c.mu.Unlock()
}

// Last is the data of the latest e QEMU sent on this connection, and false if it sent none. The
// reader keeps it before it offers the event to any Expect, so Expect first and Last second
// misses nothing. Only defined events are kept, one each.
func Last[T any](c *Client, e EventType[T]) (T, bool) {
	c.mu.Lock()
	raw, ok := c.last[e.Name()]
	c.mu.Unlock()
	var t T
	if !ok || json.Unmarshal(raw, &t) != nil {
		return *new(T), false
	}
	return t, true
}

// event hands one event to Last, then to every wait for it that matches, then to the Observer.
// The matches run outside the lock: they are the caller's code.
func (c *Client) event(name string, data json.RawMessage) {
	defined := qmpdef.IsEvent(name)
	c.mu.Lock()
	if defined {
		c.last[name] = data
	}
	var offered []*waiter
	for _, w := range c.waiters {
		if w.name == name {
			offered = append(offered, w)
		}
	}
	c.mu.Unlock()

	var matched []*waiter
	for _, w := range offered {
		if w.match(data) {
			matched = append(matched, w)
		}
	}
	if len(matched) > 0 {
		c.mu.Lock()
		c.waiters = slices.DeleteFunc(c.waiters, func(w *waiter) bool { return slices.Contains(matched, w) })
		c.mu.Unlock()
		for _, w := range matched {
			close(w.matched)
		}
	}
	if c.obs.Event != nil {
		c.obs.Event(Event{Name: name, Data: data, Defined: defined})
	}
}
