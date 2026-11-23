// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"strings"
	"testing"
	"time"
)

// A stall's report puts the slowest round trip first, with what each thread was doing, where it
// slept and its kernel stack, so the wait is named without the samples.
func TestAStallsReportNamesEachThreadsWaitSlowestFirst(t *testing.T) {
	fast := stallCall{rtt: 25 * time.Millisecond, states: map[string]map[byte]int{"main": {'S': 50}}, wchans: map[string]map[string]int{"main": {"do_sys_poll": 50}}}
	slow := stallCall{rtt: 300 * time.Millisecond,
		states: map[string]map[byte]int{"main": {'D': 590, 'R': 10}, "monitor": {'S': 600}},
		wchans: map[string]map[string]int{"main": {"io_schedule": 590}, "monitor": {"": 600}},
		stacks: map[string]string{"main": "[<0>] io_schedule+0x46/0x70\n[<0>] folio_wait_bit_common+0x13d/0x350\n"}}
	got := stallReport([]stallCall{fast, slow})
	want := "query-status 300ms\n" +
		"  main: states D=590 R=10; waiting in io_schedule=590\n" +
		"    [<0>] io_schedule+0x46/0x70\n    [<0>] folio_wait_bit_common+0x13d/0x350\n" +
		"  monitor: states S=600; waiting in -=600\n" +
		"query-status 25ms\n"
	if !strings.HasPrefix(got, want) {
		t.Errorf("report\n%s\nwant it to start\n%s", got, want)
	}
}

func TestAThreadsStateIsTheLetterAfterItsComm(t *testing.T) {
	if st, _ := threadState(1<<30, 1<<30); st != '?' {
		t.Errorf("a thread that does not exist reads %c", st)
	}
}
