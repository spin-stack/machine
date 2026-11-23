// SPDX-License-Identifier: Apache-2.0

package gates_test

import (
	"testing"

	"github.com/spin-stack/go-tools/attestgate"
)

// What a release publishes is attested with its provenance first, in the same job: go-tools'
// attestgate over every workflow. Every step that publishes runs after an unconditional provenance attestation in its job; the
// ones that look like an upload and ship nothing anyone installs are named here, each with why.
// A step runs a task or a verb, so the ones that upload and the one that attests are named too: the
// YAML says only their names.
func TestEveryUploadIsAttested(t *testing.T) {
	g := attestgate.Gate{
		Root:        root,
		RequireSBOM: true,
		MainOnly:    []string{"release.yml"},
		UploadTasks: []string{"spin-machine-dev publish qemu", "spin-machine-dev refresh builders"},
		AttestTasks: []string{"spin-machine-dev release sign"},
		NotPublished: map[string]string{
			"ci.yml/fuzz/Keep the failing inputs":                                              "the inputs a fuzz target failed on, for a person to commit with the fix",
			"ci.yml/gate/Upload Go coverage HTML":                                              "a coverage page for a person, kept seven days",
			"codeql.yml/analyze/Keep upstream's results in the touched files":                  "upstream's analysis results, for a person to read; nothing installs or verifies them",
			"codeql.yml/analyze/Keep the SARIF":                                                "a failed scope's SARIF, for a person to read why; nothing installs or verifies it",
			"parts.yml/kernel/Upload the kernel":                                               "a lane's kernel for a person to fetch; a release builds its own",
			"lab.yml/lab/Keep the result":                                                      "a measurement's result.json, which lab show reads; nothing installs or verifies it",
			"parts.yml/qemu/Upload the extracted binaries":                                     "a lane's QEMU for a person to fetch; a release builds its own",
			"parts.yml/qemu/Publish the runtime image":                                         "what ci.yml's argument check pulls (spin-machine-dev fetch qemu); a release compiles QEMU, it never takes this",
			"refresh.yml/builders/Make the builders and propose the ones whose packages moved": "a build's input, pinned by digest in the pull request that takes it; a release ships what is compiled on it, attested",
			"sanitize.yml/boots/Keep the sanitizers' logs":                                     "a sanitized QEMU's reports, for a person to read; nothing installs or verifies them",
			"sanitize.yml/fuzz/Keep the crashes and the sanitizers' logs":                      "a fuzzer's crashing inputs and reports, for a person to reproduce; nothing installs or verifies them",
			"release.yml/trace/Keep the trace":                                                 "where the release's time went, for a person: nothing installs or verifies it",
			"report.yml/report/Keep the ladders":                                               "each release's ladder.json, every sample, which lab show reads; for a person",
		},
	}
	if err := g.Check(); err != nil {
		t.Error(err)
	}
}
