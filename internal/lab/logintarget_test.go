// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"strings"
	"testing"
)

// The login-target edits put this checkout's target and its rest into the overlay, point
// default.target at the target and want the forwarder from it; the spin-early one also takes the
// forwarder off basic.target, and the other does not.
func TestTheLoginTargetEditIsThisCheckoutsTarget(t *testing.T) {
	for _, early := range []bool{false, true} {
		e, err := loginTargetEdit(checkoutRoot(t), early)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(e.Files["/usr/lib/systemd/system/spin-machine-login.target"], "Wants=spin-machine-boot-rest.service\n") ||
			!strings.Contains(e.Files["/usr/lib/systemd/system/spin-machine-boot-rest.service"], "start multi-user.target") ||
			e.Links["/etc/systemd/system/default.target"] != "/usr/lib/systemd/system/spin-machine-login.target" ||
			e.Links["/etc/systemd/system/spin-machine-login.target.wants/spin-lab-forward.service"] == "" {
			t.Errorf("early=%v: %+v", early, e)
		}
		dropIn := e.Files["/etc/systemd/system/spin-lab-forward.service.d/zz-early.conf"]
		if early != strings.Contains(dropIn, "DefaultDependencies=no") {
			t.Errorf("early=%v: the forwarder's drop-in is %q", early, dropIn)
		}
		if !strings.Contains(e.Setup, "spin-machine-login.target") {
			t.Errorf("early=%v: the setup does not refuse a release that has the target: %q", early, e.Setup)
		}
	}
}
