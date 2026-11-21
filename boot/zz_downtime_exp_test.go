// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spin-stack/machine/internal/qmpdef"
	"github.com/spin-stack/machine/machine"
	"github.com/spin-stack/machine/qmp"
)

type expParams struct {
	DowntimeLimit     *int64 `json:"downtime-limit,omitempty"`
	ThrottleInitial   *int64 `json:"cpu-throttle-initial,omitempty"`
	AvailSwitchoverBW *int64 `json:"avail-switchover-bandwidth,omitempty"`
}

type expCaps struct {
	Capabilities []expCap `json:"capabilities"`
}
type expCap struct {
	Capability string `json:"capability"`
	State      bool   `json:"state"`
}

type expInfo struct {
	Status           string `json:"status"`
	ExpectedDowntime int64  `json:"expected-downtime"`
	Downtime         int64  `json:"downtime"`
	TotalTime        int64  `json:"total-time"`
	SetupTime        int64  `json:"setup-time"`
	CPUThrottle      int64  `json:"cpu-throttle-percentage"`
	RAM              struct {
		Transferred    int64   `json:"transferred"`
		Remaining      int64   `json:"remaining"`
		Total          int64   `json:"total"`
		Duplicate      int64   `json:"duplicate"`
		Normal         int64   `json:"normal"`
		DirtySyncCount int64   `json:"dirty-sync-count"`
		DirtyPagesRate int64   `json:"dirty-pages-rate"`
		Mbps           float64 `json:"mbps"`
	} `json:"ram"`
}

var (
	expSetParams = qmpdef.Define[expParams, qmp.None]("migrate-set-parameters")
	expSetCaps   = qmpdef.Define[expCaps, qmp.None]("migrate-set-capabilities")
	expQuery     = qmpdef.Define[qmp.None, expInfo]("query-migrate")
)

type expArm struct {
	Name     string
	Downtime int64
	Auto     bool
}

