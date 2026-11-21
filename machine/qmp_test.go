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
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// hostileMonitor is the QEMU end of a monitor as a test writes it: each command the client
// sends is handed to reply, whose lines are written back as they are.
func hostileMonitor(t *testing.T, reply func(command string, w *bufio.Writer)) *qmpClient {
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
	client, qemu := conn(fds[0]), conn(fds[1])
	t.Cleanup(func() { _ = client.Close(); _ = qemu.Close() })
	go func() {
		w := bufio.NewWriter(qemu)
		_, _ = w.WriteString(`{"QMP": {"version": {}, "capabilities": []}}` + "\n")
		_ = w.Flush()
		in := bufio.NewScanner(qemu)
		for in.Scan() {
			var c struct {
				Execute string `json:"execute"`
			}
			_ = json.Unmarshal(in.Bytes(), &c)
			reply(c.Execute, w)
			if w.Flush() != nil {
				return
			}
		}
	}()
	q, err := newQMP(client)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

// padded is a reply to a command exactly n bytes long, its newline included.
func padded(n int) string {
	head, tail := `{"return": {}, "pad": "`, "\"}\n"
	return head + strings.Repeat("x", n-len(head)-len(tail)) + tail
}

// A line up to maxQMPLine is read, and one byte more is refused with the bound in its words:
// a monitor that never ends a line is not memory the client hands out.
func TestALineLongerThanTheBoundIsRefused(t *testing.T) {
	q := hostileMonitor(t, func(command string, w *bufio.Writer) {
		switch command {
		case "query-status":
			_, _ = w.WriteString(padded(maxQMPLine))
		case "query-name":
			_, _ = w.WriteString(padded(maxQMPLine + 1))
		default:
			_, _ = w.WriteString(ok + "\n")
		}
	})
	if err := q.run(ctx(t), "query-status", nil, nil); err != nil {
		t.Errorf("a reply of exactly %d bytes: %v", maxQMPLine, err)
	}
	err := q.run(ctx(t), "query-name", nil, nil)
	if err == nil || !strings.Contains(err.Error(), strconv.Itoa(maxQMPLine)) {
		t.Errorf("a reply of %d bytes = %v, want a refusal naming %d", maxQMPLine+1, err, maxQMPLine)
	}
}

// DEVICE_DELETED for devices nobody is removing is not kept: a monitor that sends them without
// end costs the client nothing, and the one waited on is still seen.
func TestDeletionsNobodyWaitsOnAreNotKept(t *testing.T) {
	const events, idLen = 32 << 10, 1 << 10
	unknown := strings.Repeat("u", idLen)
	q := hostileMonitor(t, func(command string, w *bufio.Writer) {
		switch command {
		case "device_del":
			for i := range events {
				fmt.Fprintf(w, `{"event": "DEVICE_DELETED", "data": {"device": "%s%d"}}`+"\n", unknown, i)
			}
		case "query-status":
			_, _ = w.WriteString(`{"event": "DEVICE_DELETED", "data": {"device": "x"}}` + "\n")
		}
		_, _ = w.WriteString(ok + "\n")
	})
	before := heap()
	if err := removeDevice(q, ctx(t), "x"); err != nil {
		t.Fatal(err)
	}
	if grew := heap() - before; grew > events*idLen/8 {
		t.Errorf("%d events for devices nobody removes left the heap %d bytes larger", events, grew)
	}
	runtime.KeepAlive(q)
}

// A DEVICE_DELETED that arrived before the device was asked to go is some earlier device's
// with the same id, and does not end the wait for this one.
func TestAnEarlierDeletionDoesNotEndAWait(t *testing.T) {
	q := hostileMonitor(t, func(command string, w *bufio.Writer) {
		if command == "query-name" {
			_, _ = w.WriteString(`{"event": "DEVICE_DELETED", "data": {"device": "x"}}` + "\n")
		}
		_, _ = w.WriteString(ok + "\n")
	})
	if err := q.run(ctx(t), "query-name", nil, nil); err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := removeDevice(q, short, "x"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a removal whose deletion never came = %v, want it to wait out its deadline", err)
	}
}

func removeDevice(q *qmpClient, ctx context.Context, id string) error {
	if err := q.run(ctx, "device_del", map[string]any{"id": id}, nil); err != nil {
		return err
	}
	return q.waitDeleted(ctx, id)
}

func heap() int64 {
	runtime.GC()
	var s runtime.MemStats
	runtime.ReadMemStats(&s)
	return int64(s.HeapAlloc)
}

// Cancelling a command's ctx ends it while it waits on a reply that never comes, though the ctx
// has no deadline to bound the read with.
func TestCancellingTheCtxEndsARead(t *testing.T) {
	q := hostileMonitor(t, func(command string, w *bufio.Writer) {
		if command != "stop" {
			_, _ = w.WriteString(ok + "\n")
		}
	})
	c, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- q.run(c, "stop", nil, nil) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("a cancelled command = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled command still waits on its reply")
	}
	if err := q.run(ctx(t), "cont", nil, nil); err != nil {
		t.Errorf("the next command after a cancelled one: %v", err)
	}
}
