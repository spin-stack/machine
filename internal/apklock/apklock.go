// SPDX-License-Identifier: Apache-2.0

// Package apklock reads what an Alpine builder image holds: every package its apk database says is
// installed, as name=version, sorted. A builder's tag is a hash of it, and a proposed builder says
// which of them moved (spin-machine-dev refresh builders).
package apklock

import (
	"fmt"
	"slices"
	"strings"
)

// Lock is an apk installed database's packages as name=version lines, sorted.
func Lock(db string) (string, error) {
	var pins []string
	for _, block := range strings.Split(db, "\n\n") {
		var name, version string
		for _, line := range strings.Split(block, "\n") {
			switch k, v, _ := strings.Cut(line, ":"); k {
			case "P":
				name = v
			case "V":
				version = v
			}
		}
		switch {
		case name == "" && version == "":
			continue
		case name == "" || version == "":
			return "", fmt.Errorf("a package with name %q and version %q", name, version)
		}
		pins = append(pins, name+"="+version)
	}
	if len(pins) == 0 {
		return "", fmt.Errorf("no package installed")
	}
	slices.Sort(pins)
	return strings.Join(pins, "\n") + "\n", nil
}
