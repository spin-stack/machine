// SPDX-License-Identifier: Apache-2.0

package qmp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Calls from many goroutines at once, answered in the reverse of the order they were sent, each
// get the answer to their own command.
func TestEachCallGetsTheAnswerToItsOwnCommand(t *testing.T) {
	c, m := start(t, Observer{})
	ctx := bounded(t)
	const calls = 64
	results := make([]<-chan result[echoed], calls)
	for n := range calls {
		results[n] = do(ctx, c, echo, echoArgs{N: n})
	}
	var all []sent
	for range calls {
		s, ok := m.next()
		if !ok {
			t.Fatalf("%d of %d concurrent calls reached QEMU", len(all), calls)
		}
		all = append(all, s)
	}
	slices.Reverse(all)
	for _, s := range all {
		var a echoArgs
		if err := json.Unmarshal(s.Arguments, &a); err != nil {
			t.Fatal(err)
		}
		if err := m.answer(s, fmt.Sprintf(`{"n": %d}`, a.N)); err != nil {
			t.Fatal(err)
		}
	}
	for n, got := range results {
		if r := await(t, got); r.err != nil || r.r.N != n {
			t.Errorf("call %d = %+v, %v; want its own answer", n, r.r, r.err)
		}
	}
}

// A call that gave up leaves its command to run, and the answer QEMU sends for it later is not
// taken for the answer to the call after it.
func TestALateAnswerIsNotTheNextCallsAnswer(t *testing.T) {
	c, m := start(t, Observer{})
	abandon, cancel := context.WithCancel(bounded(t))
	first := do(abandon, c, echo, echoArgs{N: 1})
	s1, _ := m.next()
	cancel()
	if r := await(t, first); !errors.Is(r.err, context.Canceled) {
		t.Fatalf("a call whose ctx was cancelled = %v, want context.Canceled", r.err)
	}
	second := do(bounded(t), c, echo, echoArgs{N: 2})
	s2, _ := m.next()
	_ = m.answer(s1, `{"n": 1}`)
	_ = m.answer(s2, `{"n": 2}`)
	if r := await(t, second); r.err != nil || r.r.N != 2 {
		t.Errorf("the call after an abandoned one = %+v, %v; want its own answer, 2", r.r, r.err)
	}
}

// Cancelling a call's ctx ends its wait for an answer that never comes, though the ctx has no
// deadline, and the next call is answered.
func TestCancellingACallEndsItsWait(t *testing.T) {
	c, m := start(t, Observer{})
	ctx, cancel := context.WithCancel(context.Background())
	waiting := do(ctx, c, nothing, None{})
	_, _ = m.next()
	cancel()
	if r := await(t, waiting); !errors.Is(r.err, context.Canceled) {
		t.Errorf("a cancelled call = %v, want context.Canceled", r.err)
	}
	next := do(bounded(t), c, nothing, None{})
	s, _ := m.next()
	_ = m.answer(s, "{}")
	if r := await(t, next); r.err != nil {
		t.Errorf("the call after a cancelled one: %v", r.err)
	}
}

