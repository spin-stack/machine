// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"strings"
	"testing"
	"time"
)

// drop is off, or the seconds waited after the guest drops its page cache before the save (#276).
func TestTheDropOptionIsOffOrTheSecondsWaitedAfterTheDrop(t *testing.T) {
	for given, want := range map[string]time.Duration{"off": -1, "0": 0, "3": 3 * time.Second} {
		if got, err := dropWait(Options{"drop": given}); err != nil || got != want {
			t.Errorf("drop=%s waits %v, %v; want %v", given, got, err, want)
		}
	}
	for _, bad := range []string{"yes", "-1", "3s"} {
		if _, err := dropWait(Options{"drop": bad}); err == nil || !strings.Contains(err.Error(), "off or a number of seconds") {
			t.Errorf("drop=%s: %v", bad, err)
		}
	}
}
