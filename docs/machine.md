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
	Stdout: w, Stderr: w,       // QEMU's output; a file is QEMU's own, any other writer keeps a 4 KiB tail
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
  caller's: they are its network and its storage. Once QEMU has exited, Start removes each
  socket it bound that is still its own, the same inode made at the same time. A path bound again
  meanwhile, by the next machine's Start while this QEMU was reaped late, is that machine's and
  stays (#507).
- **Readiness is the monitor's answer, with nothing polled.** Start binds a monitor of its own
  and connects to it before QEMU exists: the connection waits in the socket's backlog and QEMU
  accepts it as it starts. `qmp_capabilities` is answered by QEMU's main loop, which runs once
  every device on the command line is made, so Start's return means the machine exists and is
  held. A QEMU that exits first, refusing a device or a CPU, fails Start at once with its own
  words (`Property 'virtio-balloon-pci.x' not found`, `Host doesn't support requested
  features`). One that never answers fails it after 30 s, or when ctx ends, and is killed.
- **`Exit` says how it ended.** The exit status or the signal, the last `SHUTDOWN` QEMU sent
  on Start's monitor, the guest's panic line when Start read the console, and QEMU's stderr's
  last 4 KiB. `Done` closes when the monitor ends, by QEMU's exit or by `Close`, and `Exited` only when
  the process has gone.
- **A file as `Stderr` is QEMU's descriptor 2.** QEMU writes to it directly, so it goes on
  writing there when its caller has gone, as an adopted QEMU does after spin's runner restarts,
  and `Exit.Stderr` is empty: the file holds it all. Any other writer is fed through a pipe the
  caller's process copies, which keeps `Exit.Stderr`, and a writer that stops reading stops QEMU
  at its next write.
- **`Stop` asks before it kills.** A running guest's power button, then up to 3 s for it to power
  off; QEMU's quit, then up to 1 s; then SIGKILL. A monitor that does not answer within 1 s is
  a QEMU loading a state, which answers no monitor until it is done, or a monitor closed: Stop
  kills it then. ctx bounds every step but the kill. A second Stop returns the first's Exit.

## A TPM

A machine asks for a TPM 2.0 with `Spec.TPM`, and the zero value is a machine without one.
shirudo keeps its measurement register in an NV index of it and makes RSA-3072 primaries under
it (#719), so a workspace that runs shirudo is a machine with one.

```go
swtpm, err := release.SWTPM()   // usr/share/spin-stack/bin/swtpm; a release from before #719 has none
spec.TPM = machine.TPM{SWTPM: swtpm, State: "/var/lib/…/workspace/tpm"}
m, err := machine.Start(ctx, spec, machine.StartOptions{
	TPMCmd: func(c *exec.Cmd) { c.SysProcAttr = … }, // swtpm's uid, root and cgroup, as Cmd is QEMU's
})
```

- **The state is the workspace's, like a disk.** `State` is a directory the caller makes, keeps
  and hands to every machine of the workspace, and it outlives each of them. The TPM's seeds are
  in it, so a primary made from the same template is the same key on every boot, and so is an NV
  index once written. An empty directory is a TPM made at its first start, with seeds of its own
  and no endorsement key. Provisioning one (an EK and its certificate, swtpm_setup's work or the
  TPM2 commands') is the caller's, once, when the workspace's TPM is made, and never on a boot's
  clock. swtpm locks the directory, so two machines cannot run one TPM.
- **swtpm runs beside QEMU, one for each machine.** Start runs it before QEMU, on one end of a
  socket pair, and hands QEMU the other end for its `tpm-emulator` backend. Nothing is bound at a
  path, so there is no socket to fit in 108 bytes, to find or to remove, and no wait for swtpm to
  listen. QEMU's exit closes its end and swtpm follows it out. `Exited` closes once both have
  gone, so the next machine of the workspace can take the directory, and a swtpm that failed puts
  what it said after QEMU's stderr in `Exit.Stderr`. swtpm is not in QEMU's root (`Spec.Files`):
  it is a process of its own, which `TPMCmd` shapes.
- **A checkpoint carries the TPM.** QEMU asks swtpm for its state blobs as it saves and hands them
  to the new swtpm as it loads, in the migration stream with the guest's memory. A machine resumed
  onto any state directory, an empty one included, has the TPM the guest saw at the save, and
  the directory holds it from then on. boot:tpm resumes onto an empty one and reads back the same
  primary, PCR 16 and NV index.
- **The record holds the device.** `tpm-crb` is a device the machine was started with, so a
  checkpoint never resumes across a TPM and none (`boot:record`, "a TPM here", "no TPM here").
- **It costs the goal's numbers, so it is asked for.** On the bench, spin's shape, a TPM cost the
  cold first login +36.1 [+23.3, +41.2] ms and the restored one +6.0 [+4.4, +7.4] ms against the
  same machine without one (labs 45748711725, 45748711747, 2026-11-26). tpm-crb was chosen over
  tpm-tis by the same runs (qemu/devices.mak). Where the cold 36 ms go is #745.
- **QEMU puts the initrd above the ACPI tables.** qboot writes the tables below 1 MiB (`ACPI: TPM2
  0xC1943`) and QEMU the initrd at the top of RAM, so a large one cannot cover them, as a 62 MB
  initrd did under SeaBIOS and hid the TPM (#719). boot:tpm boots one and finds the TPM.
- **Nothing measures the boot.** qboot with PVH loads the kernel and the initrd from fw_cfg, so no
  PCR says what booted and there is no event log (#742).


spin runs each QEMU as a user of its own, chrooted into a root it builds, in a cgroup it bounds.
What goes in the root and how far the bounds reach depend on the machine and its release, so the
machine says them. spin keeps building the root and applying the cgroup.

```go
files := spec.Files()           // QEMU, kernel, initrd, firmware/{BIOS, linuxboot_dma.bin, kvmvapic.bin}
o, err := spec.Overhead()       // Overhead{Memory, Tasks}: beyond the guest's RAM at its ceiling
n, err := disk.Overhead()       // each disk's qcow2 caches, beyond spec's
// memory.max = ceiling + o.Memory + Σ n, pids.max = o.Tasks
```

- **The root is `Files` and `/dev/null`.** Start hands QEMU every other file as a descriptor. QEMU
  opens `/dev/null` to probe for OFD locks and falls back to POSIX locks without it. It reads
  `/proc`, `/sys` and `/dev/urandom` only when they are there. A TCG boot opens the same files.
  `efi-virtio.rom` is left out, since every NIC has `romfile=`. SeaBIOS is left out unless `BIOS`
  names it.
- **`Overhead.Memory` is what the host cannot reclaim.** That is QEMU's anonymous memory beyond
  the guest's RAM, plus the kernel's memory charged to its cgroup, reclaimable slab excluded. The
  terms are QEMU's own memory (64 MiB), the kernel and the initrd (QEMU keeps a copy of each),
  1 MiB a vCPU, 64 KiB a thread, and the page tables of the ceiling (1/256 of it at 4 KiB pages,
  the host's and KVM's together). The images' pages in the host's cache are reclaimable and are
  not in it. A qcow2 image adds its L2 cache, the whole L2 table up to 32 MiB, plus four clusters
  of refcounts (`Disk.Overhead`, QEMU's defaults in `read_cache_sizes`). That is 8 MiB for a
  64 GiB image of 64 KiB clusters, and it counts every image of a chain.
- **`Overhead.Tasks` is the most the threads can be.** That is one thread a vCPU slot, QEMU's
  four (main loop, `call_rcu`, `mon_iothread`, KVM's `kvm-nx-lpage-recovery`), a vhost worker for
  the vsock and for each NIC, and a checkpoint's eight channels and their driver. On top of those
  come QEMU's thread pool, at most 64 (a thread it cannot start aborts QEMU), and io_uring's
  workers, at most the ring's 128 entries. The lane saw one pool worker and three io_uring ones.
- **Measured** (task boot:overhead, laptop KVM, v20261121.01, 2026-11-21). Spin's shape with a
  NIC and a vsock, started as nobody in a root of `Files` alone, held 31 MiB beyond the guest and
  9 tasks, booted and sized to 2048 MiB. At its ceiling it held 73 MiB and 27 tasks, with 16
  vCPUs, 8192 MiB filled and four disks attached beside its two (one of them 64 GiB), while the
  guest read, wrote and trimmed every disk. Of the 73 MiB, 27 were page tables, 16 the bzImage
  and 8 the 64 GiB disk's L2 cache. A checkpoint there held up to 79 MiB and 34 tasks. `Overhead`
  for that machine with its disks is 153 MiB and 223 tasks. spin reserved 768 MiB and 272 tasks
  (spin's `internal/runner/qemu/bounds.go`).
- **What the lane counts.** QEMU's own memory is the anonymous memory of its mappings outside the
  guest's RAM blocks, from its `smaps`. The cgroup's `anon` is not used for it. A huge page of the
  guest's that QEMU has given back in part stays charged until the kernel splits it, which it does
  once the cgroup needs the memory. Just after the guest freed 1.4 GiB, `anon` read 160 MiB more
  than `smaps`.
- **Not measured.** A restore, which loads with the same channels as a checkpoint saves with, and
  a QEMU that has run for days. The page tables the lane saw were those of 4 KiB pages (27-32 MiB
  for 8 GiB) with huge pages on madvise, so a host with them off pays what is counted.

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
err = m.SetDiskLimit(ctx, "home", machine.DiskLimit{WriteBytes: 50 << 20}) // DiskLimit{} clears it
l, err := m.DiskLimit(ctx, "home") // DiskLimit{ReadBytes, WriteBytes, Reads, Writes} a second, 0 none
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
l, err := machine.ListenPressure()  // once per host, as root: vsock port machine.PressurePort, 1032
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
| `SlowCommand{Command, Took, Baseline}` | a QMP command on Start's monitor took at least 100 ms and 4 times what it took before. It is QEMU stalled, not the command: each one longer than 50 ms in the pressure probe's lane had QEMU's main thread blocked in the host (reclaim at `memory.high`, a swap-in, `mmap` behind the vCPU's faults) or its monitor thread throttled by `cpu.max` (80 ms). query-status met the same stalls at `memory.high` and `cpu.max`. Two busy loops on QEMU's core cost a call 4-6 ms at most (laptop, 2026-11-21, #402) | the host, in the monitor's one client |
| `GuestLost{Err}` | the guest's sender's connection ended, and the guest is blind until it dials again | the host |
| `Limited{Limit, Device, Set, Used, Events, Since}` | in the last second, a limit spin set held the machine: on QEMU's own cgroup `cpu.max`, `memory.high`, `memory.max` (and its OOM kill), or one key of `io.max` on one device; inside QEMU one field of a disk's `DiskLimit`, `Device` its serial | the host, from QEMU's cgroup each second, and from `query-blockstats` while Start's monitor holds a disk |

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
`memory.stat` (dirty and writeback pages), `io.stat`, each disk's counters (`DiskUsage`), and the
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

- **A `DiskLimit`.** The disk's `query-blockstats` counter ran at 90% of the field's limit or more
  over the second, as for `io.max`: QEMU counts nothing of what it held either. The cgroup never
  sees this limit, so the watch reads it from QEMU. It asks only while a disk is held through the
  monitor Start connected, one `DiskUsage` a second, so a machine with no limit pays no
  command. It costs QEMU's main loop ~0.1 ms, each disk's own counters (#402). A limit set through a second monitor is not reported. Held at 4 MiB/s, the guest's
  direct reads brought a `Limited` each second with `Used` at 4.19-4.25 MB/s, and guest IO stalls
  with it (task boot:pressure, laptop, 2026-11-21).

The cgroup limits are QEMU's own cgroup's. One a parent cgroup sets is counted in the parent's
files (#412). spin keeps the network
limit and its signal (spin-stack/spin#1480). `Usage` carries the counters behind each: `CPU.Throttled`
and `CPU.Throttles` from `cpu.stat`, and `Memory.High`, `Max`, `OOMs` and `OOMKills` from
`memory.events`. The guest is never told. It does not know its limits exist.

**What each vantage cannot see.**
- **The guest's PSI is the guest's word.** A workspace's root can stop the sender or write what
  it likes to port 1032. The host's kernel names the CID of every connection, so a guest can lie
  only about itself. A host Stall and `Usage` are the host's own measurement.
- **What a guest sends is bounded in size, count and time** (#559). Size: a line is at most 128
  bytes. Count: one connection per CID, and a new one replaces the old. Time: each watch takes at
  most 4 connections at once and 1 a second after them, and closes one over that at once, keeping
  the connection it holds. It reads at most 64 lines at once and 16 a second after them, and a
  guest over that is cut off with a `GuestLost` whose `Err` is `ErrGuestTooFast`. The sender sends
  ~3 lines a second outside an OOM storm, and its next hello and Stall carry what it could not send.
  Ends that come while a `GuestLost` waits its second are that one `GuestLost`, so a guest dialling
  and dropping holds one goroutine of the host. A count or `Total` below the last one heard is
  ignored, so the `OOMKill`s a watch reports add up to at most the guest's largest count, and a
  Stall's `Total` never goes backwards for its resource. A guest's microseconds past what a
  `time.Duration` holds saturate.
- **A fast OOM can kill before any trigger fires.** Two 1000 MiB hogs did (2026-11-21). So the
  sender watches the guest's OOM kills as well, through `memory.events` of each top-level cgroup,
  which the kernel notifies on every kill. The kill count travels as a running total. A count sent
  again, or a kill made while the connection was down, is counted once, in the hello of the next
  connection.
- **A machine loading or saving a state answers no command until it is done.** The watch follows
  the migration's events and times nothing while one runs, so a restore does not alarm. A command
  asked for the first time has no baseline to be slower than, so it is never slow.
- **A guest's first ~0.4 s are not reported**, since its sender starts with the boot's rest, after the first login's units.
- **vsock's own round trip is not measured** (#393). Under pressure, a heartbeat from a sender at
  the guest's lowest priority would measure the guest's scheduler.

**Where each part lives.** The sender is `cmd/spin-machine-pressure`, built static by the image's
build (`image/build.sh` checks it is in the image as built). Its unit is
`spin-machine-pressure.service`, at Nice 19, on the idle scheduler and IO class, and out of the OOM
killer's way. It is wanted by `multi-user.target`, which the boot's rest starts after the first
login's units (#581), so it starts ~0.4 s into the boot without costing the login (#401). A
restored guest has it running already. It wakes only when the kernel says something changed. It holds no identity: the host
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

**A machine Start did not start.** A runner that restarts finds the machines its predecessor
started still running, and reconnects their monitors with `Attach`. `PressureWatch.Adopt` puts a
watch on one of them, as `StartOptions.Pressure` puts one on a machine `Start` starts:

```go
a, err := l.Watch(notify).Adopt(cid, pid) // the guest's vsock CID, QEMU's pid
defer a.Close()                           // when QEMU exits, or the runner lets the machine go
```

This is the contract spin's `qemu.Attach` relies on (spin-stack/spin#1684, #679):

- **What it hears.** Adopt registers the CID with the listener, reads QEMU's cgroup from
  `/proc/PID/cgroup` and arms the same triggers on it. The watch then hears the guest's Stalls and
  OOMKills, the host's Stalls, `GuestLost`, and each cgroup limit's `Limited`. It hears no
  `SlowCommand` and no `DiskLimit`'s `Limited`, since both are read through the monitor `Start`
  connected, and an adopted machine has none (#679).
- **What it refuses.** What `Start` refuses: a CID of 0, a QEMU in its caller's own cgroup, and
  triggers the caller cannot arm, as when it is not root. It also refuses a watch that is another
  machine's and a CID another watch holds. A refused Adopt holds nothing.
- **The guest.** Its sender lost the old listener with the runner that held it, and dials again
  by itself, 1 s after that and backing off to 30 s while nobody listens. The first count of OOM
  kills the watch hears is the base: the kills before it were the watch before's to report.
- **Close is the caller's.** QEMU is not the caller's child after a restart, so nothing here waits
  on it. The caller closes the watch when its pidfd says QEMU has exited or when it detaches. Close
  frees the CID, ends the guest's connection with no `GuestLost` and closes the triggers. A second
  Close does nothing. The pid is read once, by Adopt, and a pid freed before then may name another
  process. A caller holding QEMU's pidfd asks it after Adopt whether QEMU still runs, and closes the
  watch if not.

**What spin does to adopt it.** It makes one `ListenPressure` at the runner's start and passes
`Watch` to each `Start`, with each QEMU in its cgroup through `Cmd`, as it already does, and to
`Adopt` for each machine it takes back after a restart. Its list of vsock ports names the
listener's as `machine.PressurePort`. Its pressure
handler then takes `Stall{Seen: Guest, Resource: MemoryPressure}` where its runner took the guest
supervisor's `POST /v2/memory-pressure` on port 1027. The supervisor's notice, and the runner's
handler for it, are then deleted: the trigger they arm is the same, 150 ms in 1 s.

**Next, not here.** Not yet explained:
the 175-950 ms QMP stalls under small synced writes on a contended host disk (#391), and QMP's
round trip during a load (#392).

## Actuators

The probe says which machine suffers and `Usage` says which causes it. An actuator is what holds
the cause back while it runs, without a restart and without the guest being told. The machine
offers the actuators that act inside QEMU. spin keeps the policy: which machine, how much, for how
long, and when to let go.

| Signal | Actuator | Owner | Cost |
|---|---|---|---|
| Host IO stalls on the runner, and one machine's `Usage.Disks` counters far ahead of the rest | `SetDiskLimit(serial, DiskLimit)` on that machine's busy disk; `DiskLimit{}` lets it go | machine's method, spin's policy | 1.8-5.5 ms a call, 2026-11-21. Nothing at boot, since the command line does not change |
| `Limited{Limit: DiskReadBytes…}` | the limit holding: spin keeps it, raises it or lets it go | spin | none: read only while a disk is held |
| `Limited{Limit: IOReadBytes…}` | `io.max` on QEMU's cgroup | spin, which owns the cgroup | |
| A guest `Stall` on CPU and no host one: the machine is too small for its load | `SetCPUs(n)` up to its ceiling | machine's method, spin's policy | One vCPU added: the call 2.3 ms, `/sys/devices/system/cpu/online` showing it 13.2 ms p50 [7.6, 16.2]. One removed: 14.8 [8.5, 23.7], the call returning only once the guest has let the vCPU go. Bench, spin's shape, 10 each, lab 45117940800, 2026-11-23, `boot:actuators`. 16 vCPUs, 8192 MiB and four disks plugged at once reached the guest in 0.31-0.71 s after a checkpoint (laptop, 2026-11-21, `boot:cpu-plug`) |
| A host `Stall` on CPU on other machines, and one machine's `Usage.CPU` far ahead of the rest | `cpu.max` or `cpu.weight` on the noisy machine's cgroup. More vCPUs for the others would not help: the host is short, not they | spin, which owns the cgroup | a write to the cgroup, and a `Limited` each second it holds (laptop, 2026-11-21) |
| `Limited{Limit: CPUMax}` | the limit holding: spin keeps it, raises it or lets it go | spin | none: read each second |
| A guest `Stall` on memory, or an `OOMKill` | `SetMemory(mb)` up to its ceiling, through virtio-mem | machine's method, spin's policy | plugging 2048 MiB took 60-63 ms (laptop, prototype, 2026-11-21) |
| A host `Stall` on memory: the host wants memory back | `SetMemory` down, which takes back only the blocks the guest can empty; the guest's `memory.reclaim`, whose freed pages free page reporting hands back; `memory.high` on QEMU's cgroup | `SetMemory` machine's method, the rest spin's (its `spin-reclaim.service` in the guest, its cgroup) | 2048 → 1024 MiB: the call 1.7 ms, the guest's MemTotal 1024 MiB less in 21.2 ms p50 [19.8, 21.9] idle, and 53.7 [51.3, 55.0] with 1180 MiB of page cache to drop. Bench, spin's shape, 10 each, lab 45117940800, 2026-11-23, `boot:actuators`. Back up to 2048: 9.1 [7.7, 11.9]. Counted by the 128 MiB memory blocks online, the same unplug stood at 1536 MiB for minutes (lab 45113957566): virtio-mem takes back its own smaller blocks from inside them, and a memory block stays online until all of it is gone. One `memory.reclaim` of 512M took QEMU's RSS from 910 to 394 MB, with the working set read again in 150 ms against 120 (run 36809694419, docs/memory-and-disk.md) |
| `Limited{Limit: MemoryHigh…}` | the limit holding: spin keeps it, raises it or lets it go | spin | none: read each second |
| A machine's network crowding the others' | `tc` on the TAP spin passes in. The machine opens the TAP spin names and has no NIC of its own to shape | spin | |

**The balloon is not a memory actuator here.** An inflated balloon and virtio-mem are incompatible in
one VM (docs/memory-and-disk.md), and a machine with a ceiling above its boot memory has virtio-mem.
The balloon is there for free page reporting and `deflate-on-oom`, and spin reads the guest's memory
statistics from it, never inflating it. Memory leaves a guest by `SetMemory` down, or by the guest
freeing it within what it keeps.

**A `DiskLimit` is the device's, not the place's.** `SetDiskLimit` sends `block_set_io_throttle` to
the backend of the disk's device, a place's virtio-blk or a disk attached on the hotplug
controller. Every place's node does sit under a throttle filter, but all of them are in one group,
`places` (`machine/spec.go`, `placeGroup`). A group shares its limit among its members, so a limit
set there would hold every disk of the machine together. The filter is there because
`change-backing-file` needs a filter between the device and the image, not to limit anything. The
backend's group is its device's alone, and a bind never replaces the backend. A per-place group
would have moved the command line every machine boots with, for a limit few machines are ever given.

**`io.max` and a `DiskLimit` hold different things.** `io.max` holds a block device of the host,
so what the host serves from its page cache passes it: the probe's lane drops the disk from the
host's cache before its `io.max` phase for that reason. A `DiskLimit` holds what the guest asks of
one of its disks, wherever the host finds the data, and one disk apart from the others on the same
host device. On a guest of spin's shape, 8 MiB/s held 32 MiB of direct reads to 3.78 s and
50 writes a second held 150 writes to 2.88 s. The other disk read the same 32 MiB in 2-5 ms beside
them, and the held one read it in 2-6 ms again once cleared (task boot:disk-limit, laptop,
2026-11-21, 3 runs). QEMU lets a tenth of a second's worth through at once, so 32 MiB took a little
under the 4 s its rate alone would.

**A checkpoint carries no limit.** The limit is the running QEMU's, as `io.max` is its cgroup's: the
guest's state does not hold it, and a resume starts a new QEMU. A machine resumed from a checkpoint
taken under a limit reads `DiskLimit{}` and runs free until spin sets the limit again, which holds
at once (task boot:disk-limit). Carrying it would have the machine keep policy that is spin's: a
limit is set for a neighbour, and the resumed machine may have other neighbours, or none.

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
| `GuestRunning` | `cont` returned, from `Continue`, or QEMU's `RESUME` for the loaded guest, inside `Resume` | |
| `GuestHeld` | `Hold` took the disks back, the guest still stopped | |
| `LoadStarted` | QEMU took `migrate-incoming` | |
| `StateLoaded` | the whole state read: in `Resume`, or in `Loaded` after a fast load | `Bytes`, the data the state's file holds |

The phases are per call, not per machine. spin keeps one timeline per snapshot (spin-stack/spin#1481)
and adds its own steps to it, such as the guest's flush, the upload and the publish. A func per
call needs no snapshot id to sort a machine's stream by. `Continue`, `Hold` and `Loaded` tell the
last phase of the call before them, because a checkpoint ends with the guest stopped and the caller
decides when it runs.

**They cost the goal paths nothing.** A resume's phases ask QEMU nothing more, and cost one `fstat` of the state. A checkpoint's
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

**A checkpoint does not sync the disks either.** QEMU drains them at the stop, so every flush the
guest asked for is on the disk. From then until the guest runs again it defers its own flushes to
the host's disk (`x-defer-disk-flush`, which `qemu/patches` adds): the stop's, a seal's
`blockdev-snapshot` and `cont`'s activation reach the page cache, and the guest's next flush forces
them. A caller that keeps the checkpoint's disk, such as the layer a seal put under a new overlay,
syncs it with the state after `Continue`. On a bench whose disk four writers kept busy, the stop's
fsync had held the guest 40 ms p50, and 18 without it (docs/memory-and-disk.md, "A busy host disk",
#426). A QEMU without the patch, which a machine started by v20261121.01 or an earlier release
runs, does not list the capability in `query-migrate-capabilities`, and `Checkpoint` does not ask
it for one: that machine's stop flushes to the host's disk, as every checkpoint did before.

The 0.3-0.9 GiB the guest stopped with was QEMU's default `downtime-limit` of 300 ms at work: QEMU
stops the guest once what is left fits in that limit at the rate it measures, ~4 GB/s into the page
cache. Checkpoint now sets 30 ms (`checkpointDowntime`). On the bench, QEMU's downtime for the same
shape fell from 192 ms to 6 ms, and the guest's stop to `cont` fell from 199 ms to 14 ms
(docs/memory-and-disk.md, "How long the guest is stopped", #415).

## What a snapshot keeps

`task boot:rollback` holds a snapshot to what a person in the workspace expects (#408). It boots
spin's shape to systemd, logs in over SSH through vsock, and runs the terminal loop `while true; do
date +%T.%N; sleep 0.1; done` beside a process that holds 4 MiB of random bytes and a counter. Each
step is held to what the guest had at the checkpoint, and each pause's cost is logged from the
guest's own output. On v20261121.01 (laptop, and bench lab 44937567816, 2026-11-21):

- **A checkpoint with memory, continued in place.** The guest stopped 11-17 ms (`Cost.Paused`
  11-14). The loop's largest step stayed at 101-105 ms, because the pause fits inside one of its
  sleeps. An SSH session open across the checkpoint kept answering.
- **A rollback with memory onto the sealed disk.** The process is the same PID with the same
  bytes. A file reads as it was at the checkpoint from memory, and again from the disk after the
  page cache is dropped. The loop and the counter go on from their lines at the checkpoint, 101 ms
  later on the guest's clock. The kernel logs `crng reseeded due to virtual machine fork`
  (vmgenid).
- **A disk-only rollback**, a cold boot from the sealed disk. What was synced before the seal is
  there and what came after is not. The seal is crash-consistent: the loop's log, written for 3 s
  and never synced, is not on the disk at all.
- **A disk-only snapshot**, `stop` and `cont` around `blockdev-snapshot`. The guest stopped 7-14
  ms, 6-8 of them the seal.

**The guest's clocks stop with it.** QEMU saves kvm-clock at `stop` and sets it back at `cont`, so
`CLOCK_REALTIME` and `CLOCK_MONOTONIC` both stand still for a pause. A stop held 1.00 s put the
guest's wall clock 1.001 s further behind the host's, while the process's lines stayed 101 ms
apart on both clocks and the host saw a 1.10 s gap between them on the console. A resumed guest
starts on its checkpoint's clock: 48 ms after a resume it read 6.34 s behind, the time since the
checkpoint's stop, and 5 s later it read the same. Nothing in the machine steps it, by the decision
that took chrony out (docs/log/2026-09-10-6aba37b-064c4f4-c7bab94-c7af4d3-08c56e1.md): whoever restores corrects the clock. spin's supervisor
steps it when it registers again after a restore. Nothing steps it after a snapshot continued in
place (spin-stack/spin#1498).

**An SSH session open at the checkpoint does not survive a rollback.** QEMU's vhost-vsock resets
the transport after a load, and the restored sshd reads `Transport endpoint is not connected`
within 3 ms of the resume. The session's shell, on a pty, was gone at the first look, 57-100 ms
after it. The guest's kernel logs nothing of the reset. A new login works at once. Keeping the
work across a rollback is spin's (spin-stack/spin#1483).

## The kernel command line

`Spec.Cmdline` is a `machine.Cmdline`, a typed value that `String` renders with what every boot of
this machine needs (`pci=lastbus=0`, `TERM=dumb` and the rest, each with its reason in
`machine/cmdline.go`). A consumer sets the console, the root, init and `Extra`, and gets the rest.

- **Lockdown.** `Cmdline.Lockdown` is one choice of the lockdown level and the `.bpf` and
  `.dm-verity` keyrings together, spin-machine's `--lockdown`. The kernel forces integrity, the
  floor no command line goes below, and seals each keyring empty at boot unless told not to.
  `LockdownConfidentiality`, the zero value, puts `lockdown=confidentiality` on the line and leaves
  the keyrings sealed, so a consumer that builds its own `Cmdline` gets it without asking.
  `LockdownShirudo` is a machine that runs shirudo: integrity, for its programs read kernel memory
  (#734), with `bpf.keyring_unsealed=1` and `dm_verity.keyring_unsealed=1`, for its init to enroll
  its certificates in both keyrings and seal them (#741). Either half alone is a machine shirudo
  cannot run on, and unsealed keyrings at confidentiality are open to root with nothing to seal
  them, so neither can be asked for. `LockdownKernel` adds nothing, for the lab's traceable kernel,
  which forces none. What each trades is in kernel/README.md, "Lockdown and the BPF LSM". `Args`
  refuses a `lockdown=` or either keyring parameter in `Extra`: the kernel keeps the higher of two
  levels and the last of two parameters, so one there would make the guest half of one choice.
- **Not part of the record.** The guest reads its command line once, at boot. A checkpoint resumes
  the guest it saved, at the level and with the keyrings that guest booted with, whatever the line
  of the machine it resumes into says, so the line cannot make a resume load wrongly and `Target`
  does not carry it.
  Which level a resumed workspace runs is the guest's, and a consumer that changes a workspace's
  choice gets it at that workspace's next cold boot.

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
- the vCPU's properties that shape CPUID without being a feature bit (`cpuidProperties`), each
  a part `cpu <name>`: the cache switches (`x-consistent-cache`, `x-legacy-cpuid2-cache`,
  `l3-cache`, `legacy-cache`, `host-cache-info`), and what the host model takes from the host (`family`, `model`,
  `stepping`, `model-id`, `level`, `xlevel`, `phys-bits`, `guest-phys-bits`, `pmu`). A switch
  QEMU leaves at the machine type's default is not held until `-cpu` sets it, and a test fails
  a `-cpu` option that is neither a feature nor held. A record from before they were held
  (#569) has none, and is refused by each, `cpu x-consistent-cache: checkpoint none, here false
  (same)` among them: nothing in it says which side of `x-consistent-cache=off` it was taken on,
  since v20261122.07 (switch on) and v20261123.01 (off) run the same QEMU. Under `host`,
  `x-consistent-cache=off` alone gave leaf 2 a 2 MiB 8-way L2 and leaf 4 a 4 MiB 16-way one, and
  `x-legacy-cpuid2-cache=off` (qemu/patches/0006) makes leaf 2 describe leaf 4's (#586). No
  release's QEMU has that switch, so `task boot:compat` refuses every release's checkpoints by
  it, v20261123.03 first, `cpu x-legacy-cpuid2-cache: checkpoint none, here false (same)` (CI
  run 45104443218, 2026-11-23).
- the machine: its **versioned** type (`machine.Type`, `pc-q35-11.2`), the accelerator,
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

### What a failed resume says

A `*machine.Refusal` in `Resume`'s error, matched with `errors.As`, is the one answer that says
the checkpoint will not resume into a machine like this one, so a retry on this host fails the
same way. spin answers it by trying the next host and, once every host has refused, booting the
workspace from its disk. `Resume` refuses four things this way, each before QEMU reads a byte:

- what `CanResume` refuses;
- a state file whose length is not the `Saved.Length` its checkpoint recorded (part
  `state length`): a state cut short or partly copied. A checkpoint saved before the length was
  recorded has none, and resumes without the check;
- a format this QEMU does not read (part `format`), which only a `here` that claims more formats
  than QEMU reads lets past `CanResume`;
- a `Saved.Added` that does not parse (part `added`).

Every other error is a plain wrapped error, and it may be this host or this try: a monitor
command, the image lock `blockdev-set-active` meets, QEMU exiting, the caller's context ending,
and `LoadOptions` asking for more than one channel on a stream checkpoint, which loads with one.

A load that QEMU itself reports `failed` is one of those plain errors, because nothing QEMU says
tells a state no QEMU can load from a host that failed to read it (QEMU 11.2.0-rc0, read
2026-11-22). The `MIGRATION` event carries the status alone. Every failure of the load ends at the
same label in `process_incoming_migration_co`, which puts the reason on stderr and exits. That
covers a section QEMU rejects, a state file that ends early, the host's read failing (both of
which `qemu_fill_buffer` reports as `-EIO`), and userfaultfd refused to a fast load. So the state's
length is checked before QEMU is asked to load it, and `task boot:record` holds a real truncated
state to a `Refusal` with the state never read (`events_test.go`, #546). A state of the right length
whose bytes changed in place still fails as a plain error. spin keeps the memory and retries it,
which is safe, and such a state stays suspended instead of booting its disk.

