// SPDX-License-Identifier: Apache-2.0

package qmp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

var errEOF = io.EOF

// socketpair is the two ends of a QMP socket: the client's and QEMU's.
func socketpair(t *testing.T) (client, qemu *net.UnixConn) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	conn := func(fd int) *net.UnixConn {
		f := os.NewFile(uintptr(fd), "monitor")
		defer func() { _ = f.Close() }()
		c, err := net.FileConn(f)
		if err != nil {
			t.Fatal(err)
		}
		return c.(*net.UnixConn)
	}
	client, qemu = conn(fds[0]), conn(fds[1])
	t.Cleanup(func() { _ = client.Close(); _ = qemu.Close() })
	return client, qemu
}

// receive is what QEMU reads: the bytes up to the first descriptor, and the descriptor.
func receive(t *testing.T, qemu *net.UnixConn) ([]byte, *os.File) {
	t.Helper()
	buf, oob := make([]byte, 4096), make([]byte, syscall.CmsgSpace(4))
	n, oobn, _, _, err := qemu.ReadMsgUnix(buf, oob)
	if err != nil {
		t.Fatal(err)
	}
	if oobn == 0 {
		return buf[:n], nil
	}
	msgs, err := syscall.ParseSocketControlMessage(oob[:oobn])
	if err != nil || len(msgs) != 1 {
		t.Fatalf("control messages %v: %v", msgs, err)
	}
	fds, err := syscall.ParseUnixRights(&msgs[0])
	if err != nil || len(fds) != 1 {
		t.Fatalf("descriptors %v: %v", fds, err)
	}
	f := os.NewFile(uintptr(fds[0]), "passed")
	t.Cleanup(func() { _ = f.Close() })
	return buf[:n], f
}

