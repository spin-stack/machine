// SPDX-License-Identifier: Apache-2.0

package boot_test

import "testing"

// The console as #784's restored guest had printed it when the test read it, then the rest of the
// line: no answer until the line ends, and then the whole index.
func TestATPMCheckLineIsReadOnlyWhenItEnds(t *testing.T) {
	const cut = "SPIN-TPM rsa3072=b506c83b542931d34094384681139714 pcr16=624d8f98 nv=ed58648584d5cbd1ca321952caf5ea8e7d433"
	const rest = "06c5a5ef79c96f712163188ca63"
	if m := tpmcheckSaid.FindStringSubmatch(cut); m != nil {
		t.Errorf("a line still being printed reads as %q", m[1:])
	}
	for _, end := range []string{"\n", "\r\n"} {
		m := tpmcheckSaid.FindStringSubmatch(cut + rest + end + "# ")
		if m == nil || m[3] != "ed58648584d5cbd1ca321952caf5ea8e7d433"+rest {
			t.Errorf("the whole line ended by %q reads as %q", end, m)
		}
	}
	if m := tpmcheckSaid.FindStringSubmatch("SPIN-TPM rsa3072=b5 pcr16=62 nv=none\r\n"); m == nil || m[3] != "none" {
		t.Errorf("an unwritten index reads as %q", m)
	}
}

// tpmcheck log's last line as a poll can cut it, after the count's first digit and in the banks:
// no answer until the line ends, and then the whole count and every bank (#788).
func TestATPMLogLineIsReadOnlyWhenItEnds(t *testing.T) {
	const line = "SPIN-TPM-LOG events=12 replayed=0x4,0xb,0xc,0xd"
	for _, cut := range []string{"SPIN-TPM-LOG events=1", "SPIN-TPM-LOG events=12 replayed=0x4,0x", line} {
		if m := tpmLogSaid.FindStringSubmatch(cut); m != nil {
			t.Errorf("%q, a line still being printed, reads as %q", cut, m[1:])
		}
	}
	for _, end := range []string{"\n", "\r\n"} {
		m := tpmLogSaid.FindStringSubmatch("SPIN-TPM-PCR bank=0xd pcr=9 value=ab\r\n" + line + end + "# ")
		if m == nil || m[1] != "12" || m[2] != "0x4,0xb,0xc,0xd" {
			t.Errorf("the whole line ended by %q reads as %q", end, m)
		}
	}
}
