// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	xssh "golang.org/x/crypto/ssh"
)

// timeline is a scratch connection that notes when each write and each read happened.
type timeline struct {
	net.Conn
	t0  time.Time
	mu  sync.Mutex
	log []string
}

func (t *timeline) Write(b []byte) (int, error) {
	t.mu.Lock()
	t.log = append(t.log, fmt.Sprintf("%7.2f W %d", float64(time.Since(t.t0).Microseconds())/1000, len(b)))
	t.mu.Unlock()
	return t.Conn.Write(b)
}

func (t *timeline) Read(b []byte) (int, error) {
	n, err := t.Conn.Read(b)
	t.mu.Lock()
	t.log = append(t.log, fmt.Sprintf("%7.2f R %d", float64(time.Since(t.t0).Microseconds())/1000, n))
	t.mu.Unlock()
	return n, err
}

// timedLogin is Login with each write and read of its connection noted.
func (k *Key) timedLogin(cid uint32) (time.Duration, string, error) {
	t := &timeline{t0: time.Now()}
	d, _, err := k.run(cid, xssh.KeyExchangeMLKEM768X25519, "true", func(c net.Conn) net.Conn { t.Conn = c; return t })
	t.mu.Lock()
	defer t.mu.Unlock()
	return d, strings.Join(t.log, "\n"), err
}