// Unix passes an *os.File's descriptor beside the line, and QEMU's copy is the same file.
func TestUnixPassesTheDescriptorBesideTheLine(t *testing.T) {
	client, qemu := socketpair(t)
	path := filepath.Join(t.TempDir(), "disk")
	if err := os.WriteFile(path, []byte("spin"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	line := []byte(`{"execute":"add-fd","id":1}` + "\n")
	if err := Unix(client).WriteLine(bounded(t), line, f); err != nil {
		t.Fatal(err)
	}
	got, passed := receive(t, qemu)
	if !bytes.Equal(got, line) {
		t.Errorf("QEMU read %q, want %q", got, line)
	}
	if passed == nil {
		t.Fatal("no descriptor came with the line")
	}
	b := make([]byte, 4)
	if _, err := passed.ReadAt(b, 0); err != nil || string(b) != "spin" {
		t.Errorf("the descriptor QEMU got reads %q, %v; want the file's spin", b, err)
	}
}

// A line with no File carries no descriptor, and Unix reads what QEMU writes and closes.
func TestUnixWithoutAFile(t *testing.T) {
	client, qemu := socketpair(t)
	u := Unix(client)
	if err := u.WriteLine(bounded(t), []byte("{}\n"), nil); err != nil {
		t.Fatal(err)
	}
	if got, passed := receive(t, qemu); string(got) != "{}\n" || passed != nil {
		t.Errorf("QEMU read %q and %v, want {} alone", got, passed)
	}
	if _, err := qemu.Write([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 2)
	if _, err := io.ReadFull(u, b); err != nil || string(b) != "hi" {
		t.Errorf("Unix read %q, %v; want hi", b, err)
	}
	if err := u.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := qemu.Read(b); !errors.Is(err, io.EOF) {
		t.Errorf("QEMU's read after Close = %v, want io.EOF", err)
	}
}

// A File that is not an *os.File has no descriptor to pass: refused, and nothing of its line is
// sent.
func TestUnixRefusesAFileWithNoDescriptor(t *testing.T) {
	client, qemu := socketpair(t)
	u := Unix(client)
	if err := u.WriteLine(bounded(t), []byte("refused\n"), named("disk")); err == nil {
		t.Error("Unix passed a File with no descriptor")
	}
	if err := u.WriteLine(bounded(t), []byte("next\n"), nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := receive(t, qemu); string(got) != "next\n" {
		t.Errorf("QEMU read %q first, want the line after the refused one", got)
	}
}

// A line longer than the socket takes in one sendmsg goes out whole, its descriptor with it.
func TestUnixWritesALongLineWhole(t *testing.T) {
	client, qemu := socketpair(t)
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	line := append(bytes.Repeat([]byte("x"), 4<<20), '\n')
	read := make(chan []byte, 1)
	go func() {
		// Not reading yet, so the first sendmsg takes only what the socket holds.
		time.Sleep(100 * time.Millisecond)
		var all []byte
		buf, oob := make([]byte, 1<<16), make([]byte, syscall.CmsgSpace(4))
		for len(all) < len(line) {
			n, _, _, _, err := qemu.ReadMsgUnix(buf, oob)
			if err != nil {
				break
			}
			all = append(all, buf[:n]...)
		}
		read <- all
	}()
	if err := Unix(client).WriteLine(bounded(t), line, f); err != nil {
		t.Fatal(err)
	}
	if got := <-read; !bytes.Equal(got, line) {
		t.Errorf("QEMU read %d bytes of a %d-byte line", len(got), len(line))
	}
}

// fill writes to c until the socket takes no more, so that the next write waits on QEMU.
func fill(t *testing.T, c *net.UnixConn) {
	t.Helper()
	junk := make([]byte, 64<<10)
	for size := len(junk); size > 0; size /= 2 {
		for {
			_ = c.SetWriteDeadline(time.Now().Add(20 * time.Millisecond))
			if _, err := c.Write(junk[:size]); err != nil {
				break
			}
		}
	}
	_ = c.SetWriteDeadline(time.Time{})
}

// A write waiting on a QEMU that does not read ends with its ctx, though the ctx has no deadline;
// none of the line went out, so the connection stays.
func TestAWriteWaitingOnQEMUEndsWithItsCtx(t *testing.T) {
	client, qemu := socketpair(t)
	fill(t, client)
	u := Unix(client)
	ctx, cancel := context.WithCancel(context.Background())
	wrote := make(chan error, 1)
	go func() { wrote <- u.WriteLine(ctx, []byte("{}\n"), nil) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-wrote:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("a cancelled write = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled write still waits on QEMU")
	}
	drained := make(chan []byte, 1)
	go func() {
		var tail []byte
		buf := make([]byte, 1<<16)
		for !bytes.HasSuffix(tail, []byte("next\n")) {
			n, err := qemu.Read(buf)
			if err != nil {
				break
			}
			tail = append(tail[max(0, len(tail)-16):], buf[:n]...)
		}
		drained <- tail
	}()
	if err := u.WriteLine(bounded(t), []byte("next\n"), nil); err != nil {
		t.Errorf("a write after a cancelled one that sent nothing: %v", err)
	}
	if tail := <-drained; !bytes.HasSuffix(tail, []byte("\x00next\n")) {
		t.Errorf("QEMU's last bytes = %q, want the next line straight after the junk", tail)
	}
}

// A line cut short by its ctx leaves QEMU a stream it cannot frame, so the connection is closed.
func TestALineCutShortClosesTheConnection(t *testing.T) {
	client, qemu := socketpair(t)
	ctx, cancel := context.WithCancel(context.Background())
	wrote := make(chan error, 1)
	go func() { wrote <- Unix(client).WriteLine(ctx, append(bytes.Repeat([]byte("x"), 8<<20), '\n'), nil) }()
	if _, err := qemu.Read(make([]byte, 1)); err != nil { // the line has begun
		t.Fatal(err)
	}
	cancel()
	if err := <-wrote; !errors.Is(err, context.Canceled) {
		t.Errorf("a cut write = %v, want context.Canceled", err)
	}
	_ = qemu.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.Copy(io.Discard, qemu); err != nil {
		t.Errorf("QEMU's read to the end of a cut line = %v, want the connection closed", err)
	}
}
