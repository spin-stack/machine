# How to measure a boot-time change

This page is the method behind the goal's two numbers (AGENTS.md, "The goal"). Where the numbers
stand, where their time goes, and what was kept, rejected and is open are in
[boot-time.md](boot-time.md). The lab's commands are in [cli.md](cli.md#measuring).

## Start an investigation

1. Find the phase in `docs/boot-time.md`, "Where the time goes".
2. Look the idea up in "Measured and rejected" and in "What the machine carries". An idea
   already there needs a new reason, not a new run.
3. Write its row in "Open experiments" first: the phase, its p50, the ceiling, the rule that
   decides it.
4. Measure it with the rules below. The commands are in `docs/cli.md`, "Measuring".
5. When it is decided, move the row: to "What the machine carries" when it is kept, or to
   "Measured and rejected" when it is not. Then write its entry, dated and with its run, in `docs/log.md`.

## Follow the rules of a measurement

1. **Write the budget entry first.** Before an experiment runs, write its row in "Open
   experiments". Name the phase it attacks in `docs/boot-time.md`'s budget tables, the phase's p50 today, its
   ceiling (what the goal number gains if the phase cost nothing), and the result that would
   decide it. An experiment whose ceiling is under the noise needs more boots before it needs a
   run.
2. **Measure spin's shape.** Boot the CPUs, memory, ceilings, disks and login that spin gives a
   workspace (the probes' defaults), and name the shape on every table. A gain in another shape is
   a lead.
3. **Compare only inside one interleaved run.** The arms of a comparison boot in turn on one
   host, 20 boots or more each, in one lab run. Two runs are two machines: never subtract a
   number in one run from a number in another. Runs may go at once, each on its own bench
   instance.
4. **One acceptance rule.** Report Δp50 with its bootstrap 95% interval. A change is a gain only
   when that interval excludes 0. At 20 boots a side the interval is about ±7-9 ms. A smaller
   effect needs more boots, not a second look, and halving the interval takes about four times
   the boots.
5. **Deleting follows the same rule, read the other way.** Something carried is deleted when the
   Δp50 of taking it back includes 0 and its upper bound is under +5 ms. Taken back means the
   distribution's or upstream's own value. An item for boot time is read on the cold row, because
   a restored guest does not boot. The restore rows only show that nothing broke, and at 20 boots
   they move ±13 ms on their own. An upper bound between +5 and the noise needs 40 boots.
6. **One change per arm.** A comparison that decides a change carries that change and nothing
   else. A floor or a bound is different. It may combine every change its question needs,
   because it answers what is possible, not what one change is worth.
7. **An instrument must see the phase it claims.** The kernel's "Freeing" clock starts after the
   bzImage is decompressed, so it recorded the bzImage's cost as zero while sshd accepted ~10 ms
   later. Before a phase's number is trusted, check that something on the other side of it moved
   by the same amount.
8. **Measure the bounds each release.** A bound that is not re-measured stops being one. The
   no-systemd floor bounds what systemd costs.
9. **A laptop finds leads.** It does not decide anything. Its noise has read 61 against 105 ms
   minutes apart (`de9b7db`), and it is not nested.

The decision itself is AGENTS.md's. A change goes in when it moves one of the two numbers,
interleaved against the release, without making the other worse. It also goes in when it is
functional.

## Avoid the pitfalls

Each of these made a number say something it did not measure.

- **The instrument measured itself.** Quiet consoles, `--profile` and a check that the log is
  whole now prevent it. The cases:
  - initcall_debug with a serial console (`c03ed89`);
  - a wide function_graph filter, 26× inflation (`a31d7b2`);
  - the `kvm:kvm_pio` tracepoint, 49.3 vs 27 ms (`c049a21`);
  - `trace_event=initcall` (`64d2901`);
  - systemd debug logging, 212 vs 26 ms (`ad05544`);
  - a shell harness throttling the console, 1424 vs 190 ms (`80f984b`);
  - a log_buf_len of 8M, costing 4.9 ms (`5221690`);
  - the probes' own logins left open, which a checkpoint then carried: ~5 ms on the restored
    login (lab 44809015139, fixed by #341);
  - a connection to port 22 that only asked whether sshd accepts, before each login. On spin's
    path the forwarder opened one to sshd, and sshd started a session for it beside the login:
    4.1 ms on the restored login (lab 44978246896). `ssh-first` still asks, to time `accepts`.
- **The instrument could not see the phase.**
  - The kernel's "Freeing" clock misses the bzImage's decompression. `boot:firmware-stages` now
    times it from kvm-clock: ~21 ms (lab 44787491631).
  - `restore-first`'s `read` read the whole mapped-ram file, whose length is the RAM size and
    which is mostly holes: 2 GiB read for 155 MiB of data.
- **Blame, the critical chain and log gaps are not savings.** logind was 63 ms in blame and worth
  24, chrony 43 and worth 10, and udev 46 and worth 0 (`6aba37b`, `e2f6b8f`).
- **Misattribution.** BTF's first parse looked like XFRM (`e285dec`). ksm/kcompactd's 2.00 ms is
  HZ resolution (`cda04b1`). vCPU descheduling was counted inside initcalls (`64d2901`).
- **Markers that come too early.** The QMP greeting comes before the board is built (`8eef89d`).
  SeaBIOS's banner comes before hardware init (`6aba37b`, #45).
- **Variants that did nothing.**
  - `systemd.mask=` with the debug-generator masked (`80f984b`);
  - an `unmask_udev` that had become a no-op (`b5f361f`);
  - `rm -f` on an `/etc` path that did not exist (`bb6bb89`);
  - a mask that matched no file (image/build.sh now refuses one);
  - a lab arm whose commit staged half of what it meant to change: #345's arm built the patches'
    removal without the config, and the options went unmeasured until lab 44839538785.
- **A guest that is not spin's.**
  - A 128 MiB guest never plugged runs its OOM killer through the boot. It lost boots in
    `initcalls` and stalled ~400 ms in ~20% of restores (labs 44784196287, 44788801555).
  - The lane's login moved numbers that spin's login does not have.
- **Host noise.** Interleaved boots on the bench fleet, one lab run on each instance, now prevent it.
  The cases:
  - a cold page cache right after `task build`: 98 ms read as a 35 ms regression (`f32d527`);
  - a host building a kernel during the run (`9b5f0dd`);
  - three runs of one kernel reading 59.5/96.8/167.7 ms (`5221690`);
  - 3 boots a row, which is 5-11% noise (#81).
- **Two runs are two machines.** Absolute numbers drift between runs by as much as a release moves
  them:
  - v20261120.01's restore read 158.9 in run 44641240861 and 167.0 in 44727451080;
  - main's kernel read a cold 467 in lab 44839538785, against v20261120.04's 456 in the report.
- **Different hosts compared.** The v20261026.01 notes compare a Ryzen 9 5900X with a Xeon
  6975P-C. Numbers from construct (`docs/memory-and-disk.md`, kernel patches 0001-0004, qboot) do
  not compare with the bench fleet's.
- **Nested KVM leaks between repetitions.** A cold boot after a restored guest ran on the host is
  ~1.8× slower (#246). `restore-first` now runs every cold boot before every restore and checks
  with mincore that the state is out of the page cache.
- **The page cache.** `POSIX_FADV_DONTNEED` declines recently touched pages, 4.8 vs 251 ms
  (`ab4844c`). `mincore` lies for files the caller does not own (`21a830c`). fstrim right after rm
  trims nothing (`251638d`).
- **Lab plumbing.**
  - green on a skip (`8d8e97b`);
  - concurrency groups cancelling queued runs (`a3ba5a6`);
  - 108-byte socket paths (`299777b`, `f2c47f7`);
  - `modprobe` on a module-less kernel (`5a70721`, `213cd07`);
  - BuildKit comparing a file with itself (`b9d1985`);
  - a tree whose QEMU predates an argument main's spin-machine passes, refused at boot.
