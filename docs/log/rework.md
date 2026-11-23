## 2. Rework cycles

Each cycle: the sequence, what each step measured, and the lesson.

### 2.1 udev: masked, unmasked, then argued over for weeks
1. Initial import: udev masked "to save boot time" (`8aee03a`). The serial getty could not start, so a debug init was written to work around it (`5e7d60e`, `8aee03a`, `24c8a06`).
2. 09-10 `4cbd229`: masking buys nothing (826 vs 829). The first harness had left `systemd-udev-trigger` masked and read "udev changes nothing".
3. 09-10 `722d468`: unmasked. `b5f361f`: the harness's udev row was itself a no-op.
4. 09-10 `064c4f4`: unmasking udev quietly enabled the serial getty that the console unit existed to avoid. This was left open as "a decision".
5. 09-26 `adc52cd`/`e2f6b8f`: udev's 46 ms "overlaps" the boot; removing devices, rules or VT gives 0.
6. 11-12 `3c8da99`: the memory half of the hot-add rule, added 09-10, raced the kernel and was removed.

**Lesson:** an optimisation imported without its measurement survived 3 days and cost a debug init, a console unit and a harness. Measure before inheriting a mask, and make a variant fail when its edit matches nothing (`e2f6b8f` added that check).

### 2.2 logind: out of the boot, then back in
1. 09-10 `6aba37b`: logind 63 ms on the chain, worth 24 ms to the console prompt (laptop).
2. 09-12 `bb6bb89` (#5): logind is started at the first login through the seats directory, worth 24 ms (249 vs 273). Two dead ends were recorded, and `boot:logind` was written to hold it.
3. 11-12 #240: back in the boot, with spin lingering. First SSH login 570 → 510 (lab 43308825692). The comment says the 09-12 change "moved logind, dbus-broker and user@1000.service into the first login instead, which is what a workspace waits for."
4. 11-13: the side effect, console prompt +77 ms, was fixed with getty Type=simple (#241), and that fix was removed the same day (#249).
5. #240's notes: the `SPIN_LOGIND_NO_SEATS=1` control "was written to fail, and on this image … it passes". `10-seats.conf` may now be unnecessary. Not settled.

**Lesson:** the 09-12 decision optimised the console prompt, a metric nobody waits for, and paid for it in the first login, which was not measured until 11-12. Name the metric that matters before optimising. The first SSH login probe arrived two months after the change it would have rejected.

### 2.3 Serial getty Type=idle / Type=simple
1. 09-10 `80f984b`: removing Type=idle was within noise against agetty's `sleep(1)`.
2. 09-10 `064c4f4`: Type=idle worth 25 ms (291/344 vs 266/283), read as "systemd's job queue draining".
3. 09-26 `b9707e3`/`7b08e5a`: Type=idle was eliminated as the cause of the 340 ms console-swap cost.
4. 11-13 #241: Type=simple shipped, 346/358 vs 423/441, after linger made the idle hold 91.8 ms.
5. 11-13 #249: removed, since the console prompt is not a workspace metric.

**Lesson:** the value of this setting depends on what else is in the boot transaction. It was re-measured each time something upstream changed (linger), and its worth turns on a metric (the console prompt) that does not matter (#249). Decide which metrics count before shipping fixes for the others.

### 2.4 SSH host key generation (moved 5 times)
1. 09-07 `8aee03a`: ssh fails five times per boot; "SSH keys are identity and belong wherever the rest of a VM's identity comes from" (not fixed).
2. 09-07 `2df3dbf`: generated per boot, hung off ssh.service. Hanging it off ssh.socket made an ordering cycle.
3. 09-24 `a01c847`: RSA dropped (309 ms in blame).
4. 11-12 #240 `28823fb`: `sshd@` must wait for the keys. Before this, SSH over vsock "worked not at all"; nothing had tested vsock SSH since the socket path changed.
5. 11-12 lab 43312419007: keygen wanted by multi-user.target, 518 vs 514. Nothing gained, because default dependencies kept it after basic.target. Not shipped.
6. 11-13 #247 → #248: wanted by sysinit.target with `DefaultDependencies=no`, 489 → 468 (lab 43483372488). Consumer consequence: a checkpoint taken after boot always carries host keys, so the restorer must re-key (`image/README.md`).

**Lesson:** the per-connection vsock path ran without host keys until a probe logged in over it. A functional probe of the real login path (`boot:ssh-first`) should come before timing work on it. Step 5 failed for a reason (`DefaultDependencies`) that reading the unit's ordering would have shown.

### 2.5 Save/restore and templates in the CLI; restore measurement; mapped-ram (proposed twice)
1. 09-07 `656d55b`: save/resume added; reverted the same day as lifecycle (`b882171`).
2. 09-26 `20e14d9`/`ab4844c`: TEMPLATES.md, template restore 24 ms, `drop-cache=off`.
3. 09-28 `c202568`: `save --template`/`restore`, plus `boot:restore`.
4. 09-30 #51: templates removed upstream (spin #194). `boot:restore` deleted, two days after it was written.
5. 11-13 #242: restore measured again, and mapped-ram proposed with `--channels`. Its numbers (lab 43364516874) were contaminated by leaked VMs.
6. 11-13 #246: mapped-ram in the contract. The fleet shows no win (385 vs 386); closed.

**Lesson:** the product's path (template restore, then checkpoint resume) changed outside this repository, and probes were built and deleted around it within days. Restore of a checkpoint was not measured at all between 09-30 and 11-13. The mapped-ram decision had a fleet result and was reopened with a new reason: on 11-19 `machine.VM` writes every checkpoint in mapped-ram (`FormatMappedRAM`), and on 11-20 multifd channels on it took the restore under 100 ms (#320). A restore with more RAM is still unmeasured (#242's checklist).

### 2.6 Fingerprint: what it hashes (rewritten ~8 times)
1. Binaries plus shape (09-07).
2. Plus device topology, `b476f6d`.
3. Topology corrected to exclude disks and NICs, `b882171`.
4. NIC count and MTU back in, `6599829`/`2b46d78`.
5. Root ports, then virtio-scsi, in.
6. Firmware by content in, `f2f4da0`.
7. Templates removed, `a9d6083`.
8. One hash, `c5baaa7`.
9. **Contract, not binaries**, `fa3041c` (10-13), after the QEMU 11.1.2 bump stranded every checkpoint (`f0ec1e0`).
10. **No hash at all** (11-21): a record read from the running QEMU, compared part by part (`QEMU.Target`, `CanResume`).

Each step moved every fingerprint. Comments that said "a QEMU or kernel bump strands checkpoints" survived until `e452afc` (11-12). The verdict tool was blind to `machine/` changes until #98.

**Lesson:** a prediction of the machine has to be kept honest by a test of its own (boot:roms) and still cannot say what differs. Since 11-21 the record is asked of QEMU, and a refusal names each difference. Check claims about which checkpoints resume against `CanResume`, boot:record and boot:compat, not against older comments.

### 2.7 Console unit (spin-machine-console.service)
Sequence: created 09-07 (`24c8a06`); made redundant by udev 09-10 (`064c4f4`); measured 340 ms worse as a swap; TERM=dumb fixed it, 09-26 (`7b08e5a`); "viable", but the exposure question was left open; deleted 11-12 (`3a7864d`).

**Lesson:** two months of a unit nothing enabled. Delete as soon as nothing references a unit.

### 2.8 Debug init and diagnostic initrds
`cmd/spin-machine-init` was created 09-07 and deleted 09-10 (`8eeb01c`). Diagnostic initrds came back as `boot/probeinit` (09-29, `a81b6b5`), now allowed by CLAUDE.md as isolated diagnostics.

**Lesson:** the rule ("no init in a release") was clarified rather than broken. AGENTS.md no longer carries that rule. Its "Experiments" section keeps diagnostic initrds apart from release outputs.

### 2.9 Probes added then deleted (short-lived)
- `boot:matrix` (09-10 → 09-10)
- `kernel/minimal` + `boot:floor` (09-26 → 09-30)
- `boot:kernel-pages` (09-30 → 09-30)
- `boot:memory-unplug` (09-30 → 09-30)
- cache bench (09-11 → 09-30)
- `boot:restore` (09-28 → 09-30)
- `boot:unitload` (09-26 → 10-01)
- `boot:memory-policy` (09-28 → 10-01)
- page-cache sub-probes (09-30 → 10-01)
- bench rows (09-10 … → 10-01)
- console-swap variants (09-26 → 09-30)
- `boot:exits`, `boot:mmio`, `boot:jobs` (11-12 → 11-13, #249)
- `boot:bench`, `boot:trace`, `boot:userspace`, `boot:systemd`, `boot:ssh-load` (→ 11-13, #250)

This is the cycle AGENTS.md asks for ("Remove an experiment once its question is answered"). The cost is that the answers live only in commit messages and comments.

**Lesson:** this log, or `docs/memory-and-disk.md`-style records, is where an answered probe's result should land. Before writing a probe, check docs/boot-time.md's "Measured and rejected" and the row comments in `internal/lab/sshfirst.go`.

### 2.10 /tmp and tmpfiles; erofs; BPF_LSM; chrony
- **/tmp:** masked by the imported optimizer; unmasked 09-26 (`f36ae13`, noise); capped at a quarter 09-28.
- **erofs:** removed from the kernel 09-26 (`5a3d509`); the build assertion relaxed 09-30 (#66) for the pmem/DAX experiment, which was rejected (#63, #65).
- **BPF_LSM:** off 09-07 ("a boundary inside the boundary"); on 10-01 (`1400f6a`), and the stale note was fixed 10-05 (`ebe2a6e`).
- **chrony:** added 09-07, removed 09-10.

**Lesson:** the imported scripts carried decisions without numbers. Each has been re-decided one at a time.

### 2.11 CI cache and runners (rebuilt ~5 times)
- gha cache that stored nothing (`c767cd5`, 09-15);
- Blacksmith (09-20);
- the builder's silent fallback (`aa157cf`/`1ef95d1`, 09-26);
- export from main only (`bdec6cc`);
- GitHub runners for builds plus the bench/kvm fleets (10-24);
- the gha cache unreadable on the fleet, so a registry cache (`1666cb5`, 11-12).

Also the reusable builder, added and reverted on 10-13.

**Lesson:** each move was taken without checking that the cache was actually hit. A cache-hit assertion would have caught `c767cd5` and `1666cb5` on day one.

### 2.12 apk locks broke three times
`6e7ba47` (11-03), `8d495c5` (11-09), release run 43157106109 / `ec2c7fb` (11-12); fixed by a daily refresh (`c6c29b5`), which then compiled QEMU cold twice every move. It was fixed structurally on 11-20 by pinning builder images instead of packages.

**Lesson:** a mirror that serves only the newest build cannot be pinned. Pin something that keeps its contents, such as an image, and move it when the move is wanted.
