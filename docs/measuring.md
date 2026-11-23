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
   no-systemd floor bounds what systemd costs. `releases measure` measures it beside every
   release, and report.yml keeps it in the release's record (docs/releases.md, "Bounds"), with
   the floor's Δp50 against the release and its interval, read by rule 4 as any Δ is.
9. **A laptop finds leads.** It does not decide anything. Its noise has read 61 against 105 ms
   minutes apart (`de9b7db`), and it is not nested.

The decision itself is AGENTS.md's. A change goes in when it moves one of the two numbers,
interleaved against the release, without making the other worse. It also goes in when it is
functional.

## Boot charts

A boot chart shows where a boot's time went: which processes ran, when, and what they waited
for. It finds where to look, and a number then decides (rule 4). Each release has one in
docs/releases (`RELEASE-bootchart.svg`, linked from docs/releases.md), and any tree gets one
from the lab:

```
spin-machine-dev lab dispatch bootchart --reps 3 \
  --tree 'release=--from release --spin-machine release --edit bootchart' \
  --tree 'pr=--from release --spin-machine release --edit lane --edit bootchart'
```

The run's artifact keeps `TREE-REP.bootchart.svg` and `TREE-REP.plot.svg` beside `result.json`,
with the dependency view of the same boot: `TREE-REP.chain.txt`, `TREE-REP.order.dot` and
`TREE-REP.order.svg`.
`lab show RUN_ID` downloads them into `_output/lab/runs/RUN_ID/result/`. A browser draws them.
`TREE-REP.bootchart.json` is the chart's samples, for a comparison a script makes.

