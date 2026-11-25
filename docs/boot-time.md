# Boot time: where it stands

This page follows the goal's two numbers (AGENTS.md, "The goal"). It says where each stands and where
its time goes. It lists what the machine carries for them and what each
item earns, what was tried and rejected, and what is open: those tables are one record a row in
`docs/experiments`, rendered by `spin-machine-dev docs tables` and on
[the site](https://spin-stack.github.io/spin-machine/experiments.html). The log, `docs/log`,
tells each change, dated.
Each release's own numbers are in `docs/releases.md`. What the guest's memory and disk cost is in
`docs/memory-and-disk.md`. How a number is measured is [measuring.md](measuring.md), and the lab's
commands are in `docs/cli.md`, "Measuring".

Every number here comes from the bench fleet (spin-stack/ci: AWS c8id.4xlarge, Xeon 6975P-C,
nested KVM, scratch on local NVMe) in spin's shape, as p50 or p50/p95 in ms, unless a row says
otherwise. An interval is the bootstrap 95% interval of a Δp50.

## Where the two numbers stand

| Number | Probe | p50/p95 | Over 100 ms | Run |
|---|---|---|---|---|
| Cold boot to the first SSH login | `ssh-first`: from before QEMU's exec to `true` run over SSH | 456/477 | +356 | report 44811347183, v20261120.04 |
| The same, QEMU started ahead | `prestart`, `prestarted boot`: from the request | 428/440 | +328 | the same |
| A checkpoint restored to the first login | `restore-first`, `evicted`: the state read from NVMe, out of the page cache | 108/111 | +8 | the same |
| The same, QEMU started ahead | `prestart`, `restored into prestarted` | 83.2/87.1 | under | the same |

**The cold number's probe stopped adding work on 11-22 (#450).** `ssh-first` used to close the
connection that timed `accepts` and log in on a new one, and on spin's path sshd started a session
for the closed one beside the first login. It now logs in over the connection accepted. In one
run, the probe's two versions as rows of one tree: accept to login −4.3 [−5.4, −3.0], first login
−4.4 [−9.1, +0.3] (bench, spin's shape and login, v20261122.01, 40 each, lab 44987441326). A
release measured with the new probe reads ~4 ms lower without the machine having changed.

These are the release's numbers, booted interleaved with v20261120.03 (`docs/releases.md`). Two
runs are two machines, so compare a new number only with one booted in the same run (`docs/measuring.md`,
rule 3).

**The restore is under 100 ms when QEMU is started ahead, and the cold boot is not close.** A
cold boot cannot reach 100 ms by userspace work alone: QEMU, the firmware and the kernel take
~140 ms before PID 1 runs (budget below), ~150 once two of its clocks are corrected (#665, #666). Without systemd, sshd accepted at 189 ms in spin's shape
(lab 44750322384, 2026-11-20). Every phase before PID 1 has to shrink as well.

### What spin runs, and what the probes boot

The probes boot and log in as spin does, unless a run asks otherwise (docs/cli.md, "Measuring"):

- **The shape.** spin (spin-stack/spin, `internal/runner/vmm/vmm.go`) boots 1 vCPU and 128 MiB,
  with ceilings of 16 vCPUs and 8 GiB. Its hotplug controller has 16 targets, 4 of them for disks
  attached while it runs and the rest for the extensions it boots with
  (`internal/runner/qemu/spec.go`, `controllerTargets`, since spin's c0ffbc0 of 2026-11-20). The
  probes boot 16 too. Against the 4 they booted before, 16 read within noise: first login −3.8
  [−11.1, +4.5] (lab 44847364468, 2026-11-21), so the budget's rows taken with 4 stand.
- **The memory.** spin sizes every workspace through virtio-mem right after it starts, to 2048 MiB
  unless the workspace asks for more (`installconfig.MinMemoryMB`, the least and the default). The
  probes plug 2048 (`plugged=2048`). A guest left at 128 MiB runs its OOM killer through every boot
  (lab 44784196287).
- **The login.** spin turns off systemd's vsock sshd (`systemd.ssh_auto=0`). It reaches a
  long-running `ssh.service` as root through its supervisor's forwarder to 127.0.0.1:22
  (`login=spin`). The image's own vsock lane is `login=lane`.
- **Checkpoints.** A checkpoint is mapped-ram, saved and loaded on 8 multifd channels
  (`machine.DefaultChannels`). Before the RAM load, the resume reads the state ahead.
- **The host.** spin's host must set THP defrag to `defer`, and spin does not set it
  (`docs/memory-and-disk.md`, "What the host must set"). The probes leave the bench host's policy
  as it is, unless `restore-together` is given `defrag=`.
- **The host key.** Before it execs systemd, spin's vminitd places the workspace's host key at
  `/run/spin/ssh/ssh_host_ed25519_key` with a drop-in naming it sshd's only `HostKey`, and the
  image's keygen then does not run (#515). `login=spin` does the same from a shell as PID 1, a
  stand-in that costs 6-8 ms of its own before systemd (docs/measuring.md, "The ledger"; #575).
- **Still not spin's.** spin boots `/sbin/vminitd` from its own initrd before systemd. Its
  supervisor starts `ssh.service` when it installs the user CA. The probes boot the image's init
  behind the handoff's shell.

Most numbers taken before 2026-11-21 used another shape (2 vCPUs, 2 GiB, no ceilings, the
"ladder" shape) and another login (the lane). The "Shape" column of each row below says which. A
row marked spin from before that date passed spin's shape options by hand (#319). Read a number
in the ladder shape as a lead in spin's shape.

### The bench fleet is nested, not metal

On a c8i.metal-48xl (the same CPU), the cold first login read 332 ms against 378-392 nested
(v20261114.02, ladder shape, lab 44537498507), so nesting costs ~50-60 ms. Most of that is VM
exits: the PCI walk takes 5.00 ms nested against 2.22 on metal, about 29 µs an exit against 13
(labs 44535161371, 44535156851). A production host that is not nested starts closer to the goal.

**Both hosts make the same exits, and the nested ones cost more** (#565, the `exits` probe,
v20261122.06, labs 45082640147 and 45082640186). A cold boot to the first login makes ~10,400
exits on either host: CPUID 3805, MMIO 3138, MSR writes 1169 (1043 of them the TSC deadline),
preemption timer 827, port I/O 667, interrupts 424, HLT 334, EPT violations 172. The nested L1
spends 78.7 ms on them against 48.3 on metal. Removing 1541 CPUID exits moved the first login
−10.3 ms, ~6.7 µs an exit. EC2 gives the L1 EPT, VMCS shadowing and APICv, all on, so no L1
setting is left to turn. Every exit removed counts at ~5-15 µs nested. #565 ranks the levers.

**The EPT violations are half guest RAM first touched, half MMIO first touched** (#568, `exits`,
v20261123.04, lab 45111606645, 3 boots, the same counts in each). Of 171 at ~110 µs each nested
(18.9 ms), 52 land in the 128 MiB base and 6 just above it, 48-51 in the memory virtio-mem plugs
from 4 GiB, and ~65 at MMIO addresses, where KVM's first fault on a page installs the entry that
every later access exits through as EPT_MISCONFIG: 39 in the PCIe ECAM at 2.75 GiB, 9 below 4 GiB
(the IOAPIC, the local APIC, the flash), 17 in the 64-bit BARs at 12 GiB. Prefaulting the base
in a QEMU started ahead could win at most the base's ~58 × 110 µs, ~6 ms, of `prestart` alone,
since `ssh-first`'s clock starts before QEMU's exec. That is under the noise and off both goal
numbers, so it was not built.

**Density: a host's first login holds until about a third to a half of its CPUs are busy** (#563,
section 2, `density`, v20261123.04, spin's shape and login, 2026-11-23). Each row boots that many
workspaces first, logs into each, lets them settle 10 s, and times one more's first login. Under
`load=cpu` every crowd workspace keeps its one vCPU busy. The line #563 reads is a first login p95
within 25% of the empty host's in the same run.

| host | load | crowd: first login p50/p95 | within 25% | runs |
|---|---|---|---|---|
| nested c8id.4xlarge, 16 CPUs | cpu | 0: 387/417; 4: 378/431; 8: 441/450; 16: 1015/1215; 24: 941/1196 | up to 8 | lab 45110655407, 10 a row |
| metal c8i.metal-48xl, 192 CPUs | cpu | 0: 337/447; 16: 331/488; 32: 335/344; 48: 336/341 | up to 48 | lab 45110663095, 10 a row |
| the same | cpu | 0: 342/363; 64: 327/366; 96: 466/486 | up to 64, not 96 | lab 45110670764, 8 a row |
| the same | idle | 0: 345/368; 16: 336/343; 32: 329/341; 48: 334/352 | all | lab 45110678888, 10 a row |
| the same | idle | 0: 342/356; 64: 328/350; 96: 331/340 | all | lab 45110688511, 8 a row |

Idle workspaces cost a metal host nothing up to 96, as they cost the bench nothing up to 16 (lab
44667235868). Busy ones moved the first login at 16 of the bench's 16 CPUs, and at 96 of metal's
192 (+124 ms p50). The metal empty row's p95 of 447 in lab 45110663095 is one slow boot of ten.
Between 64 and 96 is unmeasured. The crowd boots one at a time, ~0.8 s each on metal's EBS
scratch and ~0.75-1.1 s on the bench's NVMe. Section 1 of #563, what nesting costs a running
workspace's I/O, needs `io` and `build` probes and equal storage on both hosts, and is open.

## Where the time goes

Each table splits one number into phases, in spin's shape, and gives each phase a source. A phase
"by difference" is two numbers of one run subtracted. A phase marked "approximate" comes from
another run or another release. The last column is one way to spend 100 ms. It is an allocation,
not a measurement: it says how far each phase has to move.

### Cold boot

| Phase | p50 | Source | How well known | For 100 ms |
|---|---|---|---|---|
| QEMU's exec to a guest ready to run | ~28 | cold 456 less the boot into a QEMU started ahead, 428 (report 44811347183) | by difference, one run | 0, started ahead |
| … of it, QMP: QEMU's main loop free to `cont` answered, 14 commands | 6.2 | `transport` `cold_qmp`, lab 44912471522 (v20261121.01); 4.5 of it QEMU waiting on spin-machine's next command | measured | |
| … of it, the reset copying the vmlinux's 37.4 MB of segments into guest RAM, before the main loop | 9.1 before QEMU 0009, now beside the main loop | `romcopy` (a probe on branch `exp/698-romcopy`, not on main) `rom_copy`, lab 45257214350 (v20261123.07, 20 boots, p95 9.3); the ledger read the vmlinux's QEMU start +8.4 against a bzImage's (lab 45260129735, #697) | measured | |
| qboot and the bzImage's decompression, to the kernel's clock | 33.6 | `boot:firmware-stages`, lab 44787491631 (v20261120.02's kernel); ~21 of it is the decompression KASLR needed, which the machine no longer runs since #645 (first login −18.6, "What the machine carries"). Its end, KVM_SET_CLOCK plus the sched offset, is 2.8 ms after the kernel's 0 on bench, so ~31 (#665) | measured, another run; its anchor late | 5 |
| The kernel's clock to "Freeing unused kernel image" | 79.9 | report 44811347183, on dmesg's clock, which from late_initcall on is 13-15 ms behind the kernel's on bench, so ~93 (#666) | measured on a clock that reads short | 25 |
| PID 1 to sshd accepting | ~255 | sshd accepts 396 (lab 44841266997) less the three rows above; ~244 with the two corrections above | by difference, across runs | 35 |
| sshd's accept to the first login | 68.4 | `ssh-first` `accept_to_login`, lab 44841266997 | measured | 35 |
| … of it, what any login costs (a second login) | 35.9 | `ssh-first` `second_login`, the same | measured | |
| … of a login, vsock: 10 round trips of 57.5 µs through the forwarder | 0.6 | `transport` `login_turns`, `forward_rtt`, lab 44912471522 | measured | |
| **Total** | **465** | lab 44841266997, v20261120.04 (456 in the report's run) | | **100** |

**PID 1 to sshd is more than half the cold boot.** On one vCPU, everything systemd starts before
sshd accepts is on the login's path. The groups in "What the machine carries" show how much
masks, generators and the coldplug filter took off it. The no-systemd floor accepted at 189 ms in
spin's shape. Before PID 1, ~140 ms of QEMU, firmware and kernel remain (~150 corrected, #665, #666), ~113 of them with QEMU
started ahead. Each phase from the first row to the fourth has to shrink to reach 100 ms.

### Restore, QEMU started ahead

| Phase | p50 | Source | How well known | For 100 ms |
|---|---|---|---|---|
| spin-machine's exec and its dial | 2.3 | `split` (a probe on branch `restore-split-lab`, not on main), lab 44809015139 (v20261120.03) | measured, one run | 1 |
| QMP round trips to `migrate-incoming` (17 of them) | 6.7 | `transport` `restore_qmp`, lab 44912471522 (v20261121.01); 5.2 of it QEMU waiting on spin-machine. The read-ahead runs beside it | measured, one run | 3 |
| The RAM load (8 channels, 181 MiB of data, evicted) | 38.8 before the read-ahead; ~27 after it | the same; the read-ahead −11.9 to running (lab 44809512339) | measured, then by difference | 25 |
| Device state and `cont` | 4.5 | the same | measured, one run | 3 |
| … after it, the guest running to the restore's last answer (`remove-fd`, `query-status`) | 2.7 | `transport` `restore_after`, lab 44912471522 | measured, one run | |
| Running to the first login | ~35 | 83.2 less running (~47); laptop: the connection ~5, to "Accepted publickey" ~22, the session ~4 | by difference; split on a laptop | 35 |
| **Total** | **83.2** | report 44811347183 | | **67** |

A restore that starts QEMU itself adds ~25 ms (108 against 83.2). From the page cache, the RAM
load is ~10 ms instead of ~50 on a laptop, so the evicted load is the disk. Eight restores let go
at once read ~190-240 ms each under `defer`, against ~100 alone ("Open experiments", the burst).

### Restore, QEMU started by the restore

This is the goal's second number, `restore-first`'s evicted row. The rows come from lab
44976494418 (v20261122.01's parts, main's spin-machine at ee1be0f, 30 restores).

| Phase | p50 | Source | How well known |
|---|---|---|---|
| The monitor's socket listening | 2.5 | `listening`. `machine.Start` binds it before QEMU exists, so it no longer marks QEMU's start | measured |
| QEMU's exec to its first answer | ~19.5 | `transport`, lab 44912471522 (v20261121.01). `spin-machine restore` waits on it in its dial | measured, another run |
| The first answer to `spin-machine restore` returning: 17 QMP commands, the load, device state, `cont` | ~49 evicted, ~35 cached | `running` 70.8 evicted and 57.0 cached, less the two rows above | by difference |
| Running to the first login | 40.2 | `login` 111 less `running` 70.8 | by difference |
| **Total** | **111** evicted, 95.2 cached | the same | |

The evicted row read 16 ms more than the cached one, and that was the disk. The read-ahead
started in `Resume`, after the dial had waited ~19.5 ms for QEMU. Started before the dial, it
runs beside QEMU's start: `running` 62.7, login 101, 6 ms over the cached row ("What the machine
carries"). Asked for by four goroutines instead of one, the evicted row is level with the cached
one, login 96.2 against 96.3 (lab 44976924478). What is left of the restore is not the disk.

**About a third of the restored login is sshd sleeping.** On a laptop, a restored login of
32 ms split into 4.6 ms to the server's banner, ~5 ms of key exchange, 14.5 ms from the signed
`userauth` request to its success, and ~4.7 ms for the channel, `exec` and exit. ~12 ms of the
14.5 are OpenSSH's minimum time for a userauth request (`auth2.c`, `ensure_minimum_time_since`):
5 ms plus 0-4.2 ms after the key query, and again after the signed request that succeeds, because
the flag it checks is set only after the sleep. sshd logs both at `LogLevel DEBUG3`. With every
`nanosleep` in sshd returning at once, a bound and not a candidate, the restored login read −13.4
[−15.2, −10.5] on the bench and the cold login −3.2 [−13.7, +2.7], within noise (lab
44980029214). On a cold boot the vCPU still has boot work to run while sshd sleeps. ("Open
experiments", #457.)

## What the machine carries

This table lists every change the tree makes for boot time, with what it earns in spin's shape.
It is the current state. The log tells how each change came in. A row's evidence is one of
these:

- *measured*: its own arm, in spin's shape;
- *group*: an arm that took back its group (below);
- *ladder*, *lane*, *runner* or *laptop*: measured in another shape, on another login, on the lab
  runner of 2026-09 (Ryzen 9 5900X), or as a lead;
- *mechanism*: what it removes is visible, but no goal number has timed it;
- *unmeasured*.

A row that is not *measured* or *group* is a candidate for deletion, which rule 5 of
`docs/measuring.md` decides.
Functional rows (security, correctness, a consumer's contract) are listed so that nobody takes
them for boot time.

The rows are the records in `docs/experiments` with `"decision": "carried"`, rendered by `spin-machine-dev docs tables` and [on the site](https://spin-stack.github.io/spin-machine/experiments.html#what-the-machine-carries).

**What holds each patch.** Every patch the table names, and QEMU's 0003 and 0004, which are not
for boot time, has a test that fails when the patch is lost or rebased into doing nothing, since
either still builds and boots. QEMU's are in qemu/README.md ("The patches"), qboot's in
qemu/qboot/README.md, the kernel's in kernel/README.md. OpenSSH's is held by its build, which
looks for the patched condition in the `auth2.c` it compiles, and by `boot:sshd-delay`. The qemu,
qboot, kernel and openssh-source entries of `versions.yaml` name the checks a bump runs.

**What only the lane uses.** spin's path earns nothing from `sshd-vsock.socket.d/10-lane.conf`,
`sshd@.service.d/10-lane.conf`, `sshd@.service.d/keygen.conf` or the masked
`systemd-nologin.conf`. The probes still offer `login=lane`, and the no-systemd floor uses it.
Whether the image keeps a vsock login of its own is the maintainer's decision ("Open questions").

**What spin does, not the image.** spin no longer lingers its user (cold −36.1 [−45.8, −23.6],
labs 44785179901, 44785565313). Its supervisor's forwarder order is the proposal in "Open
experiments".

### How the groups were decided

The groups were decided by one `prestart` run in spin's shape, 20 boots each, against
v20261120.04 as published (lab 44818953987). The base read 453/464 cold, 124/144 restored and
93.0/120 restored into a QEMU started ahead. Each cell is the Δp50 of taking the group back.

| Arm | Takes back | Tree | Cold | Restored | Into prestarted |
|---|---|---|---|---|---|
| A | every kernel patch | `--kernel run:44812049304` | +67.5 [+62.5, +72.4] | +8.1 [−8.4, +20.1] | +12.1 [−6.5, +21.2] |
| A1 | 0006, 0009, 0011 | `--kernel run:44812044088` | +1.2 [−4.5, +8.3]; at 40 boots −2.5 [−8.4, +1.8] | −5.2 | +1.3 |
| A2 | `DEFERRED_STRUCT_PAGE_INIT` | `--kernel run:44837940315` (lab 44839538785, 40 boots) | −2.5 [−9.3, +3.8] | +1.1 | −4.0 |
| A3 | the loop and 8250 counts | `--kernel run:44837906170` (lab 44839538785, 40 boots) | +5.3 [−3.2, +15.0] | +1.7 | −4.6 |
| B | QEMU 0001, qboot's `pam.patch` | `--qemu run:44812062232` | +11.5 [+6.7, +20.2] | +4.4 | +0.9 |
| C-masks | 16 unit masks | `--edit carry-masks` | +57.5 [+49.0, +63.0] | −0.7 | +0.1 |
| C-generators | 8 generators, 3 tmpfiles rules | `--edit carry-generators` | +16.7 [+11.6, +23.3] | −5.3 | −1.3 |
| C-logging | PID 1's quiet logging, the printk sysctl | `--edit carry-logging` | +2.4 [−2.3, +7.8]; at 40 boots −1.8 [−7.7, +3.2] | −4.0 | +0.6 |
| C-coldplug | the coldplug filter | `--edit carry-coldplug` | +14.9 [+8.2, +19.8] | +463.7 | +358.0 |
| E | `rcu_expedited`, `driver_async_probe` | `--append 'driver_async_probe= rcupdate.rcu_expedited=0'` | −1.4 [−4.9, +3.1] | +1.4 | +1.7 |
| F | qboot, for SeaBIOS | `--boot --bios=bios-256k.bin` | +68.5 [+62.7, +71.7] | −1.9 | +7.4 |

A1, A2, C-logging and E were deleted (#345, #356, #354, #352); `--edit carry-logging` went with `fast-boot.conf`, the last file it edited (2026-11-22). The rest earn what the cold column
says. On one vCPU, every unit, generator and exit before the login is on the login's path. The
`carry-*` edits stay in `internal/lab`, so the ablation can run again when the shape, the kernel or
systemd moves. An edit checks what it undoes, so an edit whose item was deleted fails on later
releases.

## Measured and rejected

An idea already here needs a new reason, not a new run. The tables group ideas by the part they
touch, oldest first within a group. Each is the records with `"decision": "rejected"` and the
section named under its heading. The "Shape" column says which shape a row was measured in,
and a row measured in another shape than spin's has such a reason.

### Kernel and its command line

Records with `"section": "kernel"` ([rendered](https://spin-stack.github.io/spin-machine/experiments.html#kernel-and-its-command-line)).

### Firmware, QEMU and the machine

Records with `"section": "firmware"` ([rendered](https://spin-stack.github.io/spin-machine/experiments.html#firmware-qemu-and-the-machine)).

### systemd and the image

Records with `"section": "image"` ([rendered](https://spin-stack.github.io/spin-machine/experiments.html#systemd-and-the-image)).

### The login

Records with `"section": "login"` ([rendered](https://spin-stack.github.io/spin-machine/experiments.html#the-login)).

### The restore and checkpoints

Records with `"section": "restore"` ([rendered](https://spin-stack.github.io/spin-machine/experiments.html#the-restore-and-checkpoints)).

### QMP and vsock

Both transports were measured in spin's shape on the bench, 20 boots, with the `transport` probe
(lab 44912471522, v20261121.01, 2026-11-21). Each ceiling is what the goal number would gain if the
part cost nothing. None is above the noise (±7-9 ms cold, ±13 restored at 20 boots), so none was
built.

Records with `"section": "qmp"` ([rendered](https://spin-stack.github.io/spin-machine/experiments.html#qmp-and-vsock)).

### Memory and the host

Records with `"section": "memory"` ([rendered](https://spin-stack.github.io/spin-machine/experiments.html#memory-and-the-host)).

## Open experiments

The rows are ranked by ceiling. A row stays here until it is decided, and then moves.

The rows are the records in `docs/experiments` with `"decision": "open"`, rendered by `spin-machine-dev docs tables` and [on the site](https://spin-stack.github.io/spin-machine/experiments.html#open-experiments).

The floor is a bound and never a candidate: systemd stays (AGENTS.md, "What must not break").

## Open questions

No lab run answers these questions.

- **Does the image keep a vsock login of its own?** spin does not use the lane (see "What the
  machine carries"). This is the maintainer's decision.
- **Is `10-seats.conf` still needed?** The control that should fail passes on systemd 259 (#240).
- **What seeds the guest's RNG without virtio-rng?** It has to be answered before the removal is
  measured.
- **musl under long-running load.** No run has measured it (`31a57e7`).
- **`page_reporting_order`.** Revisit only if memory per host becomes the limit
  (`docs/memory-and-disk.md`).
- **The kernel CVE judge.** 9 CVEs apply with a fix later in 7.3, and 1 has no fix (`0ce27ad`,
  against 7.3.1; the kernel is 7.3.5 on 2026-11-20).
- **The min_ttl_ms evidence disagrees.** `47aa681` and the LRU_GEN comment in `kernel/Dockerfile`
  say different things (docs/log/2026-09-28-f7cd1ab-47aa681-337c08d.md).
- **The guest clock on 09-30.** `1d5e7f5` blames chrony, which had left the image on 09-10
  (`c7af4d3`). The fix (time by `/proc/uptime`) stands either way.
