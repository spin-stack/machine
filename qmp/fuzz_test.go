// SPDX-License-Identifier: Apache-2.0

package qmp

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/spin-stack/machine/internal/qmpdef"
)

// A QEMU the guest has taken over writes whatever it likes to its monitor, and the client reads
// it on the host (#653). Whatever it writes, the client neither panics nor hangs: every call
// returns once the connection ends, and the connection ends with what QEMU wrote.

// scripted is a QEMU that says its lines in turns: chunk 0 at once, and chunk i once the client
// has written its i-th line. Turns make each run of the fuzz target the same run: lines that
// all came at once would race the calls they answer.
type scripted struct {
	mu      sync.Mutex
	turn    *sync.Cond
	chunks  [][]byte
	said    int // chunks said
	writes  int
	reading *bytes.Reader
	closed  bool
}

func newScripted(input []byte) *scripted {
	s := &scripted{chunks: bytes.Split(input, []byte("\n\n"))}
	s.turn = sync.NewCond(&s.mu)
	return s
}

func (s *scripted) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		if s.closed {
			return 0, io.ErrClosedPipe
		}
		if s.reading != nil && s.reading.Len() > 0 {
			return s.reading.Read(p)
		}
		if s.said == len(s.chunks) {
			return 0, io.EOF
		}
		if s.said <= s.writes {
			s.reading = bytes.NewReader(append(s.chunks[s.said], '\n'))
			s.said++
			continue
		}
		s.turn.Wait()
	}
}

func (s *scripted) WriteLine(_ context.Context, _ []byte, _ File) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return io.ErrClosedPipe
	}
	s.writes++
	s.turn.Broadcast()
	return nil
}

// rest says every chunk left, whatever the client writes.
func (s *scripted) rest() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes = len(s.chunks)
	s.turn.Broadcast()
}

func (s *scripted) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.turn.Broadcast()
	return nil
}

var (
	queryImage = qmpdef.Define[None, []ImageInfo]("x-query-image")
	anything   = qmpdef.DefineEvent[map[string]any]("X_ANYTHING")
)

func FuzzWhatQEMUSays(f *testing.F) {
	greeting := `{"QMP": {"version": {"qemu": {"micro": 0, "minor": 2, "major": 11}, "package": ""}, "capabilities": []}}`
	for _, seed := range []string{
		greeting + "\n\n" + `{"return": {}, "id": 1}` + "\n\n" +
			`{"event": "X_ANYTHING", "data": {"a": [1, {"b": null}]}, "timestamp": {"seconds": 1, "microseconds": 2}}` + "\n" +
			`{"return": [{"filename": "o.qcow2", "format-specific": {"type": "qcow2", "data": {"corrupt": true}}, "backing-image": {"filename": "base"}}], "id": 2}`,
		greeting + "\n\n" + `{"error": {"class": "GenericError", "desc": "no"}, "id": 1}`,
		greeting + "\n\n" + `{"return": {}, "id": 1}` + "\n\n" + `{"return": {}, "id": 7}` + "\n" + `{"event": "STOP"}`,
		`{"return": {}}`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, input []byte) {
		// A QEMU that never answers holds a call until its ctx ends, as it holds New's: that is
		// the caller's bound, not a hang. A hang is a connection that outlives QEMU's end.
		quick := func() context.Context {
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			t.Cleanup(cancel)
			return ctx
		}
		qemu := newScripted(input)
		c, err := New(quick(), qemu, Observer{})
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		events := Expect(c, anything, nil)
		defer events.Stop()
		_, _ = Do(quick(), c, queryImage, None{})
		_, _ = Do(quick(), c, queryImage, None{})
		_, _ = Last(c, anything)
		qemu.rest()
		select {
		case <-c.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("the connection outlived what QEMU said")
		}
		if c.Err() == nil {
			t.Fatal("the connection ended and says no error")
		}
	})
}
