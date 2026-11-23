# Boot time: where it stands

This page follows the goal's two numbers (AGENTS.md, "The goal"). It says where each stands and where
its time goes. It lists what the machine carries for them and what each
item earns, what was tried and rejected, and what is open. `docs/log.md` tells each change, dated.
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
~140 ms before PID 1 runs (budget below). Without systemd, sshd accepted at 189 ms in spin's shape
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
| qboot and the bzImage's decompression, to the kernel's clock | 33.6 | `boot:firmware-stages`, lab 44787491631 (v20261120.02's kernel); ~21 of it is the decompression KASLR needs | measured, another run | 5 |
| The kernel's clock to "Freeing unused kernel image" | 79.9 | report 44811347183 | measured | 25 |
| PID 1 to sshd accepting | ~255 | sshd accepts 396 (lab 44841266997) less the three rows above | by difference, across runs | 35 |
| sshd's accept to the first login | 68.4 | `ssh-first` `accept_to_login`, lab 44841266997 | measured | 35 |
| … of it, what any login costs (a second login) | 35.9 | `ssh-first` `second_login`, the same | measured | |
| … of a login, vsock: 10 round trips of 57.5 µs through the forwarder | 0.6 | `transport` `login_turns`, `forward_rtt`, lab 44912471522 | measured | |
| **Total** | **465** | lab 44841266997, v20261120.04 (456 in the report's run) | | **100** |

**PID 1 to sshd is more than half the cold boot.** On one vCPU, everything systemd starts before
sshd accepts is on the login's path. The groups in "What the machine carries" show how much
masks, generators and the coldplug filter took off it. The no-systemd floor accepted at 189 ms in
spin's shape. Before PID 1, ~140 ms of QEMU, firmware and kernel remain, ~113 of them with QEMU
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
It is the current state. `docs/log.md` tells how each change came in. A row's evidence is one of
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

| Item | Where | Earns (Δp50 of taking it back) | Evidence | Run |
|---|---|---|---|---|
| Kernel patches 0001-0005, 0007, 0008, 0010 | kernel/patches | cold +67.5 [+62.5, +72.4], with 0006, 0009 and 0011 (since deleted, worth nothing) | group A | lab 44818953987 |
| Kernel 0012 (vmlinux BTF parsed on first use) and 0013 (the verifier asks for it only where a kernel type enters a program) | kernel/patches | cold −6.4 [−9.4, −1.0] at 60 a side (lab 45118194310, the kernel carried); before the review's fixes −10.0 [−12.7, −3.3] at 60 and −6.3 [−9.1, −4.6] at 120 (labs 45114405201, 45115330128); restored −1.0 [−6.0, +4.2] evicted, the state 1.3 MiB smaller (lab 45115341056) | measured | lab 45118194310 |
| `BLK_DEV_LOOP_MIN_COUNT=0`, `SERIAL_8250_RUNTIME_UARTS=1` | kernel/config-7.3-x86_64 | cold +5.3 [−3.2, +15.0]: inside the noise, but the upper bound does not rule out its ~2-4 ms ceiling | measured, 40 a side | lab 44839538785 |
| `KERNEL_LZ4` | kernel/config-7.3-x86_64 | the alternatives lost: an uncompressed payload, on QEMU 0007, −4.0 [−7.9, +0.3] at 40 a side, within noise, and QEMU's RSS +32 MiB (lab 45183618987); before 0007, +9 ms in copies (laptop) | measured, 40 a side | lab 45183618987, #326 |
| `VT=n`, `X86_MSR=n`, `X86_CPUID=n` | kernel/config-7.3-x86_64 | function: dead code, less surface | - | kernel/Dockerfile |
| qboot instead of SeaBIOS | qemu, `machine.DefaultBIOS` | cold +68.5 [+62.7, +71.7] | group F | lab 44818953987 |
| QEMU 0001 (device creation in one memory transaction) | qemu/patches | restored +11.7 [+0.9, +19.3], running +6.8 [+1.2, +8.8]: a restore starts QEMU too; cold +0.2 [−8.7, +7.1]. With `pam.patch`, cold +11.5 [+6.7, +20.2] (group B, lab 44818953987) | measured, 20 a side | lab 44993254745 |
| qboot `pam.patch` (PAM in three writes) | qemu/qboot | cold +3.9 [−3.4, +8.0]: inside the noise, and the upper bound over +5 keeps it (rule 5 of docs/measuring.md) | measured, 40 a side | lab 44993790452 |
| QEMU 0002, virtio-scsi `max_target`, which spin-machine passes | qemu/patches, machine | cold −8.9 [−15.5, −0.9] when it came in | measured | lab 44788216590 |
| QEMU 0007 (a bzImage given to `-kernel` mapped, not read into QEMU's heap, #646) | qemu/patches | first login −13.4 [−16.9, −7.3], sshd accepting −13.3 [−18.7, −7.5], 40 a side; restored −9.3 [−11.2, −6.1] evicted and −7.7 [−8.9, −5.1] cached (QEMU's start is on the restore's clock too); QEMU's anonymous RSS −15.9 MiB, its file RSS +16.1, the kernel now shared through the page cache. Twice the read's 6.2-6.9 ms traced in #611's split: what else the anonymous buffer cost nested was not measured. Prior art: upstream maps the initrd and, through PVH, an ELF kernel since QEMU 4.2 (Stefano Garzarella, [qemu-devel 2019-07-23](https://lists.gnu.org/archive/html/qemu-devel/2019-07/msg05164.html): `qemu_init_end` 63.85 → 55.91 ms for a 28 MB vmlinux and a 3.0 MB initrd), and [said](https://stefano-garzarella.github.io/posts/2019-12-22-qemu-kernel-initrd-mmapped) it was not available for a bzImage; [x86/loader: read complete kernel](https://lore.kernel.org/qemu-devel/20240905141211.1253307-4-kraxel@redhat.com/) (`214191f6b5`, merged 2024-12-16) made the read the whole file. No mapping of a bzImage found on qemu-devel (lists.gnu.org, lore, patchew) or in QEMU's history; Firecracker and Cloud Hypervisor ([rust-vmm/linux-loader](https://github.com/rust-vmm/linux-loader)) read the kernel straight into guest memory, with no firmware to copy it again; nothing measured found for qemu-lite | measured | labs 45182382388, 45182382864 |
| qboot `no-pci-walk.patch` | qemu/qboot | cold +5.2 [−1.7, +9.2]: inside the noise, and the upper bound over +5 keeps it; 5.0 ms nested in `firmware-stages` | measured, 40 a side | lab 44993790452 |
| qboot `mtrr.patch`, `write-pointer.patch` | qemu/qboot | function: write-back mappings, vmgenid | - | - |
| `hpet=off` | machine | nothing on record: a timer the guest would enumerate and then not use | unmeasured | - |
| ICH9 `sata=off`, `smbus=off` | machine | kernel to init 62.3 → 60.6 (09-08) | laptop, 400 boots | `fa1803f` |
| Hotplug disks on virtio-scsi, not PCIe root ports | machine | boot −31, attach 135 → 1.5 (09-29) | runner | #25 |
| Every machine started held, so QEMU can start ahead | machine | the "QEMU started ahead" rows: ~28 ms cold, ~25 restored | measured | report 44811347183 |
| Checkpoints on 8 multifd channels | machine | restored into a QEMU started ahead 150 → 103 against 1 channel; 16 channels lose to 8 when 4 restore at once, +3.2 [+1.1, +5.8] | measured | labs 44789688221, 44792429636 |
| The resume reads the state ahead | machine | restored into a QEMU started ahead −12.9 [−14.7, −9.8] | measured | lab 44809512339 |
| `spin-machine restore` reads the state ahead before its dial (`machine.ReadAhead`), beside QEMU's start | cmd/spin-machine, machine | restored −9.3 [−12.2, −7.3] evicted, cached −0.2 [−2.2, +1.5] | measured, 30 a side | lab 44976494418 |
| The read-ahead asked for by four goroutines, each every fourth 128 KiB piece | machine | restored −6.2 [−9.1, −4.3] evicted against one goroutine, cached −0.5 [−1.6, +1.9] | measured, 30 a side | lab 44976924478 |
| CPUID leaf 2 with QEMU's cache descriptors (`x-consistent-cache=off`), so glibc does not walk leaf 4 at every process start; `x-legacy-cpuid2-cache=off` (qemu/patches/0006) makes them leaf 4's caches, first login +0.6 [−6.1, +5.5] (lab 45101932173, #586) | machine/shape.go | first login −10.3 [−14.2, −4.0], 40 a side; CPUID exits 3727 → 2176; restored +1.0 [−3.9, +4.4] evicted, in noise | measured | labs 45083473790, 45083473855, 45083754801 |
| `pci=lastbus=0` | machine/cmdline.go | kernel to init 87.6 → 51.1 (2026-09): 8192 configuration reads, each an exit | runner, mechanism | log.md, 09 |
| `no_timer_check`, `tsc=reliable` | machine/cmdline.go | nothing on record (09-07) | unmeasured | - |
| `TERM=dumb` | machine/cmdline.go | 687 → 19 ms to systemd's first line (09-10), while systemd queries the console | laptop, mechanism | `b5f361f` |
| `rootfstype=ext4` | machine/cmdline.go | lead (11-19) | laptop | - |
| `quiet`, `loglevel=3`, `panic=1`, `net.ifnames=0`, `biosdevname=0`, cgroup v2, `memhp_default_state` | machine/cmdline.go | console, function | - | - |
| systemd built from Ubuntu's source with the `stage1` profile | image/systemd | first login −47, −38, −47: no BPF verifier in PID 1 | ladder | labs 44531842773, 44537549305, 44538877783 |
| systemd's drop-in candidate names derived once per unit, not once per lookup directory: upstream #42858 (v262), backported to 259 | image/systemd/0001-dropin-derive-candidate-names-once.patch | first login −6.5 [−9.9, −1.3], 100 a side against systemd rebuilt without it; PID 1's CPU before the login 56.0 → 52.4, p50 of 5 ledgers. Prior art: Eric Curtin's PR, `manager_enumerate()` 45.35 → 38.81 ms (50 runs, ~270 units, a container); Yocto's backport to 259.5 | measured | labs 45090299768, 45090326943 |
| systemd's tools linked static | image/systemd | −10 and −1, inside the noise | ladder | the same |
| SELinux off, `pcre2` on | image/systemd | function | - | - |
| `-O ^orphan_file` | image/build.sh | lead (11-19) | laptop | - |
| `lazy_itable_init=0` | image/build.sh | function: no 16 MiB of zeros in every overlay | - | - |
| 16 more unit masks (journal-flush, fsck-root, modules-load, pstore, random-seed, four modprobe@, …) | optimize-systemd.sh | cold +57.5 [+49.0, +63.0] | group C-masks | lab 44818953987 |
| 8 generators disabled, 3 tmpfiles rules masked | optimize-systemd.sh | cold +16.7 [+11.6, +23.3] | group C-generators | lab 44818953987 |
| udev's coldplug filtered (`10-coldplug.conf`), `cpu` kept for hot-added cores | image | cold +14.9 [+8.2, +19.8]; restored +463.7, which was not the coldplug but QEMU's TSC, a guest saved with its tick running (#452, QEMU 0005) | group C-coldplug | lab 44818953987 |
| ssh.socket, ssh.service and sshd-keygen without default dependencies; logind and dbus-broker without PrivateTmp; udev at `StartupCPUWeight=1` | image drop-ins | −59.4 [−65.9, −56.2] with the probe's forwarder early as well (lab 44805051310, `login-early` and `udev-startup`). In a second run, the image's half alone (`--edit lane`) −17.4 [−25.0, −8.5] and with the forwarder early as well −68.5 [−78.0, −61.8] (lab 44807457606). Each run against its own release, v20261120.03 | measured | labs 44805051310, 44807457606 |
| ssh.service without Ubuntu's `sshd -t` before it starts (`20-no-config-test.conf`); the check stays on reload | image drop-in | −3.2 [−7.2, −0.8], sshd accepting −2.4 [−6.4, −1.1], 60 boots a side | measured | lab 44977044826 |
| udevd and its coldplug not before sysinit.target (copies of the units without that line), so basic.target does not wait for udevd's start at the lowest weight | configure-system.sh | cold −48.6 [−51.7, −42.6], restored −2.0 [−8.0, +4.7] p95 114 against 114, cached p95 126 against 128, with QEMU 0005, 30 a side; prestart cold −44.7 [−53.4, −39.4], restored into prestarted −1.3 [−4.1, +3.6] p95 98.8 against 101. Without 0005 the restored p95 is 539-548 | measured | labs 44987412171, 44987412210 |
| The boot's default target holds only what the first login needs (`spin-machine-login.target`), and `multi-user.target` starts once its units are up (`spin-machine-boot-rest.service`, `Type=idle`) | image, docs/image.md "The first login's target" | first login −21.5 [−26.4, −15.5], sshd accepting −10.0 [−16.2, −3.7], accept to login −10.7 [−12.9, −8.2], second login +0.0 [−0.8, +0.6]; restored +1.1 [−6.0, +5.2] evicted, +2.2 [−10.6, +12.9] cached. The console prompt +15.5 [+11.5, +19.0], since the getty now follows the login: nothing a workspace does waits for it. With spin's supervisor off `basic.target` (spin-stack/spin#1653) the first login −45.3 [−50.2, −39.4] but the second +43.7 [+41.1, +46.1] | measured, v20261123.02, 40 a side | labs 45098372930, 45098372897 |
| QEMU 0005: a machine made for an incoming migration writes its TSC only from the migration, so KVM takes the checkpoint's TSC as given | qemu/patches | correctness: without it a guest saved with its tick running sleeps until its old TSC deadline, ~450 ms (#452). Alone it moves neither number: cold −3.0 [−5.8, +0.1], restored −1.9 [−7.6, +2.0] | measured | lab 44987412171 |
| logind, its Varlink socket, `user-runtime-dir@`, `user@` without default dependencies; `dbus.socket` without sysinit | image drop-ins, configure-system.sh | cold +16.5 [+9.8, +20.4], prestarted +11.0 [+6.2, +18.8], restored in noise; lane 478 → 402 (11-13) | measured | lab 45034963004, lab 43518915618 |
| Sessions of class `user-early-light` | configure-system.sh | lane 472 vs 436 | lane, mechanism: a root session would start `user@0` | lab 43518044150 |
| Sessions of class `user-early` and root lingering, so a login waits for its user manager and root's starts in the boot (#467) | configure-system.sh | correctness: the first login's own `systemctl --user` failed 10 of 10 on v20261122.03 and passed 10 of 10 with it (lab 45032247920). First login +4.7 [−0.2, +12.2] at 40 a side, sshd accepting +5.6 [+1.1, +10.1]; restored −10.8 [−15.9, −6.6] evicted, −12.3 [−15.1, −5.5] cached. The lane's login as spin, whose manager does not linger, +29.7 [+22.8, +42.1] | measured | labs 45031644179, 45031644114, 45031687433 |
| The user manager's sockets' environment from `environment.d/10-user-sockets.conf`, not two `systemctl --user set-environment` | image | with the row above: first login +19.6 [+15.9, +25.6] without it (lab 45031644179); with class `user-early` alone, −17.1 [−27.0, −10.7] (lab 45030778365) | measured | labs 45031644179, 45030778365 |
| Root's manager without the XDG autostart generator, which turns a graphical session's `.desktop` files into user units (#527) | optimize-systemd.sh | first login −3.8 [−6.2, −0.7], accept to login −6.1 [−9.0, −0.3], 100 boots a side; restored −1.7 [−4.4, +2.2] evicted, in noise | measured | labs 45070150688, 45070973943 |
| Host keys made from the boot's first dispatch | spin-machine-sshd-keygen.service | cold −0.8 [−10.5, +3.5], restored in noise: not a gain in spin's shape, where they are no longer made (the row below); kept for the lane, 489 → 468 (#515) | measured | lab 45034963004, lab 43483372488 |
| No host key made when spin's handoff has placed its own at `/run/spin/ssh/ssh_host_ed25519_key` (#515) | spin-machine-sshd-keygen.service | first login −6.9 [−9.6, −2.4], sshd accepting −6.8 [−10.4, −1.8], 120 a side, both arms with `--edit spin-handoff`; restored in noise, 20 a side. Prior art: RHEL's cloud-init conditions `sshd-keygen@` off while it provisions the keys (git.centos.org rpms/cloud-init, `ci-Inhibit-sshd-keygen-.service-if-cloud-init-is-active.patch`), for function and unmeasured | measured | labs 45086250796, 45085877238 |
| sshd without OpenSSH's userauth minimum time on a login: a request that authenticated, or a public key query answered with `PK_OK`, does not wait; a failure still does (#457) | image/openssh | restored −13.6 [−14.9, −9.8] evicted, −13.6 [−16.2, −11.4] cached; cold −9.4 [−15.8, −2.6], the second login −10.9 [−12.3, −9.0] | measured, 30 a side | labs 44983986606, 44983986575 |
| An Ed25519 host key only, no ECDSA | spin-machine-sshd-keygen.service | −5.0 [−8.3, −1.8], sshd accepting −4.4 [−6.9, −1.9], 60 boots a side | measured | lab 44979726719 |
| MOTD and `pam_motd` off; `mlkem768x25519-sha256` alone | configure-system.sh | −12 on the first login; 104 → 40 a login against sntrup761 | ladder, mechanism: a root login runs the same PAM and kex | #240, lab 43479854381 |
| journald volatile, 8 MB, its flush masked | optimize-systemd.sh | cold +6.1 [+2.3, +15.8], prestarted +6.5 [+3.1, +13.1], restored in noise; 8 MB against 4 MB, first login −2.2 [−12.1, +6.4], 20 a side | measured | labs 45034963004, 45121159504 |
| docker and containerd started by their sockets, not at boot (deferred: the first `docker` command waits for them, docs/image.md) | optimize-systemd.sh | cold +13.9 [+11.4, +18.2], restored +55.7 [+12.0, +73.9], into prestarted +40.7 [+33.5, +59.0] | measured | lab 45034963004 |
| timedated, firstboot, machine-id-commit masked | optimize-systemd.sh | function: time from the host, no first-boot prompt, no identity | - | - |
| `10-seats.conf`, the getty's TERM, `/tmp`'s size | image drop-ins | function | - | - |
| The pressure probe's sender, started with `multi-user.target` by the boot's rest, at the lowest priority | spin-machine-pressure.service | function (docs/machine.md, "Pressure"); from the boot's rest in place of a timer 10 s after the boot: first login −0.8 [−7.8, +4.8], restored −0.8 [−2.9, +2.1] evicted, 20 a side; it starts at 0.37-0.38 s, after the login. Before #581, after multi-user.target beside the first login, sshd accepting to login +2.6 to +3.3 in three runs | measured | labs 45120950183, 45120950314, 45120692382; 44921602512, 44924252325, 44924755505 |

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
touch, oldest first within a group. The "Shape" column says which shape a row was measured in,
and a row measured in another shape than spin's has such a reason.

### Kernel and its command line

| Idea | Result | Shape | Source | Date |
|---|---|---|---|---|
| Trim the 8250 driver | 154 µs | runner | `c03ed89` | 09-10 |
| Modular kernel | 0.57 ms; breaks "a release is one machine" | runner | `1a8e25c` | 09-10 |
| ACPI params (acpi=noirq, pnpacpi=off, …) | 69.9 vs 68.6 ms, slower | runner | `64d2901` | 09-10 |
| initcall_blacklist ksm/kcompactd | 0; event_trace's 6 ms is the profiler | runner | `64d2901` | 09-10 |
| `CONFIG_XFRM=n` | moves BTF's 9 ms first parse elsewhere | runner | `e285dec` | 09-10 |
| A generous vCPU ceiling | maxcpus=16 +5 ms, 64 +9 ms of kernel; the ceiling stays (invariant) | runner | `f32d527` | 09-10 |
| MICROVM | no ACPI CPU hotplug | - | `b151186` | 09-10 |
| `CONFIG_VT=n` for speed | 0 to login; 0.31 ms of kernel | runner | `e2f6b8f`, `9b5f0dd` | 09-26 |
| KSM and THP off | −0.50 ms | runner | `cda04b1` | 09-26 |
| COMPACTION off | impossible: virtio-mem needs CONTIG_ALLOC | - | `cda04b1` | 09-26 |
| erofs removed for speed | noise (100.46 vs 102.22) | runner | `5a3d509` | 09-26 |
| Smaller TCP hash tables | moved nothing | runner | `dae6a14`, #48 | 09-26 |
| ZRAM | the runaway killed later (20 vs 14 s), worst login 3.8 s | runner | kernel/Dockerfile, `430d2b3` | 09-28 |
| `pci=noearly` | the guest does not boot | - | #240 | 11-12 |
| A configuration-space snapshot over fw_cfg (QEMU and kernel) | 209 vs 211 with 0005 | ladder | lab 43514884598 | 11-13 |
| `scsi_mod.scan=manual` | kernel −9.3, but since Linux 5.10 a disk added at run time, at boot, or under `-S` never appears, silently. `max_target` took its place | spin | lab 44570773103; `drivers/scsi/virtio_scsi.c`, `scsi_scan.c` | 11-19 |
| `CONFIG_SCSI_SCAN_ASYNC` | −2.6 [−8.9, +6.8]: the scan is waited for before "Freeing", and `max_target` leaves it little | spin | #325, lab 44788216590 | 11-20 |
| The decompressor without `X86_VERBOSE_BOOTUP` | to the kernel's clock 35.1 → 32.4 ms, under the 3 ms set before the run | spin | #328, lab 44787878809 | 11-20 |
| An uncompressed bzImage payload, KASLR kept | to the kernel's clock −3 ms, but +3.3 in qboot's copy and +6-7 in QEMU's start | laptop | #326 | 11-20 |
| The same on QEMU 0007, which maps the bzImage and so no longer reads the 40 MB file in its start (#646, a lead for #645) | first login −4.0 [−7.9, +0.3], sshd accepting −2.9 [−6.9, +2.4] at 40 a side against LZ4 on the same QEMU; QEMU's RSS +32.0 MiB (+22.2 file, +9.9 anonymous). The QEMU half of the 11-20 rejection is gone; what is left is under the interval at 40 | spin | lab 45183618987 | 11-23 |
| The bzImage's decompression made cheaper, in general | ~21 ms before the kernel's clock with the bzImage against PVH; most of it is the copy and relocation KASLR needs, and KASLR stays. No candidate left | spin | lab 44787491631 | 11-20 |
| `driver_async_probe` for rng, balloon and vsock; `rcupdate.rcu_expedited=1` (carried since 11-13 and 09-07) | taken back −1.4 [−4.9, +3.1]: one vCPU has no second CPU to probe on. Deleted | spin | lab 44818953987, #352 | 11-21 |
| Kernel 0006 (BTF parsed in the background), 0009 (a booting AP polled by local_clock()), 0011 (queue_select written only on a change) | taken back −2.5 [−8.4, +1.8] at 40 boots. Deleted | spin | labs 44818953987, 44822157367, #345 | 11-21 |
| `DEFERRED_STRUCT_PAGE_INIT` (carried since 11-13, for 2 GiB at boot) | taken back −2.5 [−9.3, +3.8] at 40 boots: spin boots 128 MiB, which the kernel does not defer, and plugged memory is never deferred. Deleted | spin | lab 44839538785, #356 | 11-21 |
| vmlinux BTF parsed on first use, not at the first kfunc registration (a kernel patch on branch `exp/btf-first-use`) | kernel to "Freeing" −7.2 [−8.6, −6.2], inet_init 6.85 → 0.14 ms (lab 44982285400, 10 boots), but the first login −1.5 [−3.1, +1.6] at 60 boots (lab 44982285406): journald's `DeviceAllow=` and `IPAddressDeny=` load cgroup BPF programs at its start, and `bpf_check()` parses the BTF there, before the login. A verifier that asks for BTF only when a program needs it is the next step | spin | labs 44982285400, 44982285406 | 11-22 |
| `CONFIG_HZ_250` for `HZ_1000` (#567), as Firecracker's and Cloud Hypervisor's guest configs build | every exit −587 [−608, −445] (MSR writes 1018 → 755, preemption timer 691 → 482, HLT 332 → 298, 3 a side, lab 45111418504), but the first login −0.6 [−5.9, +3.1] at 60 a side (lab 45111452844): a tick's exits are handled in ~1-1.5 µs each in the L1, not the ~7 µs a nested MMIO or port exit costs | spin | labs 45111418504, 45111452844 | 11-23 |
| virtio-pci writes `queue_select` only when the queue changes, a second time and alone (#592 lever 1, a kernel patch on branch `exp/592-l1-queue-select`; first as 0011) | 68 MMIO exits fewer (91 → 23 writes, 3 a side, lab 45113437048), the first login −2.8 [−6.6, +3.4] at 60 a side (lab 45114405201) and +0.6 [−1.8, +2.3] at 120 (lab 45115330128). The vector read-back stays: virtio 1.2 §4.1.5.1.2 makes it how a device refuses a vector | spin | labs 45113437048, 45114405201, 45115330128 | 11-23 |
| x86's `msi_set_affinity` skips the message write when the vector and destination did not change (#592 lever 2, a kernel patch on branch `exp/592-l2-msix-once`) | 56 MMIO exits fewer (`__pci_write_msi_msg` 168 → 112, 3 a side, lab 45113437048), the first login −0.1 [−3.1, +4.5] at 60 a side (lab 45114405201) | spin | labs 45113437048, 45114405201 | 11-23 |
| `pci_save_state()` at a device's add kept only where an error handler can restore it (#591 a, a kernel patch on branch `exp/591a-no-save-state`) | 128 MMIO exits fewer, the host's handling −3.3 [−6.2, −2.1] ms (3 a side, lab 45113437048), but the first login −4.3 [−6.8, +0.5] at 60 a side (lab 45114405201) and −1.3 [−3.5, +1.3] at 120 (lab 45115330128). Its gate has to grow by hand: upstream a2f1e22390ac took out `pci_restore_state()`'s refusal of a state never saved | spin | labs 45113437048, 45114405201, 45115330128 | 11-23 |
| virtio-pci's vendor capabilities walked once per device (#591 b, a kernel patch on branch `exp/591b-virtio-caps-once`) | 114 MMIO exits fewer (3 a side, lab 45113437048). The first login −5.8 [−8.2, −0.6] at 60 a side (lab 45114405201), three times what 114 exits can cost, and +0.9 [−1.8, +2.7] at 120 (lab 45115330128) | spin | labs 45113437048, 45114405201, 45115330128 | 11-23 |
| The IOAPIC's redirection table read once at boot, and written back only where it was masked (#590, a kernel patch on branch `exp/590-ioapic-once`) | IOAPIC accesses 589 → 205 and the host's handling −3.1 [−6.2, −1.1] ms (3 a side, lab 45113437048), but the first login −4.5 [−7.8, +0.4] at 60 a side (lab 45114405201) and −2.0 [−4.3, +0.6] at 120 (lab 45115330128). With the four other exit patches of #591 and #592, as a bound, −7.4 [−12.1, −2.8] (#627) | spin | labs 45113437048, 45114405201, 45115330128 | 11-23 |

### Firmware, QEMU and the machine

| Idea | Result | Shape | Source | Date |
|---|---|---|---|---|
| `usb=off`, `vmport=off`, `smm=off`, `i8042=off` | 63.0/62.6/62.3/62.5 vs 62.9 ms | laptop | `fa1803f` | 09-08 |
| A named CPU model instead of `host`, for memories that resume on any host (`--cpu baseline`) | the newest model a nested c8id runs is Cascadelake-Server-v5, not SapphireRapids or GraniteRapids; cold first login +27.2 [+13.0, +30.8], sshd accepting +23.1 [+9.7, +28.5], warm login and RSS unmoved; the cause is not measured. `host` with a pool's common features is the next candidate (spin-stack/spin#1417) | spin | lab 44855890737 | 11-21 |
| Less guest RAM (512 MiB) | 1241 vs 1256 ms, nothing | runner | `80f984b` | 09-10 |
| `cache=none` on disks | breaks the shared base's caching, fails on tmpfs | - | `8b7391a` | 09-07 |
| Overlay O_DIRECT | small reads 1490 vs 530 ms | runner | docs/memory-and-disk.md | 10-01 |
| Board init wrapped in one memory transaction | the guest prints nothing | runner | `f10383b`, lab 36662499370 | 09-30 |
| Base on virtio-pmem and DAX | ~155 MB less per VM, but a side channel | runner | #63, #65 | 10-01 |
| Remove virtio-rng | −13.6 ms to the console prompt, never on a goal number; what seeds the RNG without it is open ("Open questions") | ladder | lab 43321809471 | 11-12 |
| QEMU copies the kernel's ELF segments on threads | laptop QEMU start 30.9 → 21.6; nested it adds nothing | ladder | labs 44580080420, 44581350077, #290 | 11-19 |
| A 1 MiB readahead on the root disk (`opt_io_size=524288` on virtio-blk), for fewer requests and so fewer notifications (#566) | iowrite16 exits 895 → 927 (3 a side, lab 45098880914); first login +1.4 [−2.7, +8.0] at 40 a side. The boot's reads are not readahead-bound (inferred) | spin | lab 45098879979, #592 | 11-23 |
| QEMU's start from its exec to its first trace event made cheaper (#647) | 9.7 ms on bench (lab 45177818419), no part of it over ~2 ms. The release's QEMU is static-pie on musl, so there is no dynamic loader to time (`LD_DEBUG` reads nothing): musl relocates its 59,700 relative relocations itself. On the laptop the phase read 4.7-5.4 ms p50, 40 starts each: a trivial static-pie binary's exec 0.23, QEMU's `-version` (exec, relocation, constructors, QOM type registration, the first options) 2.0, `-sandbox` dropped −0.7 to −0.9 (libseccomp builds its filter at run time), and the rest is option parsing, the trace's and the main loop's start and the machine object up to its first address space. perf over 200 `-version` runs: the relocation 7% of it, the kernel's page faults (copy-on-write, file fault-around, zeroing) ~15%, nothing else over 2%. From exec to held, malloc (musl's mallocng) is ~2.3 of 17 ms of the main thread's CPU. Candidates and their ceilings, each inferred at ~2× the laptop on bench: a filter compiled at build time as Firecracker's seccompiler does, ≤~1.5 ms; a non-PIE binary, no relocation and fewer copy-on-write faults, ≤~1 ms and it gives up ASLR of QEMU's image; DT_RELR, ≤~0.3 ms. Every one is under the ±4-5 ms that 60 boots a side resolve, so none was built. Another allocator is a lead across all of QEMU's start, not this phase (#669) | spin, laptop | lab 45177818419, #647 | 11-23 |
| Lower the vCPU ceiling, drop the balloon or ACPI | not tried: an invariant | - | AGENTS.md | 11-13 |

### systemd and the image

| Idea | Result | Shape | Source | Date |
|---|---|---|---|---|
| Mask udev | saves 0, and boots never reach a login (10 s ttyS0 timeout) | runner | `4cbd229`, `80f984b` | 09-10 |
| agetty tweaks (TTYReset, --noissue, --delay 0, …) | within the noise of agetty's sleep(1) | runner | `80f984b` | 09-10 |
| chrony or another time daemon | 10 ms; cannot know it was restored | runner | `c7af4d3` | 09-10 |
| systemd-timesyncd | SNTP only, no refclock | - | `c7af4d3` | 09-10 |
| 19 udev rules masked; loop and 8250 counts as command-line parameters | 0 to the console prompt | runner | `e2f6b8f` | 09-26 |
| A console unit instead of serial-getty | +340 ms until TERM=dumb, then equal | runner | `c6e48de`, `7b08e5a` | 09-26 |
| Fewer unit files | 300 extra: 236 vs 246 | runner | `9fab3f1` | 09-26 |
| systemd-oomd | the kernel always beat it | runner | `337c08d` | 09-28 |
| CPUWeight/IOWeight/MemoryLow on ssh.service | no change | runner | `f7cd1ab` | 09-28 |
| Getty without BindsTo dev-ttyS0 | 342 vs 345 | ladder | lab 43303190733 | 11-12 |
| The serial getty at `StartupCPUWeight=1`, out of the first login's way | +2.7 [−4.3, +7.9] at 60 boots | spin | lab 44977226981 | 11-22 |
| Mask four varlink sockets nothing uses (factory-reset, mute-console, sysext, creds) | −0.8 [−4.8, +4.3] at 60 boots | spin | the same | 11-22 |
| Keygen wanted by multi-user.target | 518 vs 514 | ladder | lab 43312419007 | 11-12 |
| Mask sshd-socket- and run-generators | login 489, no change | ladder | lab 43483372488 | 11-13 |
| Mask 11 always-failing units | 484, no change | ladder | lab 43483372488 | 11-13 |
| Mask `modprobe@drm` (logind is ordered after it) | 327/345 vs 328/337. It is masked all the same, since 09-07, one of the 16 masks of group C-masks | ladder | lab 44558035927 | 11-19 |
| Mask `systemd-udev-load-credentials` | −8 then −6 ms, p95 unchanged: not separable at 20 boots | ladder | labs 44537549305, 44538877783 | 11-19 |
| systemd with `-Db_lto=true` | Ubuntu already passes `-flto=auto`: the same packages | - | image/systemd/Dockerfile | 11-19 |
| Masks of 15 units and a generator the image does not have | they masked nothing. Deleted without a run; image/build.sh refuses such a mask | - | #352 | 11-21 |
| PID 1's `LogLevel=err`, `LogTarget=journal`, `ShowStatus=no`, `StatusUnitFormat=name`; `kernel.printk = 3 3 3 3` | taken back −1.8 [−7.7, +3.2] at 40 boots; the console stays quiet through the command line. Deleted | spin | labs 44818953987, 44822157367, #354 | 11-21 |
| logind, `user-runtime-dir@` and `user@` at `StartupCPUWeight=1000`, the units user@0's chain is (#553) | −3.6 [−10.3, +0.6] at 40 boots (−9.2 [−16.1, −0.3] at 30 in a screening run of three arms, lab 45082870346). Accept to login −7.7, sshd accepting +8.8: logind and the manager come sooner and `sysinit.target` later, on the one vCPU | spin | lab 45083184310 | 11-22 |
| `spin-machine-sshd-keygen` and `systemd-udev-load-credentials` at `StartupCPUWeight=1` (#553) | sshd accepting +22.6 [+15.9, +33.5], first login +3.4 [−3.2, +14.6] at 30 boots: ssh.service waits for the keygen, and the forwarder for ssh.service. With the arm above, first login +6.0 [+0.5, +15.1] | spin | lab 45082870346 | 11-22 |
| Mask `systemd-udev-load-credentials` again, in spin's shape on one vCPU (it was −8 and −6 on two) | −2.1 [−6.3, +0.3] at 60 boots: not separable | spin | lab 44977044826 | 11-22 |
| `systemd-user-sessions` without its ExecStart (it removes a /run/nologin the image never writes), its ExecStop kept | −0.6 [−5.2, +1.5] at 60 boots | spin | the same | 11-22 |
| Mask `sshd-socket-generator`, again on spin's path | +0.8 [−4.0, +4.7] at 60 boots | spin | the same | 11-22 |
| `DefaultMemoryAccounting=no`, for cheaper unit cgroups | +4.1 [+0.8, +7.1] at 60 boots, worse; sshd accepting +6.2 [+2.2, +8.0]. The cause is not measured | spin | the same | 11-22 |
| No unit made for a `RequiresMountsFor=` prefix with no unit file (`unit_add_mount_dependencies` checks the unit name map before it loads one) | −1.0 [−4.3, +3.9] at 100 boots, and PID 1's CPU before the login 57.2 against 56.0, p50 of 5: not separable. Whether the stubs went in those boots was not observed (no debug boot of the arm). The patch and an upstream draft, not sent, are #580 | spin | labs 45090299768, 45090326943 | 11-23 |
| The rest of the boot started by a timer 2 s after the boot, in place of `Type=idle` (#574) | −66.5 [−75.2, −56.1] against idle's −59.2 [−69.1, −47.4] in the same run, both with the supervisor early: the timer starts the rest at an arbitrary time whatever the login, and docker's socket waits for it | spin | lab 45090246267 | 11-23 |
| The rest of the boot started from sshd's PAM stack at the first session (#574) | −52.0 [−61.6, −37.4], accept to login +15.3 [+10.8, +21.7]: it runs beside the login's last steps, and every login pays its `pam_exec` | spin | lab 45090246267 | 11-23 |
| `pam_selinux` (twice) and `pam_mail` out of sshd's PAM stack: SELinux is off and there is no mail | −1.9 [−4.9, +2.8] at 60 boots | spin | lab 44979726719 | 11-22 |
| `nsswitch.conf` without the `db` entries libnss-db would serve (kept since 09, never measured); reverted to Ubuntu's | taking it back +0.8 [−8.4, +5.2] cold, restored in noise | spin | lab 45034963004 | 11-22 |
| The manager's timeouts at 15 s and 10 s, the watchdogs off (`fast-boot.conf`); reverted to systemd's defaults | taking it back +0.3 [−5.2, +3.3] cold, restored in noise | spin | lab 45034963004 | 11-22 |
| containerd's CRI-enabled config replaced by containerd.io's own (#510); reverted to the package's | first login −4.9 [−12.2, +1.6] at 20 boots, restored login −0.6 [−2.1, +1.5] evicted: containerd starts at the first docker command, after the login | spin | labs 45111391250, 45112037022 | 11-23 |
| The image's `containerd.socket` out of the boot transaction, Docker's own `docker.socket` on both sides (#509) | first login +7.4 [−4.7, +15.3], sshd accepting +7.5 [−6.6, +14.9] (lab 45111146715); prestart cold −1.1 [−8.0, +9.0], prestarted +4.5 [−4.6, +9.9], restored −0.1 [−1.0, +1.7], restored into prestarted −0.5 [−1.8, +0.7] (lab 45111146865): all within noise. The socket starts in `sockets.target`, after the first login since #581. Kept: nothing to gain, and a tool that dials containerd's socket before docker has run would lose its start | spin | lab 45111146715, 45111146865 (20 a side, v20261123.04) | 11-23 |
| PID 1, journald, logind and `user@0` linked without the dynamic linker: v262's multicall build (#42304) or a static one (#42820), for their ld.so start-up (#638) | ld.so's own start-up, `LD_DEBUG=statistics`, p50 [range] of 8 boots: PID 1 0.48 [0.46, 0.56] ms, journald 0.20 [0.14, 0.31], logind 0.20 [0.11, 0.31], `user@0` 0.21 [0.15, 0.36]; 1.1 ms for the four, under #638's 5 ms threshold, so no build was run. Relocation 0.08-0.11 ms each (2,522 symbol and 16,589 relative relocations in PID 1 and `user@0`). The instrument moved nothing: first login +3.4 [−23.0, +11.9], PID 1's CPU −1.1 [−5.1, +0.6]. The multicall build keeps libsystemd-core and libsystemd-shared dynamic and leaves journald and logind as they are; the static one has no PAM for `user@`'s `PAMName=` | spin | lab 45180856158 | 11-23 |

### The login

| Idea | Result | Shape | Source | Date |
|---|---|---|---|---|
| sntrup761 (OpenSSH's default kex) | 104 vs 40 ms a login | ladder | #245, lab 43479854381 | 11-13 |
| The vsock socket and sshd@ without default dependencies | no sooner; dropped 7 of 22 first connections. Carried since with the lane's other drop-ins (`sshd-vsock.socket.d`, `sshd@.service.d`), which the lane kept together (lab 43518915618) | laptop | `c3c0fd1` | 11-13 |
| CPUWeight/IOWeight/Nice on the first login's lane | −2 ms over the filtered coldplug; second login 63 vs 45 | lane | lab 43518915618 | 11-13 |
| One sshd for every connection (vsock `Accept=no`, `sshd -D` early) | first login 382 vs 381; second 40 vs 48 | lane | lab 43524721490 | 11-13 |
| A login without pam_systemd (a bound) | 380 vs 436, but no session and no XDG_RUNTIME_DIR | lane | lab 43518044150 | 11-13 |
| logind's Varlink socket without default dependencies | accept to login 131 → 120, first login inside the noise; kept with the lane's other drop-ins | ladder | labs 44558035927, 44558053261 | 11-19 |
| Sessions of class `user-early` with the manager started inside the login (#462). Kept since with root lingering (#467, "What the machine carries") | first login +36.2 [+32.0, +45.7], accept to login +39.5; restored +0.5 [−3.0, +3.8] | spin | lab 44987167474, 44987167549 | 11-22 |
| Every login's user manager started beside it, not waited for (`user-runtime-dir@`'s `20-manager.conf`, #462): a command that needs the user bus at once in the first login came before the manager | correctness: the first login's own `systemctl --user` failed 10 of 10. Replaced by class `user-early` (#467) | spin | lab 45032247920 | 11-22 |
| The user manager without the XDG autostart generator, on top of class `user-early` (#467) | first login −4.8 [−11.2, +1.3], accept to login −3.7 [−7.5, −0.5] at 20 boots. Carried since #527, with root's manager in the boot: −3.8 [−6.2, −0.7] at 100 ("What the machine carries") | spin | lab 45030778365 | 11-22 |
| Both user generators masked, a bound: the environment.d one also brings `/etc/environment` to user services | first login −8.4 [−15.8, −1.0] against class `user-early` | spin | lab 45030778365 | 11-22 |
| The user manager's PAM stack down to `pam_permit` and `pam_systemd`, a bound | first login −3.5 [−11.4, +2.5] against class `user-early` | spin | lab 45030778365 | 11-22 |
| `user@`'s `After=systemd-user-sessions.service` (Ubuntu's `10-login-barrier.conf`) shadowed, for a login as spin | lane +30.6 against +29.7 without it: the barrier is not what the lane waits for | lane | lab 45031687433 | 11-22 |
| Root's manager cut to what a workspace could do without, a bound: both user generators, the PAM stack down to `pam_permit` and `pam_systemd`, `ssh-agent.socket` and `systemd-ask-password.socket` masked. Without the environment.d generator user services lose the bus's address (`10-user-sockets.conf`) and `/etc/environment`; without `pam_unix`, `pam_limits` and `pam_loginuid` the manager skips the account check, the limits and the audit login uid | first login −9.6 [−13.3, −6.7] against v20261122.06, the ledger's ~10 ms of CPU for the same cuts. The XDG generator alone was carried; the rest is function | spin | labs 45070150688, 45070208762 | 11-22 |
| No user manager for root, a bound: sessions of class `user-early-light` and root not lingering, as before #467 | first login −17.3 [−19.2, −12.4], console prompt −26.6: the manager's 17.5 ms of CPU, 1:1, on a vCPU with no idle from ~155 ms to the login. The first login's `systemctl --user` fails without it (#467) | spin | lab 45070150688 | 11-22 |
| spin's forwarder (spin's supervisor) without default dependencies, after `ssh.service` only (`--edit forwarder-early`), on v20261122.06 | cold +4.2 [+0.0, +11.0], restored −0.0 [−2.3, +2.3], restored into prestarted −4.3 [−7.7, −0.4]: not a gain on either goal number, though the forwarder was active 116-141 ms after PID 1 against 154-176 (bootchart, 3 a side). The login then waits for systemd-logind, active at 162-186 ms in both arms, before `user-runtime-dir@0` (#553). On earlier releases it read cold −60.4 (lab 44987584707); what changed since is not measured | spin | labs 45071102677 (`prestart`, 20 a side), 45071549627 | 11-22 |

### The restore and checkpoints

| Idea | Result | Shape | Source | Date |
|---|---|---|---|---|
| mapped-ram with multifd ×4, 11-13 | not comparable by its own run's log; channels were measured again on 11-20 and kept | ladder | #246, lab 43483852339 | 11-13 |
| Fast snapshot load (mapped-ram and postcopy-ram on a `file:` URI) as the restore | running at 38 ms, but the login at 293/325 instead of 152/161: every page touched is a fault served by one read. `LoadOptions.Fast` stays for large states | ladder | lab 44585840498 | 11-19 |
| Fast snapshot load for every state, the caller's choice (`LoadOptions.Fast`, spin's `fast_load`) | login +394 [+388, +420] at 179 MiB of data, +103 at 1.2 GiB, −129 at 1.7 GiB, −2615 at 7.3 GiB. Replaced by the machine's choice above 1.5 GiB (#470) | spin, touched | labs 45034868686, 45035462478, 45034881876, 45034881366 | 11-22 |
| The state read ahead during a fast load | at 179 MiB the login −168 ms, still +234 over a full load; at 3.7 GiB within noise, p95 713 against 490 | spin, touched | labs 45035029887, 45035048486 | 11-22 |
| Base memory in a shared file, left out of the checkpoint (`x-ignore-shared`) | state 0.3 MiB, save 15 ms, but neither goal number improves: cold 1089 ms on disk, 709-719 on tmpfs | ladder | labs 44663272360, 44664989139, #306 | 11-20 |
| 16 multifd channels as the default | −10.1 alone, but +3.2 [+1.1, +5.8] against 8 when 4 restore at once | spin | lab 44792429636 | 11-20 |
| `direct-io` (O_DIRECT) on the channels | within the noise evicted, +45 ms cached | laptop | log.md, 11-21 | 11-21 |
| `direct-io` on the channels of a save, against dirty-page throttling in the stop | never converged beside a busy disk: each pass ~2.5 s, the save cancelled after 10 min | spin, plugged 2 GiB | lab 44973775066, docs/memory-and-disk.md | 11-22 |
| `downtime-limit` 15 instead of 30 | the guest's stop −2.5 [−7.5, +3.0] short of memory beside a busy disk, −1.5 [−6.0, +2.8] with a writer | spin, plugged 2 GiB | lab 44975130836 | 11-22 |
| The guest's clean page cache dropped right before its save (`sync; echo 1 > /proc/sys/vm/drop_caches`), #276's last idea | the state did not shrink: 183 MiB with the drop, saved at once −0.1 [−0.4, +0.3] and 3 s later −0.0 [−0.3, +0.4]. The restored login was worse, +4.7 [+3.5, +7.3] evicted at once and +5.6 [+4.6, +7.6] 3 s later, as the login read again from the guest's disk what the drop took from its cache | spin | lab 45110967817 | 11-23 |

### QMP and vsock

Both transports were measured in spin's shape on the bench, 20 boots, with the `transport` probe
(lab 44912471522, v20261121.01, 2026-11-21). Each ceiling is what the goal number would gain if the
part cost nothing. None is above the noise (±7-9 ms cold, ±13 restored at 20 boots), so none was
built.

| Idea | Result | Shape | Source | Date |
|---|---|---|---|---|
| Faster QMP in general | the cold boot waits on 14 commands, 6.2 ms from QEMU's main loop being free to `cont` answered (1.3% of 470). A restore waits on 17 before `migrate-incoming`, 6.7 ms, and 2.7 after the guest runs. The commands' own run is ~1.2 ms of each path. The rest is round trips: query-status alone is 131 µs from the client, 18 µs in QEMU. spin-machine's start and dial are off the path: its first command waits 17 ms (cold) and 19.5 (restored) for QEMU | spin | lab 44912471522 | 11-21 |
| Pipelining the bind (every command sent before the first answer) | ceiling: QEMU's wait on the client, 4.5 ms cold and 5.2 restored, under the noise. The read-ahead overlaps the restore's half, so its share is smaller | spin | the same | 11-21 |
| The held start's bind sent ahead of its answers: the run state with the places, every place's serial at once, each image's two nodes together, the reopen with the placeholder's delete; 13 round trips to 9, add-fd still waiting for its answer and `cont` for the bind's last (#649) | first login −1.7 [−5.6, +2.6], sshd accepting −2.4 [−5.5, +2.8] at 60 a side (lab 45230790902); restored −0.3 [−3.8, +3.7] evicted, +0.9 [−10.6, +7.5] cached (lab 45231358105). The split: QEMU waited 4.5 ms p50 for the client in 24 boots, 0.9 of it QEMU busy with the probe's plug monitor, so ~3.6 ms is spin-machine's own, ~0.28 ms a round trip (lab 45177818419). The four round trips removed are ~1.1 ms (inferred), and the whole of spin-machine's share is under what 60 boots resolve. Code on tag `archive/exp-649-held-start` | spin | labs 45230790902, 45231358105 | 11-23 |
| Fewer round trips (no `query-status` before the bind, places read from the Spec instead of `query-block` and `qom-get`, `Last(RESUME)` for the `query-status` after a load) | ceiling ~1.3 ms cold, ~1.9 restored | spin | the same, the detail by command | 11-21 |
| Smaller answers | nothing large is on either path: `query-block` runs in 0.12 ms. The record's 125 KB answers left the restore's clock with `Resume(here)` | spin | the same | 11-21 |
| QMP over vsock, SEQPACKET, ivshmem, virtio-serial, gRPC or protobuf framing | QMP does not ride vsock: it is a Unix socket between the host and QEMU. vsock carries the login, and a login is 10 round trips of 47.8 µs (57.5 through the forwarder), 0.6 ms of a 32.8 ms warm login. Every vsock change shares that ceiling | spin | the same | 11-21 |
| `kvm.halt_poll_ns`, CPU pinning, for vsock | bounded by the same 0.6 ms a login. What they do to the boot's other wake-ups is a different question, not measured here | spin | the same | 11-21 |
| An `iothread=` for the disks, against QMP stalls beside a guest's synced writes on a busy host disk (#391) | no stall to take off the monitor: 2,400 query-status round trips beside 4 KiB `oflag=dsync` writes, with the scratch NVMe idle, 99-100% busy with O_DIRECT writes and 100% busy through the page cache, never took more than 1.2 ms (p50 0.24-0.56 ms), while the guest's writes fell from ~115,000 to 1,500-3,100 in the row. The laptop's 175-950 ms was QEMU's main thread blocked in the host, by #402 in reclaim, the mmap lock or a cgroup throttle, which an iothread does not move | spin | labs 45111443524, 45112573850, `stall` probe archived as `archive/qmp-stall-391` | 11-23 |

### Memory and the host

| Idea | Result | Shape | Source | Date |
|---|---|---|---|---|
| Lower `memhp_default_state` (unplug policies) | kept `online` | runner | `2903d8c`, #48 | 09-30 |
| DAMON_RECLAIM, MGLRU eviction | slower, or not selective | runner | docs/memory-and-disk.md | 10-01 |
| `page_reporting_order` 0 or 3 | ~100 MB transient vs ~180 MB of huge pages lost | runner | docs/memory-and-disk.md, #83 | 10-01 |
| Prefetch the boot's files | 47 ms to win 24 | runner | run 36803912357 | 10-01 |
| A second limit (`memory.high`) | `memory.max` suffices | - | docs/memory-and-disk.md | 10-01 |
| KSM, free page hinting, virtio-fs DAX | security, correctness, unsupported | - | docs/memory-and-disk.md | 10-01 |
| QEMU at a higher host priority (cgroup `cpu.weight`, nice, a real-time class) on an idle host | ceiling: QEMU's threads wait 0.2/1.1 ms for a CPU from its exec to the cold first login and 0.1/0.2 to the restored one, the guest's steal 0. Priority can win back only that wait. On a busy host it is a lead for spin ("Open experiments") | spin | lab 44925573288, `run-delay` | 11-21 |
| A burst of restores bound by host CPU0 | no: busy time, interrupts and QEMU's threads spread over all 16 CPUs, and irqbalance runs | spin | lab 44817486118 | 11-21 |

## Open experiments

The rows are ranked by ceiling. A row stays here until it is decided, and then moves.

| Experiment | Number | Phase, p50, ceiling | Decided by | State |
|---|---|---|---|---|
| A burst of restores: 8 at once read ~190-240 ms each under `defer`, against ~100 alone | restore, as a host sees it; spin restores concurrently with no host-wide limit | the burst's ~100-140 ms over one restore alone | `restore-together`, `together=1 8`, evicted, with the change against without it | open. The host's CPUs are 72-78% busy in the burst, and direct reclaim runs as the states fill the page cache (lab 44817486118). Next candidates: `vm.watermark_scale_factor`, and the CPU one restore costs |
| A login without OpenSSH's exec per connection and PAM | both | a warm login, ~25-43 ms, is in both numbers | a bound first: `prestart` with the login replaced | lead (log.md, 11-20, "spin's login, spin's memory"). spin's path already runs one sshd, and PAM and pam_systemd are what spin's sessions are made of |
| Carried items never measured alone: kernel patches 0005, 0007, 0008 and 0010 each (group A is +67.5 together); `pci=lastbus=0` (2026-09, before patches 0004 and 0010); logind's early varlink socket (measured on 2 vCPUs); QEMU 0001 and qboot's `pam.patch` apart; `no_timer_check`, `tsc=reliable`; `hpet=off`; systemd's static tools; `-O ^orphan_file`; `rootfstype=ext4`; the lane's drop-ins on spin's path | cold | each ≤ a few ms; B together +11.5 | rule 5, an arm each, 40 boots | open. The static tools and `orphan_file` need an image build (`--systemd` builds only an experiment patch) |
| What functional items cost, last measured on another machine: BPF JIT and BTF (+21.4 ms to "Freeing", 09-07, a vmlinux through PVH, before the ftrace patches), lockdown at confidentiality (nothing told from noise, 10-01, a host at load 6-10) | cold | a few ms each, unknown in spin's shape | `ssh-first` with a `--kernel` built without each, 20 boots | open. They stay for function, so a number says what they cost and decides nothing |
| The no-systemd floor, each release | a bound | how much of the cold number is systemd's to give back: 445 → 240 in spin's shape | `ssh-first` on the release with `--edit floor --append init=/usr/local/sbin/spin-floor` and `login=lane`; `releases measure` does it beside every release, and report.yml records it (docs/releases.md, "Bounds") | last measured 11-22 on v20261122.02: the lane's first login 407 → 241, −166.3 [−174.7, −160.3] (report.yml run 45018301368, 20 boots). On v20261122.01: 404 → 250, −153.9 [−162.5, −151.2], sshd accepting 242 → 197 (lab 44978079694, 20 boots). 11-20: 445 → 240 (lab 44750322384) |
| A workspace's cgroup at a higher `cpu.weight` while it boots or restores, on a busy host (spin's runner owns the cgroup) | restore, as a busy host sees it | with 16 workspaces keeping the 16 CPUs busy, the restored first login is 306 ms against 115 idle, and QEMU's threads wait 282 ms for a CPU (lab 44925573288). The same weight ratio as QEMU at nice −10, set as the crowd at nice 10: restored −79.3 [−93.5, −71.0], cold −104.7 [−258.3, +150.9], within noise (lab 44927312303) | spin's `restore-together` or a crowd on its runner, with the weight raised through the workspace's cgroup and set back after the login, against without it | a proposal for spin. Measured in one cgroup, where each thread competes alone. In spin each workspace is a cgroup, and the weight is shared by its threads. The idle host has nothing to win (Measured and rejected, "Memory and the host") |
| journald's, logind's and `user@`'s seccomp and sandbox settings emptied: `SystemCallFilter=`, `Restrict*=`, `Protect*=`, `MemoryDenyWriteExecute=`, `LockPersonality=` (#574, experiment 4, a bound) | cold | the executors' CPU before the login: journald 6.0, logind 10.3, `user@0` 2.8 ms, p50 of 5 ledgers (lab 45120692382) | the bound read first login −14.1 [−18.5, −11.5], sshd accepting −13.2 [−16.0, −10.9] at 60 a side (lab 45121038332). Next: split it into its seccomp and namespace halves, then one seccomp filter per unit in systemd, which keeps the function (#637) | open, #637. A bound and never a candidate: the settings are function |
| The restore with more than 2 GiB of RAM | restore | unmeasured (#242's checklist) | `restore-first` with `plugged` above 2048 | open |
| A workspace's 2048 MiB at boot (`memory=2048 plugged=0`) instead of 128 plugged to 2048 through virtio-mem during the boot (#561) | cold | the plug and the onlining of 2048 MiB on the one vCPU while the boot runs; separate runs read −15 nested and −8 metal (v20261122.06, labs 45074465204, 45074579100, 45074680619, 45078339515), so the ceiling is ~15 ms. Prior art: the kernel onlines hotplugged memory strictly in sequence, one memory block after another, and deferred struct page init, which spreads the boot's own memory over threads, does not apply to it (David Hildenbrand on linux-mm, [lore](https://lore.kernel.org/linux-mm/87v9disj2s.fsf@oracle.com/T/)). Squeezy (arXiv 2411.12893, 2024) measured virtio-mem's operations in seconds at GiB sizes and found init_on_free slows a plug. Neither measured a boot. The kernel has init_on_alloc and init_on_free off | `ssh-first`, the two shapes as two trees of one run (`--opt` on a tree), 40 a side on bench and on bench-metal, `restore-first` beside them; 3 ledgers of each shape for the onlining's CPU | a proposal for spin, spin-stack/spin#1660. Measured as two trees of one run (v20261123.04, spin's login): cold first login −5.5 [−10.9, −0.7] nested at 80 a side (lab 45111507862; −2.5 [−7.3, +2.0] at 40, lab 45110554589), −7.0 [−11.4, −3.4] on metal at 40 (lab 45110570912); restored login −2.5 [−3.7, −1.0], the state −17.6 MiB, QEMU's RSS −26 MiB (labs 45110562994, 45111507862). The ledger (lab 45110580480, 3 boots each) shows `kworker/0:1` on the vCPU ~8 ms before PID 1 when plugged and not at all when the memory is at boot, which costs the kernel's own setup ~4.5 ms more: PID 1 −3.4 ms. The probes keep spin's shape until spin changes it |

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
- **`machine.Type` off pc-q35-11.1.** QEMU is 11.2 and the machine is still pc-q35-11.1
  (`machine/shape.go`, `internal/build/machinetype.go`). It is a change of its own.
- **The kernel CVE judge.** 9 CVEs apply with a fix later in 7.3, and 1 has no fix (`0ce27ad`,
  against 7.3.1; the kernel is 7.3.5 on 2026-11-20).
- **The min_ttl_ms evidence disagrees.** `47aa681` and the LRU_GEN comment in `kernel/Dockerfile`
  say different things (docs/log.md, 09-28).
- **The guest clock on 09-30.** `1d5e7f5` blames chrony, which had left the image on 09-10
  (`c7af4d3`). The fix (time by `/proc/uptime`) stands either way.