// A call whose ctx has ended sends nothing.
func TestACallWithAnEndedCtxSendsNothing(t *testing.T) {
	c, m := start(t, Observer{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Do(ctx, c, nothing, None{}); !errors.Is(err, context.Canceled) {
		t.Errorf("a call with an ended ctx = %v, want context.Canceled", err)
	}
	if !m.quiet() {
		t.Error("a call with an ended ctx sent its command")
	}
}

// A line up to the bound is read, and one byte more ends the connection: a monitor that never
// ends a line is not memory the client hands out. The call waiting is told why.
func TestALineOverTheBoundEndsTheConnection(t *testing.T) {
	c, m := start(t, Observer{})
	ctx := bounded(t)
	first := do(ctx, c, nothing, None{})
	s, _ := m.next()
	_ = m.say(padded(s.ID, maxLine))
	if r := await(t, first); r.err != nil {
		t.Errorf("an answer of exactly %d bytes: %v", maxLine, r.err)
	}
	second := do(ctx, c, nothing, None{})
	s, _ = m.next()
	_ = m.say(padded(s.ID, maxLine+1))
	r := await(t, second)
	if !errors.Is(r.err, ErrClosed) || !errors.Is(r.err, ErrLineTooLong) {
		t.Errorf("a call answered with %d bytes = %v, want ErrClosed and ErrLineTooLong", maxLine+1, r.err)
	}
	ended(t, c)
	if !errors.Is(c.Err(), ErrLineTooLong) {
		t.Errorf("Err = %v, want ErrLineTooLong", c.Err())
	}
	if !m.isClosed() {
		t.Error("the connection is still open after a line over the bound")
	}
}

// A line that is not JSON ends the connection: nothing after it can be trusted to be framed.
func TestALineThatIsNotJSONEndsTheConnection(t *testing.T) {
	c, m := start(t, Observer{})
	_ = m.say(`{"return": `)
	ended(t, c)
	if c.Err() == nil || errors.Is(c.Err(), io.EOF) {
		t.Errorf("Err = %v, want the line's decoding error", c.Err())
	}
	if !m.isClosed() {
		t.Error("the connection is still open after a line that is not JSON")
	}
}

// The handshake is bounded by New's ctx, with no clock in the client: a monitor that never greets,
// or never answers qmp_capabilities, is closed when ctx ends.
func TestTheHandshakeEndsWithItsCtx(t *testing.T) {
	for _, tc := range []struct {
		name string
		qemu func(*monitor)
	}{
		{"no greeting", func(*monitor) {}},
		{"no answer to qmp_capabilities", func(m *monitor) {
			_ = m.say(greeting)
			_, _ = m.next()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newMonitor()
			go tc.qemu(m)
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			if _, err := New(ctx, m, Observer{}); !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("New = %v, want context.DeadlineExceeded", err)
			}
			if !m.isClosed() {
				t.Error("New gave up and left the connection open")
			}
		})
	}
}

// ctx bounds only the handshake: the client outlives it.
func TestTheClientOutlivesNewsCtx(t *testing.T) {
	m := newMonitor()
	go func() { _ = m.greet() }()
	ctx, cancel := context.WithCancel(context.Background())
	c, err := New(ctx, m, Observer{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	cancel()
	got := do(bounded(t), c, nothing, None{})
	s, ok := m.next()
	if !ok {
		t.Fatal("a call after New's ctx ended never reached QEMU")
	}
	_ = m.answer(s, "{}")
	if r := await(t, got); r.err != nil {
		t.Errorf("a call after New's ctx ended: %v", r.err)
	}
}

// What the greeting says is the client's Version; a first line that is no greeting is no client.
func TestTheGreetingSaysTheVersion(t *testing.T) {
	c, _ := start(t, Observer{})
	if v, want := c.Version(), (Version{Major: 11, Minor: 1, Micro: 90, Package: "spin"}); v != want {
		t.Errorf("Version = %+v, want %+v", v, want)
	}

	m := newMonitor()
	go func() { _ = m.say(`{"return": {}}`) }()
	if _, err := New(bounded(t), m, Observer{}); err == nil {
		t.Error("New accepted a first line that is not QEMU's greeting")
	}
	if !m.isClosed() {
		t.Error("New refused a greeting and left the connection open")
	}

	m = newMonitor()
	_ = m.out.Close()
	if _, err := New(bounded(t), m, Observer{}); !errors.Is(err, io.EOF) {
		t.Errorf("New on a monitor that closed before greeting = %v, want io.EOF", err)
	}
}

// QEMU refusing a command is an *Error naming the command, the class and QEMU's words.
func TestARefusalIsAnError(t *testing.T) {
	c, m := start(t, Observer{})
	got := do(bounded(t), c, nothing, None{})
	s, _ := m.next()
	_ = m.say(fmt.Sprintf(`{"error": {"class": "DeviceNotFound", "desc": "no x"}, "id": %d}`, s.ID))
	r := await(t, got)
	var e *Error
	if !errors.As(r.err, &e) || *e != (Error{Command: "x-nothing", Class: "DeviceNotFound", Desc: "no x"}) {
		t.Errorf("a refused command = %v, want an *Error naming it", r.err)
	}
	if said := r.err.Error(); !strings.Contains(said, "x-nothing") || !strings.Contains(said, "no x") {
		t.Errorf("a refusal says %q, which does not name the command and QEMU's words", said)
	}
}

// An answer that does not decode into the command's answer type is an error, not a zero value.
func TestAnAnswerOfTheWrongShapeIsAnError(t *testing.T) {
	c, m := start(t, Observer{})
	got := do(bounded(t), c, echo, echoArgs{N: 1})
	s, _ := m.next()
	_ = m.answer(s, `{"n": "one"}`)
	if r := await(t, got); r.err == nil {
		t.Errorf("an answer of the wrong shape = %+v, want an error", r.r)
	}
}

// Arguments are sent as the command's: none at all for None, which QEMU refuses as null.
func TestArgumentsAreSentAsDefined(t *testing.T) {
	c, m := start(t, Observer{})
	ctx := bounded(t)

	none := do(ctx, c, nothing, None{})
	s, _ := m.next()
	if want := fmt.Sprintf(`{"execute":"x-nothing","id":%d}`+"\n", s.ID); string(s.line) != want {
		t.Errorf("sent %q, want %q", s.line, want)
	}
	_ = m.answer(s, "{}")
	if r := await(t, none); r.err != nil {
		t.Error(r.err)
	}

	some := do(ctx, c, echo, echoArgs{N: 7})
	s, _ = m.next()
	if want := fmt.Sprintf(`{"execute":"x-echo","arguments":{"n":7},"id":%d}`+"\n", s.ID); string(s.line) != want {
		t.Errorf("sent %q, want %q", s.line, want)
	}
	_ = m.answer(s, `{"n": 7}`)
	if r := await(t, some); r.err != nil {
		t.Error(r.err)
	}
}

// A Command no definition made is refused, and nothing is sent.
func TestTheZeroCommandIsRefused(t *testing.T) {
	c, m := start(t, Observer{})
	if _, err := Do(bounded(t), c, Command[None, None]{}, None{}); err == nil {
		t.Error("Do sent a command with no name")
	}
	if !m.quiet() {
		t.Error("the zero Command reached QEMU")
	}
}

// A command whose arguments carry a File sends it with the line, and no other line goes out until
// QEMU has answered it: QEMU keeps one descriptor per connection, replaced by the next to arrive.
func TestADescriptorHoldsTheConnectionUntilItsAnswer(t *testing.T) {
	c, m := start(t, Observer{})
	ctx := bounded(t)
	handed := do(ctx, c, handOver, withFile{named("disk")})
	s, _ := m.next()
	if s.file != named("disk") {
		t.Errorf("the descriptor beside x-hand-over = %v, want disk", s.file)
	}
	after := do(ctx, c, nothing, None{})
	if !m.quiet() {
		t.Fatal("a line went out before QEMU answered the command carrying a descriptor")
	}
	_ = m.answer(s, "{}")
	if r := await(t, handed); r.err != nil {
		t.Fatal(r.err)
	}
	s, ok := m.next()
	if !ok {
		t.Fatal("the next line never went out once QEMU answered the descriptor's command")
	}
	if s.file != nil {
		t.Errorf("a command with no File sent %v", s.file)
	}
	_ = m.answer(s, "{}")
	if r := await(t, after); r.err != nil {
		t.Error(r.err)
	}
}

// The descriptor's hold ends with QEMU's answer though its call gave up first, and an answer QEMU
// repeats, or one to no command, takes nothing from the turn, held or not.
func TestTheDescriptorsHoldEndsWithItsAnswerAlone(t *testing.T) {
	c, m := start(t, Observer{})
	ctx := bounded(t)
	abandon, cancel := context.WithCancel(ctx)
	handed := do(abandon, c, handOver, withFile{named("disk")})
	first, _ := m.next()
	cancel()
	await(t, handed)
	waiting := do(ctx, c, nothing, None{})
	if !m.quiet() {
		t.Fatal("a call that gave up let the turn go before QEMU took its descriptor")
	}
	_ = m.answer(first, "{}")
	s, ok := m.next()
	if !ok {
		t.Fatal("QEMU's answer to the descriptor did not let the turn go")
	}

	repeat := func() {
		t.Helper()
		// The descriptor's command, qmp_capabilities (the first id) and an id no command had.
		for _, id := range []uint64{first.ID, 1, 0} {
			if err := m.say(fmt.Sprintf(`{"return": {}, "id": %d}`, id)); err != nil {
				t.Fatal(err)
			}
		}
	}
	repeat() // the turn is free
	if err := m.answer(s, "{}"); err != nil {
		t.Fatal(err)
	}
	if r := await(t, waiting); r.err != nil {
		t.Fatal(r.err)
	}

	second := do(ctx, c, handOver, withFile{named("disk")})
	held, _ := m.next()
	repeat() // the turn is the second descriptor's
	third := do(ctx, c, nothing, None{})
	if !m.quiet() {
		t.Fatal("an answer to another command let a descriptor's turn go")
	}
	_ = m.answer(held, "{}")
	if r := await(t, second); r.err != nil {
		t.Fatal(r.err)
	}
	last, ok := m.next()
	if !ok {
		t.Fatal("the turn never came back after the repeated answers")
	}
	_ = m.answer(last, "{}")
	if r := await(t, third); r.err != nil {
		t.Error(r.err)
	}
}

// A command carrying a File that could not be written lets the turn go.
func TestADescriptorThatCannotGoLetsTheTurnGo(t *testing.T) {
	c, m := start(t, Observer{})
	short, stop := context.WithTimeout(bounded(t), 20*time.Millisecond)
	defer stop()
	blocked := make(chan sent)
	m.sent = blocked // nobody receives: the write waits until its ctx ends
	if _, err := Do(short, c, handOver, withFile{named("disk")}); err == nil {
		t.Fatal("a descriptor the monitor never took was sent")
	}
	m.sent = make(chan sent, 128)
	got := do(bounded(t), c, nothing, None{})
	s, ok := m.next()
	if !ok {
		t.Fatal("a failed write of a descriptor kept the turn")
	}
	_ = m.answer(s, "{}")
	if r := await(t, got); r.err != nil {
		t.Error(r.err)
	}
}

// A call waiting for its turn on the wire still ends with its ctx.
func TestACallWaitingForTheWireEndsWithItsCtx(t *testing.T) {
	c, m := start(t, Observer{})
	_ = do(bounded(t), c, handOver, withFile{named("disk")}) // holds the turn: QEMU never answers
	_, _ = m.next()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := Do(ctx, c, nothing, None{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a call that never got the wire = %v, want context.DeadlineExceeded", err)
	}
}

// Close ends every call in flight, and every call after it, with ErrClosed.
func TestCloseEndsEveryCall(t *testing.T) {
	c, m := start(t, Observer{})
	ctx := bounded(t)
	waiting := do(ctx, c, nothing, None{})
	_, _ = m.next()
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if r := await(t, waiting); !errors.Is(r.err, ErrClosed) {
		t.Errorf("a call in flight at Close = %v, want ErrClosed", r.err)
	}
	ended(t, c)
	if !errors.Is(c.Err(), ErrClosed) {
		t.Errorf("Err after Close = %v, want ErrClosed", c.Err())
	}
	if _, err := Do(ctx, c, nothing, None{}); !errors.Is(err, ErrClosed) {
		t.Errorf("a call after Close = %v, want ErrClosed", err)
	}
	if !m.quiet() {
		t.Error("a call after Close was sent")
	}
	if err := c.Close(); err != nil {
		t.Errorf("a second Close: %v", err)
	}
}

// Close returns once the reader has stopped, so nothing the caller handed the client runs after.
func TestCloseReturnsOnceTheReaderStops(t *testing.T) {
	c, _ := start(t, Observer{})
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.Done():
	default:
		t.Error("Close returned while the reader still ran")
	}
}

// A call whose line is on its way to QEMU when the connection is closed is told ErrClosed too.
func TestCloseEndsACallMidWrite(t *testing.T) {
	c, m := start(t, Observer{})
	m.sent = make(chan sent) // nobody receives: the write waits on QEMU
	writing := do(bounded(t), c, nothing, None{})
	time.Sleep(20 * time.Millisecond) // the write is waiting
	_ = c.Close()
	if r := await(t, writing); !errors.Is(r.err, ErrClosed) {
		t.Errorf("a call writing at Close = %v, want ErrClosed", r.err)
	}
}

// A call waiting for the wire when the connection ends is told so.
func TestTheEndReachesACallWaitingForTheWire(t *testing.T) {
	c, m := start(t, Observer{})
	_ = do(bounded(t), c, handOver, withFile{named("disk")}) // holds the turn
	_, _ = m.next()
	waiting := do(bounded(t), c, nothing, None{})
	_ = c.Close()
	if r := await(t, waiting); !errors.Is(r.err, ErrClosed) {
		t.Errorf("a call waiting for the wire at Close = %v, want ErrClosed", r.err)
	}
}

// Err is nil while the connection is up, and QEMU closing it is io.EOF, which a call in flight is
// told beside ErrClosed.
func TestQEMUClosingTheConnectionIsEOF(t *testing.T) {
	c, m := start(t, Observer{})
	if err := c.Err(); err != nil {
		t.Errorf("Err while connected = %v", err)
	}
	waiting := do(bounded(t), c, nothing, None{})
	_, _ = m.next()
	_ = m.out.Close()
	if r := await(t, waiting); !errors.Is(r.err, ErrClosed) || !errors.Is(r.err, io.EOF) {
		t.Errorf("a call in flight when QEMU went = %v, want ErrClosed and io.EOF", r.err)
	}
	ended(t, c)
	if !errors.Is(c.Err(), io.EOF) {
		t.Errorf("Err = %v, want io.EOF", c.Err())
	}
	if err := c.Close(); err != nil {
		t.Errorf("Close after QEMU went: %v", err)
	}
}

// QEMU answers quit and then goes: the answer that came first is the call's, every time.
func TestAnAnswerBeforeTheEndIsTheAnswer(t *testing.T) {
	for range 50 {
		c, m := start(t, Observer{})
		got := do(bounded(t), c, nothing, None{})
		s, _ := m.next()
		_ = m.answer(s, "{}")
		_ = m.out.Close()
		ended(t, c)
		if r := await(t, got); r.err != nil {
			t.Fatalf("a call answered just before QEMU went = %v, want its answer", r.err)
		}
	}
}

// The Observer's Command hook wraps every call: the call runs under the context it returns, and
// it is told how the call ended.
func TestTheObserverWrapsEveryCommand(t *testing.T) {
	type ended struct {
		name string
		err  error
	}
	var seen []ended
	c, m := start(t, Observer{Command: func(ctx context.Context, name string) (context.Context, func(error)) {
		ctx, cancel := context.WithCancel(ctx)
		if name == "x-echo" {
			cancel() // a bound the observer sets is the call's bound
		}
		return ctx, func(err error) { cancel(); seen = append(seen, ended{name, err}) }
	}})
	ctx := bounded(t)
	got := do(ctx, c, nothing, None{})
	s, _ := m.next()
	_ = m.say(fmt.Sprintf(`{"error": {"class": "GenericError", "desc": "no"}, "id": %d}`, s.ID))
	await(t, got)
	if _, err := Do(ctx, c, echo, echoArgs{}); !errors.Is(err, context.Canceled) {
		t.Errorf("a call whose observer cancelled its ctx = %v, want context.Canceled", err)
	}
	var e *Error
	if len(seen) != 3 || seen[0] != (ended{"qmp_capabilities", nil}) ||
		seen[1].name != "x-nothing" || !errors.As(seen[1].err, &e) ||
		seen[2].name != "x-echo" || !errors.Is(seen[2].err, context.Canceled) {
		t.Errorf("the observer saw %+v, want the handshake, the refusal and the cancelled call", seen)
	}
}

// The client reads no clock: what bounds a call is its caller's ctx, so a caller that may hold no
// clock (spin's storage agent) can use it, and a deterministic test of it stays deterministic.
func TestTheClientReadsNoClock(t *testing.T) {
	clocks := map[string]bool{"Now": true, "Since": true, "Until": true, "After": true, "AfterFunc": true,
		"NewTimer": true, "NewTicker": true, "Tick": true, "Sleep": true}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	read := 0
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		read++
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if s, ok := n.(*ast.SelectorExpr); ok {
				if pkg, ok := s.X.(*ast.Ident); ok && pkg.Name == "time" && clocks[s.Sel.Name] {
					t.Errorf("%s reads the clock: time.%s", path, s.Sel.Name)
				}
			}
			return true
		})
	}
	if read == 0 {
		t.Fatal("no source to read")
	}
}
