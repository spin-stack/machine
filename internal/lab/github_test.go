// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// gh's answers to `gh run view`, as it writes them.
var (
	notFound  = errors.New("gh run view 1 -R o/r: exit status 1: failed to get run: HTTP 404: Not Found")
	forbidden = errors.New("gh run view 1 -R o/r: exit status 1: failed to get run: HTTP 403: Forbidden")
	running   = answer{out: `{"databaseId":1,"status":"in_progress","conclusion":"","url":"u"}`}
	completed = answer{out: `{"databaseId":1,"status":"completed","conclusion":"success","url":"u"}`}
)

type answer struct {
	out string
	err error
}

// github answers each read of the run with the next of its answers, and counts the reads.
type github struct {
	answers []answer
	reads   int
}

func (g *github) view() ([]byte, error) {
	a := g.answers[g.reads]
	g.reads++
	return []byte(a.out), a.err
}

// A run lab dispatch has just started can answer 404 to its first reads and still be running
// (#520). Wait follows it to the end through that, and fails at once on anything else.
func TestWaitFollowsARunGitHubCannotReadYet(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answers []answer
		reads   int
		fails   error // the answer Wait gives up on, or nil for the completed run
	}{
		{"completed", []answer{running, completed}, 2, nil},
		{"404 then completed", []answer{{err: notFound}, {err: notFound}, running, completed}, 4, nil},
		{"404 past a minute", []answer{{err: notFound}, {err: notFound}, {err: notFound}, completed}, 3, notFound},
		{"404 after the run was read", []answer{running, {err: notFound}, {err: notFound}, {err: notFound}}, 4, notFound},
		{"403", []answer{{err: forbidden}, completed}, 1, forbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &github{answers: tc.answers}
			r, err := waitFor(t.Context(), 1, time.Microsecond, g.view)
			if g.reads != tc.reads {
				t.Errorf("Wait read the run %d times, want %d", g.reads, tc.reads)
			}
			switch {
			case tc.fails != nil:
				if !errors.Is(err, tc.fails) {
					t.Errorf("Wait = %+v, %v, want %v", r, err, tc.fails)
				}
			case err != nil || r != (Run{ID: 1, Status: "completed", Conclusion: "success", URL: "u"}):
				t.Errorf("Wait = %+v, %v, want the completed run", r, err)
			}
		})
	}
}

// An answer that is no run fails at once, naming the run.
func TestWaitRefusesAnAnswerThatIsNoRun(t *testing.T) {
	g := &github{answers: []answer{{out: "<html>"}, completed}}
	if _, err := waitFor(t.Context(), 1, time.Microsecond, g.view); err == nil || !strings.Contains(err.Error(), "reading run 1") || g.reads != 1 {
		t.Errorf("Wait on an answer that is no run = %v after %d reads", err, g.reads)
	}
}

// A person who gives up on a run (^C) is not kept waiting for its next read.
func TestWaitStopsWhenItsCallerDoes(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	g := &github{answers: []answer{running, completed}}
	if _, err := waitFor(ctx, 1, time.Hour, g.view); !errors.Is(err, context.Canceled) || g.reads != 1 {
		t.Errorf("Wait after its caller stopped = %v after %d reads, want %v after 1", err, g.reads, context.Canceled)
	}
}
