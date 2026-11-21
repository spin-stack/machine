// SPDX-License-Identifier: Apache-2.0

package qmp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"syscall"
	"time"
)

// Conn is what a Client speaks over: the bytes QEMU writes, and lines to QEMU, each with at most
// one descriptor beside it. Unix is the one over a monitor's socket. spin's storage agent brings
// its own, which opens what it passes by path, because it may hold no socket of its own.
type Conn interface {
	io.Reader
	// WriteLine writes line, which ends in a newline, with f's descriptor beside it when f is not
	// nil. QEMU keeps its own copy of the descriptor, and f stays the caller's. ctx bounds the
	// write. On an error either none of line reached QEMU or the Conn is closed: a line cut short
	// leaves QEMU a stream it cannot frame.
	WriteLine(ctx context.Context, line []byte, f File) error
	// Close ends a Read or WriteLine that is waiting, which is how New's ctx bounds the handshake
	// and how Close reaches the reader. It may be called more than once.
	Close() error
}

// File is what a Conn passes as a descriptor. Name is for errors. Unix passes an *os.File and
// refuses any other File.
type File interface{ Name() string }

// Unix is a Conn over a QMP socket. A write blocked on QEMU ends when its ctx does.
func Unix(c *net.UnixConn) Conn { return unixConn{c} }

type unixConn struct{ c *net.UnixConn }

func (u unixConn) Read(p []byte) (int, error) { return u.c.Read(p) }

func (u unixConn) Close() error { return u.c.Close() }

func (u unixConn) WriteLine(ctx context.Context, line []byte, f File) error {
	var oob []byte
	var file *os.File
	if f != nil {
		var ok bool
		if file, ok = f.(*os.File); !ok {
			return fmt.Errorf("passing %s to QEMU: a socket passes only an *os.File's descriptor", f.Name())
		}
		oob = syscall.UnixRights(int(file.Fd()))
	}
	defer u.bound(ctx)()
	n, _, err := u.c.WriteMsgUnix(line, oob, nil)
	runtime.KeepAlive(file) // Fd is valid only while file is reachable
	if err != nil {
		// Nothing went out: a sendmsg that took part of the line says so with no error.
		return failed(ctx, err)
	}
	if n < len(line) {
		// A stream socket's sendmsg may take part of the line. The descriptor went with its first
		// byte, and the rest follows as a write, which takes it all or fails.
		if _, err := u.c.Write(line[n:]); err != nil {
			_ = u.c.Close()
			return fmt.Errorf("%w; the line was cut short, so the connection is closed", failed(ctx, err))
		}
	}
	return nil
}

// failed is a write's err, in ctx's words when ctx is what ended it: bound's deadline says only
// "i/o timeout".
func failed(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return fmt.Errorf("writing to QEMU's monitor: %w", err)
}

// bound ends a write still waiting on QEMU when ctx ends, by setting a deadline in the past: a
// ctx that is cancelled, or has no deadline, gives the socket nothing else to end a write with.
// The returned func lets go of ctx, and waits for an interruption that has begun, so that it
// cannot land on the next line's write. Only the write deadline is touched: the client's reader
// reads beside every write.
func (u unixConn) bound(ctx context.Context) (release func()) {
	// A closed socket refuses the deadline, and says so again on the write.
	_ = u.c.SetWriteDeadline(time.Time{})
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = u.c.SetWriteDeadline(time.Unix(1, 0)) // mutate-exempt: any time in the past ends the write alike
		close(interrupted)
	})
	return func() {
		if !stop() {
			<-interrupted
		}
	}
}

// maxLine bounds one line from QEMU, before it is decoded. A tenant who escapes the guest
// controls QEMU, and a line that never ends is otherwise memory the client grows until it is
// killed. The largest answers the machine reads are qom-list-get's in a checkpoint's record,
// 125 KB and 47 KB on QEMU 11.2.0-rc0, so 4 MiB is ample, and it is the bound spin's own client
// kept.
const maxLine = 4 << 20

// ErrLineTooLong is a line from QEMU over the bound. The connection is closed, because what
// follows cannot be framed.
var ErrLineTooLong = fmt.Errorf("qmp: QEMU sent a line longer than %d bytes", maxLine)

// readLine is the next line QEMU wrote, refused once it runs past maxLine.
func readLine(in *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := in.ReadSlice('\n')
		if len(line)+len(chunk) > maxLine {
			return nil, ErrLineTooLong
		}
		line = append(line, chunk...)
		if !errors.Is(err, bufio.ErrBufferFull) {
			return line, err
		}
	}
}
