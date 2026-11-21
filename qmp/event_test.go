// SPDX-License-Identifier: Apache-2.0

package qmp

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
)

func short(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	t.Cleanup(cancel)
	return ctx
}

func deletion(device string) string {
	return fmt.Sprintf(`{"event": "X_DELETED", "data": {"device": %q}, "timestamp": {"seconds": 1, "microseconds": 2}}`, device)
}

// An Expect armed before the command that causes the event sees it, though the event comes before
// the command's answer; only an event match accepts ends the wait.
func TestAnExpectArmedFirstSeesTheEvent(t *testing.T) {
	c, m := start(t, Observer{})
	x := Expect(c, gone, func(d deleted) bool { return d.Device == "x" })
	defer x.Stop()
	got := do(bounded(t), c, nothing, None{})
	s, _ := m.next()
	_ = m.say(deletion("y"))
	_ = m.say(deletion("x"))
	_ = m.answer(s, "{}")
	if r := await(t, got); r.err != nil {
		t.Fatal(r.err)
	}
	d, err := x.Wait(bounded(t))
	if err != nil || d.Device != "x" {
		t.Errorf("Wait = %+v, %v; want the deletion of x", d, err)
	}
	if again, err := x.Wait(short(t)); err != nil || again != d {
		t.Errorf("a second Wait = %+v, %v; want the same event", again, err)
	}
}

// An event that came before Expect does not end its wait: it is some earlier device's with the
// same id. Last is what remembers it.
func TestAnEarlierEventDoesNotEndAWait(t *testing.T) {
	c, m := start(t, Observer{})
	_ = m.say(deletion("x"))
	m.sync(t, c)
	x := Expect(c, gone, func(d deleted) bool { return d.Device == "x" })
	defer x.Stop()
	if _, err := x.Wait(short(t)); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a wait for an event that came before it = %v, want its deadline", err)
	}
	if d, ok := Last(c, gone); !ok || d.Device != "x" {
		t.Errorf("Last = %+v, %v; want the deletion of x", d, ok)
	}
}

// One event ends every wait that matches it, once: a wait armed after it waits for its own.
func TestAnEventEndsEveryWaitThatMatchesIt(t *testing.T) {
	c, m := start(t, Observer{})
	a := Expect(c, gone, nil)
	b := Expect(c, gone, func(d deleted) bool { return d.Device == "x" })
	_ = m.say(deletion("x"))
	_ = m.say(deletion("x"))
	_ = m.say(deletion("y"))
	m.sync(t, c)
	for _, x := range []*Expected[deleted]{a, b} {
		if d, err := x.Wait(bounded(t)); err != nil || d.Device != "x" {
			t.Errorf("Wait = %+v, %v; want the first deletion, of x", d, err)
		}
	}
	later := Expect(c, gone, nil)
	defer later.Stop()
	if _, err := later.Wait(short(t)); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a wait armed after the event = %v, want its deadline", err)
	}
}

// An event whose data is not what the definition says matches nothing, and the wait goes on.
func TestAnEventThatDoesNotDecodeMatchesNothing(t *testing.T) {
	c, m := start(t, Observer{})
	x := Expect(c, gone, nil)
	defer x.Stop()
	_ = m.say(`{"event": "X_DELETED", "data": {"device": 7}}`)
	m.sync(t, c)
	if _, err := x.Wait(short(t)); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a wait offered data it cannot decode = %v, want its deadline", err)
	}
	if _, ok := Last(c, gone); ok {
		t.Error("Last decoded data that is not a deletion")
	}
}

// A stopped wait is offered nothing.
func TestAStoppedWaitSeesNothing(t *testing.T) {
	c, m := start(t, Observer{})
	x := Expect(c, gone, nil)
	x.Stop()
	_ = m.say(deletion("x"))
	m.sync(t, c)
	if _, err := x.Wait(short(t)); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a stopped wait = %v, want its deadline", err)
	}
}

// The connection's end ends every wait with ErrClosed, and a wait armed after it.
func TestTheEndEndsEveryWait(t *testing.T) {
	c, m := start(t, Observer{})
	x := Expect(c, gone, nil)
	_ = m.out.Close()
	if _, err := x.Wait(bounded(t)); !errors.Is(err, ErrClosed) || !errors.Is(err, errEOF) {
		t.Errorf("a wait when QEMU went = %v, want ErrClosed beside io.EOF", err)
	}
	if _, err := Expect(c, gone, nil).Wait(bounded(t)); !errors.Is(err, ErrClosed) {
		t.Errorf("a wait armed after the end = %v, want ErrClosed", err)
	}
}

