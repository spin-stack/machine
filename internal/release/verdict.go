// SPDX-License-Identifier: Apache-2.0

package release

import (
	"fmt"
	"strings"
)

// FingerprintKeys are the manifest's keys that decide whether a checkpoint resumes: the machine's
// contract, machine.Spec.Fingerprint, of one fixed machine. **Checkpoints carry over if and only if
// it did not change.**
//
// The files' own checksums - qemu, kernel, bios, pvh, image - are deliberately not what decides it.
// A newer QEMU of the same machine type is another emulator of one machine (QEMU's migration
// promise, which boot:compat holds a release to); a resumed guest runs the kernel in its saved
// memory; the BIOS travels in a checkpoint and only its size is the machine's; pvh.bin and the
// base image are read by a boot.
var FingerprintKeys = []string{"fingerprint_sha256"}

// ComparedKeys are the manifest's keys a verdict compares: the fingerprint, and what is said
// beside it.
var ComparedKeys = []string{"qemu_sha256", "kernel_sha256", "bios_sha256", "pvh_sha256", "fingerprint_sha256", "image_sha256"}

// Verdict says, in Markdown for the release's notes, whether the machine described by now is still
// the one the checkpoints taken against reference were: then is reference's manifest, nil when it
// published none. It is not a gate - changing the machine is a thing this repository does on
// purpose, and a check that failed on it would be one people learn to force through - but what
// changing the machine is not allowed to be is silent.
func Verdict(reference string, then, now map[string]string) string {
	var b strings.Builder
	say := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	if reference == "" {
		say("Nothing to compare against: this is the first published release.")
		return b.String()
	}
	if then == nil {
		say("Cannot say whether checkpoints carry over: `%s` published no `machine.env`", reference)
		say("to compare against. Releases from this one on do.")
		return b.String()
	}
	var movedFP, movedOther, checked, unchecked []string
	for _, k := range ComparedKeys {
		fp := isFingerprint(k)
		before, was := then[k]
		after, is := now[k]
		if !was || before == "" || !is {
			// Not compared: said plainly below rather than folded into a verdict, because a
			// comparison that silently skipped an input is the mistake this exists to prevent.
			if fp {
				unchecked = append(unchecked, k)
			}
			continue
		}
		if fp {
			checked = append(checked, k)
		}
		if before == after {
			continue
		}
		if fp {
			movedFP = append(movedFP, k)
		} else {
			movedOther = append(movedOther, k)
		}
	}
	detail := func(keys []string) {
		for _, k := range keys {
			say("- `%s`", k)
			say("  - was `%s` (%s)", then[k], reference)
			say("  - now `%s`", now[k])
		}
	}
	switch {
	case len(movedFP) > 0:
		say("### ⚠️ Every checkpoint taken against `%s` stops resuming on this machine", reference)
		say("")
		say("The machine's contract moved - its machine type, its shape, its devices or its BIOS's")
		say("size (machine.Spec.Fingerprint) - so a host resuming a checkpoint saved")
		say("against `%s` on this machine is doing something undefined rather than", reference)
		say("something that fails.")
		say("")
		detail(movedFP)
	case len(unchecked) == 0:
		say("### Checkpoints taken against `%s` still resume on this machine", reference)
		say("")
		say("`fingerprint_sha256`, the machine's contract, is unchanged: its machine type, shape,")
		say("devices and BIOS size are the same, whatever else below moved - a new QEMU of the same")
		say("machine type, a new kernel or firmware a boot reads. Each VM's own sizes come from")
		say("whoever runs the guests and are untouched by a release.")
	default:
		say("### Nothing compared here moves the fingerprint")
		say("")
		say("Unchanged since `%s`: %s.", reference, orNothing(checked))
		say("")
		say("Not compared, and therefore not vouched for: %s. This is a partial", strings.Join(unchecked, " "))
		say("answer — only a release compares the whole machine.")
	}
	if len(movedOther) > 0 {
		say("")
		say("Changed, and not part of the fingerprint — checkpoints are unaffected:")
		say("")
		detail(movedOther)
	}
	return b.String()
}

func isFingerprint(k string) bool {
	for _, f := range FingerprintKeys {
		if f == k {
			return true
		}
	}
	return false
}

func orNothing(keys []string) string {
	if len(keys) == 0 {
		return "nothing"
	}
	return strings.Join(keys, " ")
}
