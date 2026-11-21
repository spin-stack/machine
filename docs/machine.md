# The machine

`machine/` is the definition. It is Go rather than a document because a definition that
nothing executes drifts. `spin-machine-dev shell` boots a VM through it, so a slot that moved or a
kernel argument that stopped working fails here first.

```go
rel, err := machine.OpenRelease("/usr/share/spin-stack")  // says which file is missing, if one is
spec, err := rel.Spec("kvm")                              // QEMU, Kernel, Firmware, Accel; "tcg" takes the TCG build
spec.Initrd = …                                           // the caller's: a release carries none
spec.BootCPUs = 2
spec.Memory = machine.Memory{BaseMB: 2048}
spec.Places = []machine.Place{{Serial: "root"}}           // a place: the images come at Boot
spec.VsockCID = 7
spec.Monitors = []machine.Monitor{{Socket: "/run/vm/qmp.sock"}}
spec.Cmdline = machine.DefaultCmdline()

m, err := machine.Start(ctx, spec, machine.StartOptions{Stderr: w}) // ctx bounds the start alone
defer m.Stop(ctx)
here, err := machine.ReadTarget(ctx, spec, machine.StartOptions{}) // what a machine of spec is: read once for a host
err = machine.CanResume(saved, here) // nil, or a *machine.Refusal naming every difference
err = m.Resume(ctx, state, saved, here, machine.LoadOptions{}, root) // trusts here: QEMU is not asked again
```

`Args` validates first. Each spec it refuses would otherwise boot and be subtly wrong: two disk
places with one serial, a monitor that is both a path and a descriptor, or a kernel command line
the kernel would truncate. A disk's images arrive later, at `VM.Boot`, `VM.Resume` or
`VM.AttachDisk`, and each refuses an image whose format is not qcow2 or raw rather than guess it.

## Opening a disk

QEMU reads no image by path and follows no backing file a header names. It reads the descriptors
a `machine.Disk` hands it. `machine.OpenDisk` opens them from a path:

```go
root, err := machine.OpenDisk(machine.DiskPath{
	Serial: "root", Path: "/var/lib/spin/ws/root.qcow2", Format: "qcow2",
	Under: []string{"/var/lib/spin/layers"}, // where the images below the top may be: files or directories
})
defer root.Close()                            // after Boot: QEMU holds its own copies
```

- **The base is never opened for writing.** The top is opened read-write unless the disk is
  `Readonly`, and every image below it read-only. A writable top that is one of `Under`'s images
  is refused, whatever path or link names it.
- **The chain is the headers', held to `Under`.** A backing file a header names is the choice of
  whoever wrote the image, the guest's for an image the guest wrote. Each is opened `O_PATH`,
  resolved through its descriptor, and refused unless it is a regular file under `Under`. Only
  then is it reopened through that descriptor, so what was checked is what QEMU gets. A chain that
  comes back on itself, an image that names a backing file and not its format, an encrypted image
  and one with an external data file are refused, each error naming the file.
- **Read in process, not by qemu-img.** On one three-image chain, `qemu-img info
  --backing-chain` took 3.4 ms p50 and `OpenDisk` 0.05 ms (laptop, 2026-11-21), and boot and
  restore open their disks on the goal's clock. The reader opens nothing it has not checked and
  runs nothing, which is what a Landlock sandbox around qemu-img bought. Every read is bounded by
  the header's first cluster.

## Starting and ending one

`machine.Start` owns one QEMU process, from its start to its exit. It returns a
`*machine.Process`, which is the `*machine.QEMU` its own monitor drives with the process under it:

