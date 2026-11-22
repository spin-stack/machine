// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"crypto/ed25519"
	"io"
	"os"
	"slices"
	"testing"
	"time"

	xssh "golang.org/x/crypto/ssh"
)

// minimumDelay is the least OpenSSH's userauth minimum time waits since a request arrived: 5 ms, and
// 0-4.2 ms more by the user (auth2.c, MIN_FAIL_DELAY_SECONDS).
const minimumDelay = 5 * time.Millisecond

// TestSSHDDelaysOnlyAFailure is image/openssh's patch on a real guest: spin's shape booted to
// systemd, root's key in authorized_keys, logged into over vsock. Every turn of a failure, a key
// sshd does not accept for root or any key for a user that does not exist, takes at least
// minimumDelay, as OpenSSH wants. The signed request of a login that succeeds takes less, in the
// median of SPIN_SSHD_LOGINS logins (default 10).
//
// A turn is timed by the client, from the request to the answer that ends it, so it holds sshd's
// time and the transport's as well as any delay. A failure can only be longer than the delay, and
// a success shorter only without it: neither bound depends on how fast the host is. sshd answers
// a login's key query with PK_OK before it delays, so the query's turn never shows its delay: the
// delay holds up the signed request instead, which carries both. On v20261122.01's sshd the signed
// request took 16.2 ms in the median and the query 0.74 ms (laptop, 10 logins, 2026-11-22).
//
//	SPIN_SSHD_DELAY_TEST=1  it starts a machine (task boot:sshd-delay)
//	SPIN_SSHD_LOGINS=N      logins that succeed, default 10
func TestSSHDDelaysOnlyAFailure(t *testing.T) {
	if os.Getenv("SPIN_SSHD_DELAY_TEST") == "" {
		t.Skip("set SPIN_SSHD_DELAY_TEST=1: this starts a machine")
	}
	logins := envInt(t, "SPIN_SSHD_LOGINS", 10)
	x := newRollback(t)
	w := x.r.workspace(t, x.signer, false)
	defer w.Kill()
	w.boot(x.ctx, t, x.overlay("top.qcow2", x.frozen, "qcow2"), []string{x.frozen, x.raw})

	var refused, invalid []time.Duration
	for range 3 {
		refused = append(refused, failedTurns(t, w.cid, "root", newKey(t), newKey(t), newKey(t))...)
		invalid = append(invalid, failedTurns(t, w.cid, "nobody-here", newKey(t))...)
	}
	t.Logf("a key root does not have refused in %v; a user that does not exist in %v", refused, invalid)
	for _, turn := range refused {
		if turn < minimumDelay {
			t.Errorf("a key root does not have was refused in %s, under the %s every failure waits", turn, minimumDelay)
		}
	}
	for _, turn := range invalid {
		if turn < minimumDelay {
			t.Errorf("a user that does not exist was refused in %s, under the %s every failure waits", turn, minimumDelay)
		}
	}

	var signed []time.Duration
	for range logins {
		k := &timedKey{Signer: x.signer}
		c, err := vsockLogin(w.cid, "root", xssh.PublicKeysCallback(func() ([]xssh.Signer, error) { return []xssh.Signer{k}, nil }))
		answered := time.Now()
		if err != nil {
			t.Fatalf("logging in as root: %v", err)
		}
		_ = c.Close()
		signed = append(signed, answered.Sub(k.signed))
	}
	t.Logf("a login's signed request answered in %s, the median of %d: %v", median(signed), logins, signed)
	if m := median(signed); m >= minimumDelay {
		t.Errorf("the signed request that logs in took %s in the median: sshd delays a login after PK_OK or after its success", m)
	}
}

// failedTurns offers user's keys, none of which sshd accepts, and returns each query's turn: from
// the query to the next one, and the last to the refusal that ends the login.
func failedTurns(t *testing.T, cid uint32, user string, keys ...xssh.Signer) []time.Duration {
	t.Helper()
	timed := make([]*timedKey, len(keys))
	signers := make([]xssh.Signer, len(keys))
	for i, k := range keys {
		timed[i] = &timedKey{Signer: k}
		signers[i] = timed[i]
	}
	c, err := vsockLogin(cid, user, xssh.PublicKeysCallback(func() ([]xssh.Signer, error) { return signers, nil }))
	refused := time.Now()
	if err == nil {
		_ = c.Close()
		t.Fatalf("%s logged in with keys it does not have", user)
	}
	turns := make([]time.Duration, len(timed))
	for i, k := range timed {
		if k.asked.IsZero() {
			t.Fatalf("key %d of %s was never offered: %v", i+1, user, err)
		}
		next := refused
		if i+1 < len(timed) {
			next = timed[i+1].asked
		}
		turns[i] = next.Sub(k.asked)
	}
	return turns
}

// timedKey is a key that notes when the client first asked for it, which it does just before it
// sends the key's query, and when it signed with it, just after sshd answered the query with PK_OK
// and just before the client sends the signed request.
type timedKey struct {
	xssh.Signer
	asked, signed time.Time
}

func (k *timedKey) PublicKey() xssh.PublicKey {
	if k.asked.IsZero() {
		k.asked = time.Now()
	}
	return k.Signer.PublicKey()
}

func (k *timedKey) Sign(rand io.Reader, data []byte) (*xssh.Signature, error) {
	k.signed = time.Now()
	return k.Signer.Sign(rand, data)
}

func newKey(t *testing.T) xssh.Signer {
	t.Helper()
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := xssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func median(d []time.Duration) time.Duration {
	s := slices.Clone(d)
	slices.Sort(s)
	return s[len(s)/2]
}
