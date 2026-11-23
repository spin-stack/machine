---
date: "2026-11-21"
order: 360
area: boot
title: 'a contended checkpoint loop, how a qemu bump is checked for #421: `boot:finish-migrate` (#425, this change)'
issues: [421, 425]
---
#424's wait for QEMU to leave `finish-migrate` was held in the tree only by a fake QEMU, and an idle host does not reach the window, so boot:record passed with the fix and without it. *Built:* `TestACheckpointIsContinuedAtOnceOnABusyHost` (`task boot:finish-migrate`), the scratch loop of #421: spin's shape plugged to 2 GiB with a guest rewriting 600 MiB of tmpfs, 300 checkpoints each continued at once, any refusal a failure. The target runs the test and QEMU under `taskset` on the host's last two CPUs, and the test keeps each of the CPUs it is given busy with a spin loop and refuses to run on more than four. It is not in CI. The qemu entry's note in versions.yaml names it beside boot:record and boot:compat, so a QEMU that reorders `migration_completion_end` and `migration_iteration_finish`, or adds a run state after `completed`, is caught at the bump. *Measured* (laptop, real KVM, v20261121.01 with this checkout's spin-machine): with `finished` made to return at once, 39 of 300 continues were refused, and 0 of 300 with it. QEMU alone pinned to the busy CPUs, the test free, refused 0 of 300 without the fix, which is why the test runs on them too. *Status:* in place.
