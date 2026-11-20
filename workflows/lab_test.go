// SPDX-License-Identifier: Apache-2.0

package workflows_test

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/spin-stack/spin-machine/internal/lab"
)

// Every probe lab can run is one lab.yml offers: GitHub refuses a dispatch whose choice is not in
// the list, which is how density's first dispatch failed, after it had waited its turn.
func TestLabYMLOffersEveryProbe(t *testing.T) {
	b, err := os.ReadFile("../.github/workflows/lab.yml")
	if err != nil {
		t.Fatal(err)
	}
	var offered []string
	in := false
	for _, line := range strings.Split(string(b), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "probe:":
			in = true
		case in && strings.HasPrefix(trimmed, "- "):
			offered = append(offered, strings.TrimPrefix(trimmed, "- "))
		case in && strings.HasSuffix(trimmed, ":") && trimmed != "options:":
			in = false
		}
	}
	for _, p := range lab.ProbeNames() {
		if !slices.Contains(offered, p) {
			t.Errorf("lab.yml's probe input does not offer %s (it offers %v)", p, offered)
		}
	}
}
