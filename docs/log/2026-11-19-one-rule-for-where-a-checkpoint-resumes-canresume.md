---
date: "2026-11-19"
order: 90
area: machine
title: 'one rule for where a checkpoint resumes: `CanResume(Saved, Target)` (this change, functional)'
prs: [293]
---
After #293 a checkpoint recorded its format and CPU, but the machine it was of was still spin's `machine_identity` string, and nothing recorded the QEMU that wrote it: QEMU loads what an older QEMU of the same machine type wrote and promises nothing the other way, so during a rolling update a checkpoint from an updated host could be placed on one not yet updated with an identical fingerprint (spin-stack/spin#1348). `Saved` now carries `Fingerprint` and `VMM` ("qemu 11.1.90"), a machine says what it resumes (`VM.Target`: formats, fingerprint, VMM, CPU), and `CanResume` is the one rule a placement and `Resume` both ask: the format read, the same fingerprint, the same VMM at the writer's version or newer, the CPU shown again. `Args` names the machine by its fingerprint (`-name guest=spin-machine:<fingerprint>`, read back with `query-name`), which the guest does not see and `Contract` does not hash. Records from before a field was kept are not checked on it.
