// SPDX-License-Identifier: Apache-2.0

package lab

import (
	"path/filepath"
	"strings"
	"testing"
)

// --cpu is baseline or nothing: a model named elsewhere is --boot --cpu=NAME, said so.
func TestATreesCPUIsBaselineOrNothing(t *testing.T) {
	base := fakeRelease(t, "k1")
	_, err := MakeTree(filepath.Join(t.TempDir(), "t"), TreeSpec{Name: "t", From: base, CPU: "GraniteRapids-v1"}, src(t))
	if err == nil || !strings.Contains(err.Error(), "--boot --cpu=NAME") {
		t.Errorf("a tree asking --cpu GraniteRapids-v1: %v, want refused, pointing at --boot", err)
	}
}