```go
m, err := machine.Start(ctx, spec, machine.StartOptions{
	Files:  []*os.File{tap},    // descriptors 3, 4, … as the Spec numbers them: what only the caller opens
	Stdout: w, Stderr: w,       // QEMU's output; Start keeps stderr's last 4 KiB besides
	Console: console,           // optional: the guest's console through a pipe Start reads
	Cmd: func(c *exec.Cmd) { c.SysProcAttr = …  }, // uid, chroot, namespaces, cgroup
})
<-m.Exited()                    // QEMU has exited and been reaped; Close of the monitor does not close it
e := m.Wait()                   // machine.Exit{State, End, Panic, Stderr}
e = m.Stop(ctx)                 // the power button, quit, SIGKILL: each step bounded
e = m.Kill()                    // SIGKILL, whatever QEMU is doing
q, err := machine.Dial(ctx, socket, qmp.Observer{}) // a second monitor, or one of a QEMU started elsewhere
```

- **QEMU opens no path.** Start binds a listening socket at each monitor's `Socket`, opens
  `/dev/kvm` under KVM, `/dev/vhost-vsock` for a vsock, `/dev/vhost-net` for each NIC and a
  `file:` console, and hands them over as descriptors, before `Cmd` runs. A QEMU run as another
  user, in a root with no `/dev`, starts the same machine. The TAPs and the disks stay the
  caller's: they are its network and its storage.
- **Readiness is the monitor's answer, with nothing polled.** Start binds a monitor of its own
  and connects to it before QEMU exists: the connection waits in the socket's backlog and QEMU
  accepts it as it starts. `qmp_capabilities` is answered by QEMU's main loop, which runs once
  every device on the command line is made, so Start's return means the machine exists and is
  held. A QEMU that exits first, refusing a device or a CPU, fails Start at once with its own
  words (`Property 'virtio-balloon-pci.x' not found`, `Host doesn't support requested
  features`). One that never answers fails it after 30 s, or when ctx ends, and is killed.
- **`Exit` says how it ended.** The exit status or the signal, the last `SHUTDOWN` QEMU sent
  on Start's monitor, the guest's panic line when Start read the console, and QEMU's stderr's
  end. `Done` closes when the monitor ends, by QEMU's exit or by `Close`, and `Exited` only when
  the process has gone.
- **`Stop` asks before it kills.** A running guest's power button, then up to 3 s for it to power
  off; QEMU's quit, then up to 1 s; then SIGKILL. A monitor that does not answer within 1 s is
  a QEMU loading a state, which answers no monitor until it is done, or a monitor closed: Stop
  kills it then. ctx bounds every step but the kill. A second Stop returns the first's Exit.

## Driving a running machine

`machine.VM` is what any VMM behind the machine answers. `machine.QEMU` adds what spin's runner
needs of a QEMU, so that the runner reaches QEMU through `machine.QEMU` alone:

```go
n, err := m.CPUs(ctx)            // vCPUs now; SetCPUs(ctx, n) plugs and unplugs, also before Boot
size, err := m.Memory(ctx)       // MemorySize{Now, Base, Ceiling, Block} in MiB; SetMemory(ctx, mb)
mem, err := m.GuestMemory(ctx)   // the guest's last balloon report; machine.ErrNoGuestStats until one
st, err := m.Status(ctx)         // machine.Prelaunch, Inmigrate, Running, Paused, Postmigrate, …
saved, cost, err := m.Checkpoint(ctx, f, machine.SaveOptions{}, seal) // cost: Paused, Total, RAM
err = m.Hold(ctx)                // a checkpointed machine takes its disks back and stays stopped
err = m.Continue(ctx)            // and runs
err = m.Shutdown(ctx)            // presses the ACPI power button
<-m.Done()                       // the monitor's connection ended: QEMU exited, or Close
<-m.Exited()                     // QEMU's process ended (a Process from Start)
end, said := m.End()             // the last SHUTDOWN QEMU sent: End{Guest, Reason}
```

- **`Shutdown` presses the power button, never ctrl-alt-del.** The guest's kernel has no i8042 or
  AT keyboard driver, so QEMU takes the keys and the guest never sees them. Booted to systemd, a
  machine sent ctrl-alt-del ran on until it was killed, and one whose button was pressed powered
  off 0.64 s later (laptop, 2026-11-21). `End` then says `GuestShutdown`.