func parseArms(t *testing.T) []expArm {
	var arms []expArm
	for _, a := range strings.Split(os.Getenv("SPIN_DT_ARMS"), ",") {
		auto := strings.HasSuffix(a, "a")
		n, err := strconv.ParseInt(strings.TrimSuffix(a, "a"), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		arms = append(arms, expArm{Name: a, Downtime: n, Auto: auto})
	}
	return arms
}

func TestDowntimeExperiment(t *testing.T) {
	if os.Getenv("SPIN_DT_ARMS") == "" {
		t.Skip("experiment")
	}
	arms := parseArms(t)
	reps := envInt(t, "SPIN_DT_REPS", 8)
	writer := os.Getenv("SPIN_DT_WRITER") // "WSS_MIB:CHUNK_MIB:SLEEP" or empty
	stateDir := cmp.Or(os.Getenv("SPIN_DT_STATEDIR"), t.TempDir())
	out, err := os.Create(cmp.Or(os.Getenv("SPIN_DT_OUT"), filepath.Join(t.TempDir(), "out.jsonl")))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = out.Close() }()

	r := newRecorder(t, 3900000)
	s, mac := r.next(t, "/bin/sh")
	raw := filepath.Join(r.dir, "raw.sock")
	s.Monitors = []machine.Monitor{{Socket: raw}}
	g := startGuest(t, s, mac, nil)
	g.boot(t, r.overlay(t, t.TempDir()), r.base())
	g.type_(t, "mount -t proc proc /proc; echo UP-$((1+1))")
	g.await(t, regexp.MustCompile(`UP-2\b`), 60*time.Second)
	ctx := context.Background()
	if err := g.SetMemory(ctx, 2048); err != nil {
		t.Fatal(err)
	}
	for size := (machine.MemorySize{}); size.Now != 2048; time.Sleep(50 * time.Millisecond) {
		if size, err = g.Memory(ctx); err != nil {
			t.Fatal(err)
		}
	}
	g.type_(t, fmt.Sprintf("mount -t tmpfs -o size=3g tmpfs /mnt && dd if=/dev/urandom of=/mnt/fill bs=1M count=%d 2>/dev/null; echo FILLED-$((3+3))", envInt(t, "SPIN_DT_FILL", 1536)))
	g.await(t, regexp.MustCompile(`FILLED-6\b`), 3*time.Minute)
	if writer != "" {
		var wss, chunk int
		var pause string
		if _, err := fmt.Sscanf(strings.ReplaceAll(writer, ":", " "), "%d %d %s", &wss, &chunk, &pause); err != nil {
			t.Fatal(err)
		}
		g.type_(t, fmt.Sprintf("dd if=/dev/urandom of=/mnt/src bs=1M count=%d 2>/dev/null; dd if=/dev/urandom of=/mnt/ws bs=1M count=%d 2>/dev/null; echo SRC-$((4+4))", chunk, wss))
		g.await(t, regexp.MustCompile(`SRC-8\b`), time.Minute)
		g.type_(t, fmt.Sprintf("(i=0; while :; do dd if=/mnt/src of=/mnt/ws bs=1M count=%d seek=$(( (i*%d) %% %d )) conv=notrunc 2>/dev/null; i=$((i+1)); echo $i > /mnt/n; sleep %s; done) & echo WRITER-$((5+5))", chunk, chunk, wss, pause))
		g.await(t, regexp.MustCompile(`WRITER-10\b`), time.Minute)
	}
	time.Sleep(3 * time.Second)

	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: raw, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	q, err := qmp.New(ctx, qmp.Unix(c), qmp.Observer{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = q.Close() }()

	enc := json.NewEncoder(out)
	for rep := 0; rep < reps; rep++ {
		order := rand.Perm(len(arms))
		for _, i := range order {
			arm := arms[i]
			dl := arm.Downtime
			if _, err := qmp.Do(ctx, q, expSetParams, expParams{DowntimeLimit: &dl}); err != nil {
				t.Fatal(err)
			}
			if _, err := qmp.Do(ctx, q, expSetCaps, expCaps{Capabilities: []expCap{{"auto-converge", arm.Auto}}}); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(stateDir, "state")
			_ = os.Remove(path)
			f, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			told := &timeline{}
			var atStop expInfo
			sctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			phases := func(p machine.Phase) {
				if _, ok := p.(machine.SaveStarted); ok {
					if _, err := qmp.Do(ctx, q, expSetParams, expParams{DowntimeLimit: &dl}); err != nil {
						t.Error(err)
					}
				}
				told.tell(p)
			}
			_, cost, err := g.Checkpoint(sctx, f, machine.SaveOptions{Phases: phases}, func(ctx context.Context) error {
				var e error
				atStop, e = qmp.Do(ctx, q, expQuery, qmp.None{})
				return e
			})
			cancel()
			if err != nil {
				t.Fatalf("checkpoint: %v\n%s", err, tail([]byte(g.said()), 2000))
			}
			after, err := qmp.Do(ctx, q, expQuery, qmp.None{})
			if err != nil {
				t.Fatal(err)
			}
			notFinal := 0
			contStart := time.Now()
			for {
				err := g.Continue(ctx)
				if err == nil {
					break
				}
				if !strings.Contains(err.Error(), "not finalized") || notFinal > 2000 {
					t.Fatal(err)
				}
				notFinal++
				time.Sleep(time.Millisecond)
			}
			contTook := time.Since(contStart)
			syncStart := time.Now()
			_ = f.Sync()
			syncTook := time.Since(syncStart)
			_ = f.Close()
			rec := map[string]any{"rep": rep, "arm": arm.Name, "paused_ms": cost.Paused.Milliseconds(), "total_ms": cost.Total.Milliseconds(), "ram": cost.RAM, "sync_ms": syncTook.Milliseconds(), "not_finalized": notFinal, "cont_ms": float64(contTook.Microseconds()) / 1000, "at_stop": atStop, "after": after}
			var lastRAM int64
			var stopped, running, completed time.Time
			for _, p := range told.told {
				switch p := p.(type) {
				case machine.RAMWritten:
					if stopped.IsZero() {
						lastRAM = p.Bytes
					}
				case machine.GuestStopped:
					stopped = p.At
				case machine.SaveCompleted:
					completed = p.At
				case machine.GuestRunning:
					running = p.At
				}
			}
			rec["last_ram_before_stop"] = lastRAM
			rec["stopped_to_running_ms"] = float64(running.Sub(stopped).Microseconds()) / 1000
			rec["stopped_to_completed_ms"] = float64(completed.Sub(stopped).Microseconds()) / 1000
			if err := enc.Encode(rec); err != nil {
				t.Fatal(err)
			}
			if b, err := json.Marshal(rec); err == nil {
				t.Logf("DTJSON %s", b)
			}
			t.Logf("rep %d arm %s paused %v total %v ram %d MiB, at stop remaining %d MiB syncs %d mbps %.0f dirty-rate %d pages/s, after syncs %d throttle %d",
				rep, arm.Name, cost.Paused, cost.Total, cost.RAM>>20, atStop.RAM.Remaining>>20, atStop.RAM.DirtySyncCount, atStop.RAM.Mbps, atStop.RAM.DirtyPagesRate, after.RAM.DirtySyncCount, atStop.CPUThrottle)
			_ = os.Remove(path)
			time.Sleep(2 * time.Second)
		}
	}
	if writer != "" {
		g.type_(t, "echo N=$(cat /mnt/n)-END")
		g.await(t, regexp.MustCompile(`N=\d+-END`), 30*time.Second)
		t.Logf("writer: %s", regexp.MustCompile(`N=\d+-END`).FindAllString(g.said(), -1))
	}
}