**What it shows.** The `bootchart` edit makes systemd-bootchart PID 1. It forks a sampler and
execs systemd. Every 5 ms (200 Hz) the sampler reads each process's CPU time and wait, and the
CPU's and the disk's use, until the first login's command signals it. The chart's clock is the
kernel's, from 0 at the kernel's start. `pid1` is where the sampling starts, 82 ms p50 on
v20261122.03. `chart_end` is where the first login's command ran, 320 ms p50 on the same release
(lab 45037354657, bench, spin's shape, 3 boots). The chart ends there, with the command itself as
its last process. Each process is named by what it became, its `comm` read again whenever it
ran, and labelled with its unit, read from its cgroup v2 path. Each bar is drawn from its start to
its exit or to the chart's end. Its colour is the CPU it used (blue) and the time it waited to run
(yellow), with an orange strip where it read or wrote, as the graph's title says. "Top CPU consumers" sums each process's CPU time over the chart, and "Top CPU consumers
by unit" sums it by unit. Every process that used CPU while the chart ran is drawn, however
briefly. Only what did not run, idle kernel threads mostly, is left out. The graphs above the
bars are the whole guest's. IO read and write are the kilobytes the kernel sent to its disks
(/proc/vmstat's `pgpgin` and `pgpgout`), averaged over 250 ms and each scaled to its own peak, so
a few kilobytes fill the write graph (#538). A boot that writes nothing after the chart starts
draws an empty write graph. CPU pressure, under CPU utilization, is the share of each 5 ms in
which some task waited for the vCPU. There is no graph of the summed wait: it counted two waiting
tasks twice and sat at its ceiling on one vCPU (#534). `systemd-analyze plot` is the same boot drawn
by unit: when each unit started activating and when it was active.

**Reading the plot.** The plot is taken once the boot has finished, after the probe's three later
logins (the chart, its data, the plot itself), so it runs past the first login. A dashed green line
marks the first login at `chart_end`. Four things on it are easy to misread, all seen on
v20261122.04's plot (release run 45041820698, first login at 340 ms):

- Right of the line came after the login and did not hold it: udevd's readiness at 395 ms,
  a session scope near the plot's end (`session-4.scope` at 418 ms on .04, `session-5.scope` on
  .06), which is one of the probe's later logins and never the first, and the getty and
  `multi-user.target`, which wait for the serial console's device. The first login's own session
  scope ended before the plot was taken, and the plot leaves out a unit that is gone.
- A red bar is a unit's activating time on the wall clock, and on one vCPU that includes the time
  its process waited to run. `systemd-logind-varlink.socket`'s 18 ms and `tmp.mount`'s 16 ms both
  end at 175 ms, when PID 1 ran again after forking five services. The plot says when. The chart
  says whether the time was CPU or waiting.
- A stretch where no unit changes state is PID 1's or the kernel's own work: 110 to 151 ms, after
  the unit files load. The chart's PID 1 row and its CPU graph say which (#521).
- `dev-vda.device` reads activating from 110 ms to the plot's end. No unit queues a job for it and
  none depends on it: PID 1 marks it `tentative` at its start, because root is mounted from it,
  and `plugged` when udevd announces the disk, and the plot draws tentative as activating (debug
  boots of v20261122.06, lab 45070718529). Nothing waits for it.

**Reading the chain and the graph.** Once the boot has finished, the probe asks
`systemd-analyze critical-chain` about the units the login waits for: on spin's path the forwarder
that stands for spin's supervisor, `ssh.service` and the login user's manager (`user@0.service`);
on the lane, `sshd-vsock.socket` and the user's manager. `TREE-REP.chain.txt` keeps that, and below
it the login user's own manager's chain (`systemd-analyze --user critical-chain`).
`TREE-REP.order.dot` is `systemd-analyze dot --order` restricted to edges whose two ends are both
on the system's chains, and `TREE-REP.order.svg` is graphviz's drawing of it. Every edge there is
green, an `After=`: an arrow from A to B says A is ordered after B.

- A unit's `@` is when it became active, in ms **from PID 1's start, not the kernel's**. The plot
  and the chart use the kernel's clock: add `systemctl show -p UserspaceTimestampMonotonic`, 87 ms
  on v20261122.06. Its `+` is how long it was activating.
- Each line's child is the unit it was ordered after that became active last. systemd-analyze
  infers that from `After=` and the times. A unit on the chain was ordered behind its child, and
  the time between the child's end and the unit's start was the unit's wait for its ordering, or
  PID 1 starting it. Time a unit's process spent waiting for the vCPU is inside its `+`, and the
  chart says how much.
- A chain ends at `-.slice` and `-.mount`, active at PID 1's 21 ms on v20261122.06 (108 ms of
  the kernel's clock): what happens before that is in the plot and the chart, not here.

On v20261122.06 (lab 45070438471, bench, spin's shape, boot 2): the forwarder became active at
161 ms, 3 ms after `basic.target` at 158, which came after `sockets.target` and `docker.socket`,
after `sysinit.target` at 149, after `systemd-tmpfiles-setup.service` (123, +25), after
`systemd-journald.service` (63, +55). `ssh.service` was active at 123 ms (107, +16). The graph
shows the same: the forwarder's arrows go to `ssh.service` and to `basic.target`. A chain says
what a unit was ordered behind, never that the login waited for it. With the forwarder moved off
`basic.target` it became active ~33 ms sooner and the first login did not move (labs 45071549627,
45071102677): the login's session waits for `systemd-logind.service`, which `user@0.service`'s
chain shows active at the same time in both (#553). Only `ssh-first` says whether a chain holds the login.

**What it costs.** The sampler runs on the guest's one vCPU beside the boot it draws. With the
edit, `ssh-first` read the first login +10.4 ms [+4.5, +16.8], sshd accepting +8.5
[+4.6, +11.7], the console prompt +15.7 [+12.3, +19.7], and QEMU's RSS +4.0 MiB (lab 45036503632,
bench, spin's shape, 20 a side, v20261122.03). The chart names the sampler `bootchart`, and its header
says how much CPU the sampler used. So a bar on a chart is longer than it is without the sampler. The probe's own
`first_login` is the login that stops the chart, its command included: 428 ms p50 in lab
45037354657, beside `ssh-first`'s 422 with the edit. Neither is a goal number.

**How to compare two charts.** Compare charts from one run, and the probe boots its trees
interleaved, as every probe does. Two charts taken apart are two machines (rule 3). Read three
repetitions of each tree, because one boot's bar can be another's outlier. Then look for what
moved:
- a process that starts later, and the one it waited for, shown by the dashed line to its
  parent;
- a bar whose yellow grew, which waited for the vCPU behind something else;
- "Top CPU consumers", a process's CPU time, which is less noisy than when it ran.

A chart that shows something to cut becomes an experiment with a budget row (rule 1), measured by
`ssh-first`. Release to release, docs/releases.md links each release's chart beside the one
before it.

**What it is not.**
- It is not a goal number, nor a phase's cost. A bar is wall time with the sampler running, on a
  vCPU that everything shares. "Blame, the critical chain and log gaps are not savings" holds for
  it too.
- It does not see before PID 1. QEMU, the firmware and the kernel's own time are before `pid1`.
  `initcalls` and `boot:firmware-stages` see them.
- It does not see every process. One that starts and exits between two samples, 5 ms apart, is
  never seen. The unit plot has each unit.

## The ledger

A ledger accounts for one boot from the moment before QEMU is exec'd to the first login, and
every millisecond in it has an owner. Where a boot chart samples every 5 ms, the ledger reads the
scheduler's own events from the kernel's start, so the time between two of them belongs to
whichever task the vCPU ran. It finds where to look. A number then decides (rule 4).

```
spin-machine-dev lab dispatch ledger --reps 3 \
  --tree "release=--from release:TAG --spin-machine release --kernel traceable:TAG --edit ledger \
    --append '<lab.LedgerAppend>'"
```

**It needs a kernel that can be traced.** Lockdown at confidentiality takes tracefs away
(kernel/README.md), so a release's own kernel records nothing. `traceable:TAG` is the kernel
published beside release TAG, lab-only: the release's source and config with
`CONFIG_LOCK_DOWN_KERNEL_FORCE_NONE=y` in place of confidentiality and every other option held to
the release kernel's (#536). Releases before the first one built with it have none. `releases
measure` takes one ledger of each release on it, beside its boot chart, and its record keeps it as
`docs/releases/TAG-ledger.txt`.
`--append` takes `lab.LedgerAppend` as it stands in internal/lab/ledgerprobe.go: the scheduler's
tracepoints from the kernel's start, on CLOCK_MONOTONIC, into a 2 MiB buffer that drops what
does not fit rather than the boot.

The run's artifact keeps `TREE-REP.ledger.txt`, `TREE-REP.ledger.json`, `TREE-REP.trace.txt` and
`TREE-REP.journal.txt` beside `result.json`. Perfetto opens the trace as it is.

**What it shows.** The table cuts the kernel's clock into windows where the boot and the login
change hands. Each cut is an event in the trace: PID 1's exec of init, sshd-session's exec,
sshd-auth's exec and exit, the fork of the login's process, its exec of the shell and of
`/usr/bin/true`, and true's exit. Each window lists its CPU by owner, and idle. An owner is a task's
unit, read from its cgroup after the login or from systemd's `ExecMainPID`, else its parent's.
Kernel threads are one owner. PID 1 is the kernel's until it execs init, because the kernel's
initcalls run in it. On spin's login (`login=spin`) PID 1 execs the lab's stand-in for spin's
handoff first, a shell that places spin's host key and execs systemd, and the ledger gives it its
own window, `spin's handoff, to PID 1's exec of systemd`, with its length recorded as `handoff`.
Its mounts and its `mkdir` are `unattributed`. It took 6.3 to 7.9 ms in three boots, 2.3 to
3.7 of them idle on cold reads of the shell and `mount` (lab 45088516505, v20261123.01, 2026-11-23).
systemd mounts `/proc` and `/run` itself when no one has, so part of that window is moved from
systemd's. spin's own vminitd runs before systemd too, does more, and is not measured here. A task forked by PID 1 that nothing names is `unattributed`. The generators
and the executors that exited before the login are the usual ones. A task named `9` is
systemd-executor, which PID 1 starts by its file descriptor. A second table gives each owner's
time from PID 1 to the login by state: running, runnable (waiting for the vCPU), disk
(uninterruptible) and sleeping. The journal's lines for the login's phases come last, at
journald's receipt of each. They are no earlier than when they were written, so the windows are
cut from the trace, not from them.

**Why it adds up.** On each CPU the time from one switch to the next is the task the first one
put there, so the owners of a window add up to its length. That holds by construction, unless the
trace lost events. A switch away from a task the CPU was not running counts as a break, and
`breaks` is 0 in every boot so far. The parts that do not hold by construction are these:

- *The kernel's 0 on the host's clock.* The host asks the guest for 20 markers in the trace,
  50 ms apart. Each marker was written after its request left and before its answer arrived, so
  each puts the kernel's 0 in a bracket. The brackets agree only if the two clocks run at one rate.
  Spread over a second they met within ±0.15 ms (lab 45062836688). `kernel_zero` is that 0, and
  everything before it (spin-machine, QEMU, qboot, the decompression) is one span.
- *The residue.* That is `first_login` less `kernel_zero` and the command's exit on the kernel's
  clock: sshd's answer crossing vsock and the client reading it. It read 0.4 to 1.2 ms in six
  boots. A residue that is negative or large says the alignment is wrong.

`kernel_zero` read 92 to 113 ms. The first boot of each run read ~112 and the others ~93
(labs 45062548343, 45062836688). `boot:firmware-stages` read 73.7 ms from QEMU's exec to the
kernel's clock on the same bench (lab 45062836494). The ~20 ms more in the ledger start before
QEMU: spin-machine's own start and the held start's QMP. That is inferred, not measured
(#537).

**What it costs.** The trace and sshd's DEBUG1 run on the guest's vCPU beside the boot they
record. In spin's shape, 20 a side, interleaved (lab 45063070048, bench, v20261122.05):

| arm, against the release | first login | sshd accepts | console prompt | second login | QEMU RSS |
|---|---|---|---|---|---|
| the traceable kernel alone | −1.0 [−9.5, +4.9] | −5.6 [−12.1, +2.8] | +0.8 [−4.1, +4.7] | +1.0 [+0.3, +1.4] | +0.0 MiB |
| the kernel, the trace and the edit | +3.0 [−2.4, +15.9] | +5.1 [−0.1, +15.6] | +12.1 [+7.1, +17.6] | +2.2 [+1.1, +2.8] | +2.0 MiB |

The first login is within noise, and its interval reaches +16, so a ledger's numbers may run up
to ~16 ms long. The boot chart's sampler cost +10.4 [+4.5, +16.8] (above). The second login's
+2.2 is sshd logging at DEBUG1. The probe's own `first_login` is a login with the trace
running, and it is no goal number.

**What it is not.** It is not a goal number, nor a saving. CPU on a window's path says what the
vCPU did. It does not say what the window would have been without it: a task that waits for the
login may run in the login's window whether or not the login waits for it. It does not split
the time before the kernel's clock. It names a unit only for tasks it can still see after the
login, or that systemd remembers.

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
    4.1 ms on the restored login (lab 44978246896), and 4.3 ms of `ssh-first`'s accept to login
    (lab 44987441326). `ssh-first` now logs in over the connection that timed `accepts` (#450).
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
  - spin's login without spin's host key handed over paid the image's keygen, which spin's own
    path skips: ~7 ms of every cold login (#515). `login=spin` hands it over since #575.
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