- **`End` is QEMU's word, not an explanation.** A guest that powers itself off ends as
  `GuestShutdown`. A panic ends as `GuestReset`, because `panic=1` reboots the guest and
  `-no-reboot` makes the reboot an end. The machine has no panic device, so QEMU never sends
  `GUEST_PANICKED`, and only the console tells a panic from a reboot.
- **`GuestMemory` turns the reports on at its first call**, one a second, by setting the
  balloon's polling interval. Set on the command line, the interval would put a timer in every
  boot from its first second. The balloon's id (`balloon0`) gives it a path. The record leaves
  every id out and names the balloon by its slot, so the id did not move it.
- **`Memory` is two round trips**: `query-memory-size-summary` and one `qom-list-get` of
  virtio-mem and its backend. A machine started with no ceiling above its memory has no virtio-mem,
  and its `Ceiling` is its `Base`.

## Pressure

A runner hosts many machines. The probe says which of them is short of CPU, memory or IO, which
is held at a limit spin set on its cgroup, and which QMP commands are slow, as it happens. `Usage` then says which machine causes it. The machine
owns both ends: a sender in the guest and the listener on the host. spin keeps what spans machines:
the host's own PSI, ranking, attribution and what to do about it.

```go
l, err := machine.ListenPressure()  // once per host, as root: vsock port 1032
defer l.Close()
p, err := machine.Start(ctx, spec, machine.StartOptions{
	Pressure: l.Watch(func(e machine.Pressure) { /* must not block */ }),
	Cmd:      func(c *exec.Cmd) { /* QEMU in a cgroup of its own */ },
})
u, err := p.Usage(ctx)              // counters; two Usages say what was used between them
```

`machine.Pressure` is one of these types:

| Type | Says | Seen by |
|---|---|---|
| `Stall{Seen: Guest, Resource}` | the guest's tasks waited for a vCPU, for memory it does not have, or for its disks: at least 150 ms in 1 s, by the guest kernel's PSI | the guest, as `spin-machine-pressure.service` |
| `Stall{Seen: Host, Resource}` | QEMU's threads waited for the host's CPU, memory or IO, by the PSI of QEMU's cgroup | the host |
| `OOMKill{Kills}` | the guest's OOM killer killed that many processes since the last report | the guest |
| `SlowCommand{Command, Took, Baseline}` | a QMP command on Start's monitor took at least 100 ms and 4 times what it took before: `query-blockstats` took 536 ms against 14 while host hogs shared QEMU's one core (laptop, 2026-11-21) | the host, in the monitor's one client |
| `GuestLost{Err}` | the guest's sender's connection ended, and the guest is blind until it dials again | the host |
| `Limited{Limit, Device, Set, Used, Events, Since}` | in the last second, a limit on QEMU's own cgroup held the machine: `cpu.max`, `memory.high`, `memory.max` (and its OOM kill), or one key of `io.max` on one device | the host, from QEMU's cgroup each second |

Each Stall carries `Total`, the resource's stall since the guest booted or the cgroup was made.
Two Stalls of the same vantage and resource say how much stall fell between them.