// An event matched just before the end is still the wait's.
func TestAnEventBeforeTheEndIsTheWaits(t *testing.T) {
	for range 50 {
		c, m := start(t, Observer{})
		x := Expect(c, gone, nil)
		_ = m.say(deletion("x"))
		_ = m.out.Close()
		ended(t, c)
		if _, err := x.Wait(bounded(t)); err != nil {
			t.Fatalf("a wait matched just before QEMU went = %v, want the event", err)
		}
	}
}

// QEMU leaves data out of an event with no members, and the event is still seen and kept.
func TestAnEventWithoutDataIsAnEvent(t *testing.T) {
	c, m := start(t, Observer{})
	x := Expect(c, stopped, nil)
	defer x.Stop()
	_ = m.say(`{"event": "X_STOP", "timestamp": {"seconds": 1, "microseconds": 2}}`)
	if _, err := x.Wait(bounded(t)); err != nil {
		t.Errorf("a wait for an event with no data = %v", err)
	}
	if _, ok := Last(c, stopped); !ok {
		t.Error("Last did not keep an event with no data")
	}
}

// An EventType no definition made is no wait.
func TestTheZeroEventTypeIsRefused(t *testing.T) {
	c, _ := start(t, Observer{})
	x := Expect(c, EventType[deleted]{}, nil)
	defer x.Stop()
	if _, err := x.Wait(bounded(t)); err == nil {
		t.Error("a wait on an event with no name returned no error")
	}
}

// Last is the latest of a defined event, and nothing before the first.
func TestLastIsTheLatest(t *testing.T) {
	c, m := start(t, Observer{})
	if _, ok := Last(c, gone); ok {
		t.Error("Last before any event said it had one")
	}
	_ = m.say(deletion("a"))
	_ = m.say(deletion("b"))
	m.sync(t, c)
	if d, ok := Last(c, gone); !ok || d.Device != "b" {
		t.Errorf("Last = %+v, %v; want b", d, ok)
	}
}

// The Observer sees every event, in order, with whether the module defines it; and it runs on the
// reader, so nothing after an event is handled until it returns.
func TestTheObserverSeesEveryEventInline(t *testing.T) {
	seen := make(chan Event, 8)
	release := make(chan struct{})
	c, m := start(t, Observer{Event: func(e Event) {
		seen <- e
		if e.Name == "HOLD" {
			<-release
		}
	}})
	_ = m.say(deletion("x"))
	_ = m.say(`{"event": "RESET", "data": {"guest": true}}`)
	_ = m.say(`{"event": "HOLD"}`)
	for _, want := range []Event{{Name: "X_DELETED", Defined: true}, {Name: "RESET"}, {Name: "HOLD"}} {
		select {
		case e := <-seen:
			if e.Name != want.Name || e.Defined != want.Defined {
				t.Errorf("the observer saw %s (defined %v), want %s (defined %v)", e.Name, e.Defined, want.Name, want.Defined)
			}
			if e.Name == "RESET" && string(e.Data) != `{"guest": true}` {
				t.Errorf("RESET's data = %s", e.Data)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("the observer never saw %s", want.Name)
		}
	}
	got := do(bounded(t), c, nothing, None{})
	s, _ := m.next()
	answered := make(chan error, 1)
	go func() { answered <- m.answer(s, "{}") }()
	select {
	case <-got:
		t.Fatal("an answer was handled while the observer held the reader")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if r := await(t, got); r.err != nil {
		t.Error(r.err)
	}
	if err := <-answered; err != nil {
		t.Error(err)
	}
}

// Events QEMU sends without end cost the client nothing it keeps: of a name nobody defines,
// nothing; of a defined one, the latest alone.
func TestEventsAreNotKept(t *testing.T) {
	const events, size = 4 << 10, 8 << 10
	c, m := start(t, Observer{})
	pad := strings.Repeat("u", size)
	before := heap()
	for i := range events {
		_ = m.say(fmt.Sprintf(`{"event": "U%s%d", "data": {}}`, pad, i))
		_ = m.say(deletion(fmt.Sprintf("%s%d", pad, i)))
	}
	m.sync(t, c)
	if grew := heap() - before; grew > events*size/8 {
		t.Errorf("%d events of %d bytes left the heap %d bytes larger", 2*events, size, grew)
	}
	if d, ok := Last(c, gone); !ok || !strings.HasSuffix(d.Device, fmt.Sprint(events-1)) {
		t.Error("Last is not the latest deletion")
	}
	runtime.KeepAlive(c)
}

func heap() int64 {
	runtime.GC()
	var s runtime.MemStats
	runtime.ReadMemStats(&s)
	return int64(s.HeapAlloc)
}
