// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	xssh "golang.org/x/crypto/ssh"

	"github.com/spin-stack/spin-machine/boot"
)

// Where the first SSH login's own time goes, between sshd's accept and the command running, on
// the guest's monotonic clock: boot:ssh-first says how long accept to login is (~125-145 ms on the
// bench fleet, 2026-11-19), and docs/log.md puts ~56 of it in pam_systemd (lab 43518044150, a
// login without it) without saying which hop.
//
// The first login's command is the probe: it marks the journal the moment it runs, and prints
// the start of its own connection's sshd@ unit and of its session's scope. A second login, as
// root through sudo, reads this boot's journal. The marks, in order:
//
//	accept        sshd@'s InactiveExit: PID 1 started the connection's sshd
//	authenticated sshd: "Accepted publickey" - the key exchange and the key
//	session open  pam_unix(sshd:session): "session opened" - pam_systemd comes after it in
//	              common-session, so from here to the command is pam_systemd and what follows
//	logind        systemd-logind: "New session" - logind has the request and asked for the scope
//	scope active  the session scope's ActiveEnter: PID 1 ran the scope's job; logind answers then
//	command       the command's own journal mark: the shell ran it
//
// Journal times are when journald received a line, so each mark is a little late by the same
// kind of amount; the scope's is PID 1's own. Every row of boot:ssh-first, interleaved.
//
//	SPIN_LOGIN_LANE=1  run at all
//	REPS=<n>           boots of each row (default 10)
func TestLoginLane(t *testing.T) {
	if os.Getenv("SPIN_LOGIN_LANE") == "" {
		t.Skip("set SPIN_LOGIN_LANE=1: boots VMs with a vsock device and logs into them")
	}
	out := releaseDir(t)
	if !canSudo() {
		t.Skip("this writes a key into the boot's own overlay through qemu-nbd, which needs sudo")
	}
	if _, err := os.Stat("/dev/vhost-vsock"); err != nil {
		t.Skip("no /dev/vhost-vsock: run task vsock")
	}
	reps := envInt(t, "REPS", 10)
	rows, signer := firstLoginRows(t)

	marks := []string{"accept", "authenticated", "session open", "logind", "scope active", "command"}
	got := map[string][][]time.Duration{} // row -> boot -> mark offsets from accept
	for range reps {
		for _, v := range rows {
			m, err := loginLane(t, out, v, signer)
			if err != nil {
				t.Logf("%s: %v", v.label, err)
				continue
			}
			got[v.label] = append(got[v.label], m)
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\nms on the guest's clock, p50/p95 over the boots of each row, interleaved\n\n  %-16s", "")
	for _, m := range marks[1:] {
		fmt.Fprintf(&b, " %17s", "to "+m)
	}
	b.WriteString("\n")
	for _, v := range rows {
		fmt.Fprintf(&b, "  %-16s", v.label)
		for i := 1; i < len(marks); i++ {
			var ds []time.Duration
			for _, m := range got[v.label] {
				ds = append(ds, m[i]-m[i-1])
			}
			p50, ok := boot.Percentile(ds, .5)
			p95, _ := boot.Percentile(ds, .95)
			if !ok {
				fmt.Fprintf(&b, " %17s", "-")
				continue
			}
			fmt.Fprintf(&b, " %8.1f/%-8.1f", float64(p50)/float64(time.Millisecond), float64(p95)/float64(time.Millisecond))
		}
		fmt.Fprintf(&b, "  (%d boots)\n", len(got[v.label]))
	}
	t.Log(b.String())
}

// loginLane boots v once, logs in with the probe as the first login's command, reads the journal
// in a second login, and returns the marks' times in TestLoginLane's order.
func loginLane(t *testing.T, out string, v variant, signer xssh.Signer) ([]time.Duration, error) {
	t.Helper()
	socks, err := os.MkdirTemp("/tmp", "ll")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(socks) }()
	r := &checkpointRun{v: v, label: v.label, out: out, signer: signer, dir: t.TempDir(), socks: socks}
	mustRun(t, filepath.Join(out, "bin", "qemu-img"), "create", "-f", "qcow2",
		"-F", "qcow2", "-b", filepath.Join(out, "image", "rootfs.qcow2"), r.overlay())
	editOverlay(t, r.overlay(), v)
	r.cid = uint32(100000 + rand.IntN(1<<20))
	_, stop := r.launch(t, filepath.Join(socks, "q"))
	defer stop()

	// The mark first, before anything the command does costs time; then the starts PID 1 keeps,
	// read while the connection's sshd@ is still running and its scope still exists.
	const probe = `logger -t spin-lane command
unit=$(systemctl list-units 'sshd@*' --state=active --plain --no-legend | awk 'NR==1{print $1}')
scope=$(sed -n 's|.*/\(session-[0-9]*\.scope\)$|\1|p' /proc/self/cgroup)
systemctl show -p Id -p InactiveExitTimestampMonotonic -p ActiveEnterTimestampMonotonic "$unit" "$scope"`
	deadline := time.Now().Add(60 * time.Second)
	for !vsockAccepts(r.cid, 22) {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("sshd never accepted on vsock")
		}
		time.Sleep(2 * time.Millisecond)
	}
	var units string
	for {
		_, o, err := sshRun(r.cid, signer, xssh.KeyExchangeMLKEM768X25519, probe)
		if err == nil {
			units = o
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("never logged in: %w", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	_, journal, err := sshRun(r.cid, signer, xssh.KeyExchangeMLKEM768X25519,
		"sudo journalctl -b -o json --output-fields=MESSAGE,SYSLOG_IDENTIFIER,__MONOTONIC_TIMESTAMP SYSLOG_IDENTIFIER=sshd-session SYSLOG_IDENTIFIER=sshd SYSLOG_IDENTIFIER=systemd-logind SYSLOG_IDENTIFIER=spin-lane")
	if err != nil {
		return nil, fmt.Errorf("reading the journal: %w", err)
	}
	return laneMarks(units, journal)
}

// laneMarks reads the marks out of systemctl show's output and the journal's: the first of each
// in the boot, which is the first login's.
func laneMarks(units, journal string) ([]time.Duration, error) {
	us := func(s string) time.Duration {
		n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		return time.Duration(n) * time.Microsecond
	}
	var accept, scope time.Duration
	for _, block := range strings.Split(strings.TrimSpace(units), "\n\n") {
		p := map[string]string{}
		for _, line := range strings.Split(block, "\n") {
			k, v, _ := strings.Cut(line, "=")
			p[k] = v
		}
		switch {
		case strings.HasPrefix(p["Id"], "sshd@"):
			accept = us(p["InactiveExitTimestampMonotonic"])
		case strings.HasPrefix(p["Id"], "session-"):
			scope = us(p["ActiveEnterTimestampMonotonic"])
		}
	}
	first := map[string]time.Duration{}
	sc := bufio.NewScanner(strings.NewReader(journal))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var e struct {
			Message string `json:"MESSAGE"`
			Ident   string `json:"SYSLOG_IDENTIFIER"`
			Mono    string `json:"__MONOTONIC_TIMESTAMP"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		var mark string
		switch {
		case strings.HasPrefix(e.Message, "Accepted publickey for spin"):
			mark = "authenticated"
		case strings.Contains(e.Message, "pam_unix(sshd:session): session opened for user spin"):
			mark = "session open"
		case e.Ident == "systemd-logind" && strings.HasPrefix(e.Message, "New session"):
			mark = "logind"
		case e.Ident == "spin-lane":
			mark = "command"
		default:
			continue
		}
		if _, seen := first[mark]; !seen {
			first[mark] = us(e.Mono)
		}
	}
	m := []time.Duration{accept, first["authenticated"], first["session open"], first["logind"], scope, first["command"]}
	names := []string{"accept", "authenticated", "session open", "logind", "scope active", "command"}
	for i, d := range m {
		if d == 0 {
			return nil, fmt.Errorf("no %s mark (units %q)", names[i], units)
		}
	}
	return m, nil
}