**A Stall says a machine suffers. `Usage` says who causes it.** A guest stall on CPU with no host
stall is a guest short of the vCPUs it was given. A host stall on CPU is the host taking its vCPUs:
the guest sees none of that time. Its own PSI sees only the time its tasks waited for a vCPU it was
running. Both were seen on one guest of one vCPU (task boot:pressure, laptop, 2026-11-21). Three
loops in the guest brought guest CPU stalls each second and no host ones. One loop, with two host
hogs on the vCPU's core, brought host CPU stalls each second. The guest's CPU total did not move
then. A load rarely stalls one resource alone: two readers of the disk on that one vCPU brought
guest IO stalls, and guest CPU and host CPU stalls with them. Who causes a host stall is in every machine's `Usage`: QEMU's cgroup's `cpu.stat`,
`memory.stat` (dirty and writeback pages), `io.stat`, each disk's `query-blockstats`, and the
balloon's count of the guest's OOM kills and allocation stalls. Its `Stalled` fields are the
cgroup's PSI totals. A host's IO is someone else's IO only when the disks are on a disk. A disk
on tmpfs is memory to the host (#394).

**Short of X, held at its X limit, or squeezed by the host.** These are three different answers.
A guest Stall says the machine is short of X. A `Limited` says spin's own limit is why. A host
Stall with no `Limited` says the host is squeezing it. A host Stall alone cannot tell the last two
apart. Under `cpu.max` of a fifth of a CPU, one loop in a guest of one vCPU brought a host CPU
Stall each second, as hogs on its core do. It brought a `Limited` of `cpu.max` each second as well,
with `Used` at 199,500-200,200 µs a second and 10 throttles, one each 100 ms period (task
boot:pressure, laptop, 2026-11-21). The host reads the limits each second, since `cpu.stat` and
`io.stat` cannot be polled. A `Limited` comes again each second the limit holds, with the same
`Since`, and the last reading is taken as QEMU exits, so a machine killed by `memory.max` says so.
Each limit is read from its own files:

- **`cpu.max`.** `cpu.stat`'s `nr_throttled` moved. `Set` and `Used` are CPU microseconds a second.
- **`memory.high` and `memory.max`.** `memory.events`' `high`, `max` or `oom_kill` moved. `Set` is
  the limit and `Used` is `memory.current`, in bytes. With `memory.high` 32 MiB above what QEMU
  held, the guest reading its disk again and again brought 2,200-4,300 `high` events a second, and
  `memory.current` stayed within 0.1% of the limit. The host caches what QEMU reads in QEMU's
  cgroup. `memory.events` notifies a reader of each change, at most every 10 ms (the kernel's
  `CGROUP_FILE_NOTIFY_MIN_INTV`), and under
  `memory.high` it changed thousands of times a second. It is read with the others each second.
- **`io.max`.** A device's `rbps`, `wbps`, `riops` or `wiops` ran at 90% of its limit or more over
  the second. cgroup v2 counts nothing of what `io.max` throttled: `io.stat` gains no field from it,
  checked on Linux 7.0 with `CONFIG_BLK_DEV_THROTTLING` on (2026-11-21). QEMU's `io.pressure` does
  not show it either. Held 5 s at an `rbps` of 4 MiB/s, which QEMU read at 99.9%, its total did not
  move, since io_uring leaves no thread of QEMU's waiting. The guest's own IO stalled each second.
  So a rate at the limit is the one sign there is.

The limits are QEMU's own cgroup's. One a parent cgroup sets is counted in the parent's files, and
a disk throttled by QEMU's throttle group (#395) is invisible to the cgroup. spin keeps the network
limit and its signal (spin-stack/spin#1480). `Usage` carries the counters behind each: `CPU.Throttled`
and `CPU.Throttles` from `cpu.stat`, and `Memory.High`, `Max`, `OOMs` and `OOMKills` from
`memory.events`. The guest is never told. It does not know its limits exist.

**What each vantage cannot see.**
- **The guest's PSI is the guest's word.** A workspace's root can stop the sender or write what
  it likes to port 1032. The host's kernel names the CID of every connection, so a guest can lie
  only about itself. A host Stall and `Usage` are the host's own measurement.
- **A fast OOM can kill before any trigger fires.** Two 1000 MiB hogs did (2026-11-21). So the
  sender watches the guest's OOM kills as well, through `memory.events` of each top-level cgroup,
  which the kernel notifies on every kill. The kill count travels as a running total. A count sent
  again, or a kill made while the connection was down, is counted once, in the hello of the next
  connection.
- **A machine loading or saving a state answers no command until it is done.** The watch follows
  the migration's events and times nothing while one runs, so a restore does not alarm. A command
  asked for the first time has no baseline to be slower than, so it is never slow.
- **A guest's first 10 s are not reported**, since its sender starts after them.
- **vsock's own round trip is not measured** (#393). Under pressure, a heartbeat from a sender at
  the guest's lowest priority would measure the guest's scheduler.

**Where each part lives.** The sender is `cmd/spin-machine-pressure`, built static by the image's
build (`image/build.sh` checks it is in the image as built). Its unit is
`spin-machine-pressure.service`, at Nice 19, on the idle scheduler and IO class, and out of the OOM
killer's way. `spin-machine-pressure.timer` starts it 10 s after the boot. Started with the boot,
after multi-user.target, it ran beside the first login and cost it ~3 ms, so a guest's first 10 s
go unreported. A restored guest has it running already. It wakes only when the kernel says something changed. It holds no identity: the host
is always CID 2. The host side is `machine/pressure.go`, `machine/limit.go` and `machine/usage.go`. Start refuses a
watch for a Spec with no vsock, for a QEMU in its caller's own cgroup (one cgroup for every machine
attributes nothing), and when the cgroup's triggers cannot be armed: a window under 2 s is root's.

**The line is an external contract** between a guest of one release and the host of the next
(`internal/pressure`): newline-terminated ASCII, at most 128 bytes, microseconds, totals since the
guest booted. The host refuses a version it does not know, and the guest then dials again.

```
spin-machine-pressure 1 oom=3       hello: version, the guest's OOM kills so far
stall memory 150000 1000000 8123    a trigger fired: resource, threshold, window, some total
oom 4                               the guest's OOM kills so far
```

**What spin does to adopt it.** It makes one `ListenPressure` at the runner's start and passes
`Watch` to each `Start`, with each QEMU in its cgroup through `Cmd`, as it already does. Its pressure
handler then takes `Stall{Seen: Guest, Resource: MemoryPressure}` where its runner took the guest
supervisor's `POST /v2/memory-pressure` on port 1027. The supervisor's notice, and the runner's
handler for it, are then deleted: the trigger they arm is the same, 150 ms in 1 s.

**Next, not here.** An actuator for each signal: a per-disk IO limit through the throttle group
every disk place already sits under (#395), and CPU and memory through `SetCPUs` and `SetMemory`,
with the signal, actuator and owner table (#396). Not yet explained: the 175-950 ms QMP stalls
under small synced writes on a contended host disk (#391), and QMP's round trip during a load
(#392).

## Phases

A checkpoint stops the guest, and a caller showing it to a person needs to say for how long and
why. `SaveOptions.Phases` and `LoadOptions.Phases` take a func that is told each step of one call
as it happens. Each `machine.Phase` is a typed value with the time the host learned of it:

```go
saved, cost, err := m.Checkpoint(ctx, f, machine.SaveOptions{Phases: func(p machine.Phase) { /* quick */ }}, seal)
err = m.Continue(ctx)            // tells the checkpoint's last phase, GuestRunning
err = m.Resume(ctx, state, saved, here, machine.LoadOptions{Phases: func(p machine.Phase) {}}, root)
```

| Phase | When | Carries |
|---|---|---|
| `SaveStarted` | QEMU took `migrate` and writes the RAM while the guest runs | |
| `RAMWritten` | every 100 ms while the guest runs | `Bytes` written, `Total` of the RAM blocks at their ceilings |
| `GuestStopped` | pre-switchover: the guest stopped and its disks quiet | |
| `Sealed` | the caller's seal returned | `Took` |
| `SaveCompleted` | QEMU said `completed` | the `Cost` Checkpoint returns |
| `GuestRunning` | `cont` returned, from `Continue` or inside `Resume` | |
| `GuestHeld` | `Hold` took the disks back, the guest still stopped | |
| `LoadStarted` | QEMU took `migrate-incoming` | |
| `StateLoaded` | the whole state read: in `Resume`, or in `Loaded` after a fast load | |

The phases are per call, not per machine. spin keeps one timeline per snapshot (spin-stack/spin#1481)
and adds its own steps to it, such as the guest's flush, the upload and the publish. A func per
call needs no snapshot id to sort a machine's stream by. `Continue`, `Hold` and `Loaded` tell the
last phase of the call before them, because a checkpoint ends with the guest stopped and the caller
decides when it runs.

**They cost the goal paths nothing.** A resume's phases ask QEMU nothing more. A checkpoint's
`RAMWritten` costs one `query-migrate` every 100 ms, but only while the guest runs and only for a
caller that takes the phases. Without `Phases`, a checkpoint and a resume send the same commands as
before. The func is called on Checkpoint's and Resume's goroutines, one phase at a time and in
order. `GuestStopped` and `Sealed` are told with the guest stopped, so the func's time is the
guest's.

**What a 1.6 GiB checkpoint's phases showed** (laptop ext4, spin's shape with 2 GiB plugged and
1.5 GiB of tmpfs filled, 8 channels, 32 saves, 2026-11-21, #399). QEMU's downtime (`Cost.Paused`)
runs from the guest's stop to `completed`, the seal included. The RAM the guest dirtied last is
written after the seal: the last `RAMWritten` before `GuestStopped` was 0.3-0.9 GiB short of
`Cost.RAM`. From `completed` to `cont`'s return took 238 ms p50 while Checkpoint closed its copy of
the state's file itself. QEMU truncates the file it writes, so ext4 writes the dirty pages back on
the file's last close (`auto_da_alloc`), and that last close was Checkpoint's. Closed on a goroutine
of its own, the same gap took 6 ms p50, and the guest's stop to `cont` fell from 451 ms to 198. The
caller's `fdatasync` after `Continue` took 259 ms instead of 3, so the time to a durable state did
not change (−19 ms [−4088, +3963]). It moved off the guest's clock.

## What is load-bearing and invisible from outside

- **Every device on the command line sits at a fixed slot on bus 0.** A fixed slot makes
  the guest's enumeration order independent of the order code runs in. Because every device is
  on bus 0, the kernel can be told `pci=lastbus=0`. The flag stops the search for peer host
  bridges across all 256 buses, which costs 8192 configuration reads, each one a VM exit. Kernel
  to init went from 87.6 ms to 51.1 ms (2026-09-07, 5e7d60e). The flag bounds only that search, so the kernel would
  not find a second host bridge. Disks attached while the machine runs are SCSI disks on the
  `HotplugDisks` controller, not devices behind a bridge.
- **`vmgenid`.** A VM resumed from a checkpoint starts with the checkpoint's memory,
  including the state of the guest's random pool. Without `vmgenid`, two guests resumed from
  one checkpoint, or one resumed twice, would produce the same "random" bytes until something
  reseeded them.

## The record

`QEMU.Target` is the machine's record, read from the running QEMU over QMP. A checkpoint taken
on the machine carries it (`Saved.From`), and a checkpoint resumed into the machine is held to
it. `machine.CanResume` compares the two records part by part. It returns nil, or a
`*machine.Refusal` that names each difference, both values and the rule it breaks:

```
the checkpoint's machine is not this one: device 02.0: checkpoint none, here vhost-vsock-pci (same)
```

QEMU refuses some differences by itself and loads others silently (measured 2026-11-21): an
extra device, which the guest never finds; a larger BIOS block or more base RAM, because
mapped-ram refuses only a smaller block; a device property the migration stream does not carry,
such as a NIC's MTU or `ICH9-LPC.disable_s3`. The record names each of them. It holds:

- the QEMU version (`vmm`). A checkpoint crosses it upward only: a newer QEMU loads what an older
  one of the same `pc-q35-X.Y` saved, with the newer type's changes to defaults switched back off
  for the older type (`hw_compat_X_Y`, `pc_compat_X_Y`).
- the CPU the guest is shown, read off its vCPU: the vendor, the model, and the features it has
  on, by their canonical names. The features compare as one set. See
  [migration.md](migration.md).
- the machine: its **versioned** type (`machine.Type`, `pc-q35-11.1`), the accelerator,
  each `-machine` option, `-smp` and `-m`, as QEMU reads them back.
- every device the machine was started with. A device on the root complex is named by its slot
  (`device 02.0`), any other by its type (`device i8257/2` for the second one on its bus). Each
  property is a part, except its links and children, what is one VM's (`perVM`: the MAC, the
  vsock context id, vmgenid's GUID, backends, descriptors) and what changes while it runs
  (`runTime`: the balloon's statistics, the memory plugged, the clock). A disk's place and a
  hot-plugged device are not the machine: `Saved.Added` adds the hot-plugged ones again before
  the state loads.
- every RAM block by its maximum length, except fw_cfg's `/rom@` blocks, which QEMU builds and
  resizes on a load.

Where a part sits is its rule. An exact part is equal on both sides, and one only the machine
resumed into has is a difference. An at-least part may grow: virtio-scsi's `max_target`
(`mayHaveMore`), because the guest can still be given a disk at a target it never scanned. A
per-VM or run-time property missing from those lists makes a machine refuse its own checkpoint,
and `task boot:record` fails on it, as it does on a QEMU that changes what the record reads.

`Target` is read once per `QEMU` value: 7 round trips, since the version is the monitor's
greeting (8 of them read in 8.4 ms on a laptop at spin's shape, 2026-11-21). The record is 25 KB of JSON at spin's shape, with every list sorted, so two equal
records are equal bytes.

`Resume` does not read it. The caller hands `Resume` the record of the machine it resumes into,
and `Resume` refuses with `CanResume` before it asks QEMU anything. The caller is responsible for
that record describing the machine. The record is a constant of a host: its QEMU binary, its CPU
and the shape it resumes into. spin reads it once, when its runner starts, and holds each machine
of its pool to it once (spin-stack/spin#1416).

`machine.ReadTarget(ctx, spec, opts)` is that read: it starts a machine of `spec` held, reads its
`Target` and stops it. The caller hands it the Spec its machines are started with, so that the
record is read off the same devices and ceilings that `CanResume` will compare. What is one
machine's is changed, because the record leaves it out. The caller's monitors are dropped, since
their sockets belong to its own machines. Each NIC is put on a hub port, so no TAP and no
`/dev/vhost-net` is needed. The console writes nowhere, but the serial port is still there. A
machine of spin's shape whose NIC was on a real TAP with vhost read the same record, byte for
byte, as `ReadTarget`'s (laptop, v20261121.01, 2026-11-21). The vsock's context id is the Spec's
and has to be free. One call took 72-76 ms p50 over 20 at spin's shape: QEMU's start to its
monitor's answer 28 ms, the record 11 and the quit, until QEMU has exited, 37 (laptop,
2026-11-21). It runs at a runner's start, never on either goal number's clock. Read
inside `Resume`, it cost restore-first's first login +4.3 ms [+0.2, +7.5] with the state on the
disk and +8.2 ms [+6.2, +10.8] from the page cache (bench, lab 44868324811). No part of that read
could leave the clock: QEMU's main loop is busy from its exec to the end of the load, and QEMU
answers no monitor while it loads a state.

It leaves out the files a guest does not reload on a resume: the kernel, the initrd and the
option ROM that entered the kernel (`linuxboot_dma.bin`). A resumed guest runs the kernel in its
saved memory until it reboots. A newer QEMU can add a property to a device, which the record
then refuses as a part the checkpoint's machine did not have. `task boot:compat` holds a release
to what the record says: a checkpoint the newest release saved resumes on this tree's build, or
the run names every difference.

Moving `machine.Type` moves every guest to new hardware. It is a decision, never a side
effect of a QEMU bump. `spin-machine-dev verify qemu` says when a QEMU offers a newer type and
fails once the pinned one is deprecated, and `spin-machine-dev bump qemu` prints what the newer
types change against it.

`spin-machine restore` prints a refusal one difference a line, before QEMU reads the state.
`spin-machine target` prints a held machine's record as JSON, and `restore --target FILE` takes it
as given instead of asking the machine.

