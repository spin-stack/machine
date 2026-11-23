// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"fmt"
	"os"
	"path/filepath"
)

// loginTargetEdit is the image's first-login target as this checkout builds it, put into a
// published release's overlay: its two units, and default.target pointed at the target. The
// forwarder stands for spin's supervisor, which the target wants by its name, so it is wanted
// here by the link spin's would get from that name. supervisorEarly also takes the forwarder off
// basic.target, as spin would have to take its supervisor (docs/image.md, "The first login's
// target").
func loginTargetEdit(checkout string, supervisorEarly bool) (Edit, error) {
	units := filepath.Join(checkout, "image", "mkosi.extra", "usr", "local", "lib", "spin-base", "rootfs", "usr", "lib", "systemd", "system")
	e := Edit{Files: map[string]string{}, Links: map[string]string{
		"/etc/systemd/system/default.target":                                           "/usr/lib/systemd/system/spin-machine-login.target",
		"/etc/systemd/system/spin-machine-login.target.wants/spin-lab-forward.service": "/etc/systemd/system/spin-lab-forward.service",
	}}
	for _, u := range []string{"spin-machine-login.target", "spin-machine-boot-rest.service"} {
		b, err := os.ReadFile(filepath.Join(units, u))
		if err != nil {
			return Edit{}, fmt.Errorf("reading %s from this checkout: %w", u, err)
		}
		e.Files["/usr/lib/systemd/system/"+u] = string(b)
	}
	if supervisorEarly {
		e.Files["/etc/systemd/system/spin-lab-forward.service.d/zz-early.conf"] = "[Unit]\nDefaultDependencies=no\nConflicts=shutdown.target\nBefore=shutdown.target\n"
	}
	e.Setup = `test "$(readlink "$MNT/etc/systemd/system/default.target")" != /usr/lib/systemd/system/spin-machine-login.target || { echo "the release's default target is this checkout's already" >&2; exit 1; }
`
	return e, nil
}
