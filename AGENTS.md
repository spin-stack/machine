# Working in this repository

## What this is

A virtual machine: QEMU, a guest kernel, a base image, and the definition of the machine
they make. Four artefacts and one version.

Anything that gets a workspace to its first login sooner may live here: an init, a guest
agent, a different kernel, machine or login path. Name what it is and who uses it.

## The goal

**A cold boot to the first SSH login in under 100 ms.** Measured by `task boot:ssh-first` on the
bench fleet: from the moment before QEMU is exec'd to `true` having run as the workspace user
over SSH through vsock.

The second number is a checkpoint restored to the same first login (`task boot:restore-first`).
A change goes in when it moves one of the two, interleaved against the release, without making
the other worse - or when it is functional (security, correctness). Every other number - the
console prompt, a phase, an initcall, a unit's time - is a tool for finding why one of the two
moved, never a target of its own. `docs/log.md` shows what optimising the others cost.

Where both stand, what each phase of them costs, and how a number is taken and read is
`docs/lab.md`. It also says why the cold number cannot reach 100 ms by userspace work alone:
QEMU, the firmware and the kernel spent ~125-160 ms before PID 1 ran (2026-11-20, depending on
the shape).

**Every part of the machine may be changed, without limit.** The kernel, systemd, QEMU and qboot
are all ours to patch when a patch serves the goal. A patch is carried as the tree carries them
today: the kernel's in `kernel/patches`, QEMU's in `qemu/patches`, qboot's beside its README in
`qemu/qboot`, and systemd's in `image/systemd`, whose Dockerfile rebuilds Ubuntu's source
package (an experiment's patch goes in `image/systemd/experiments`).

## What must not break

1. **The base image is never written to.** Every VM maps it read-only through a qcow2
   backing chain and many share one file. Anything that opens it for writing invalidates
   every overlay in existence - silently, because the overlays keep working until they read
   a cluster that moved. New images are written beside the old one and renamed over it.

2. **No identity in the base image.** No hostname, no machine-id with a value, no
   `/etc/resolv.conf`, no SSH host keys, no random seed: anything baked in is shared by every
   VM that ever boots from that image. `image/build.sh` asserts each of these on the finished
   filesystem.

3. **CPUs, memory and devices stay dynamic.** CPU hotplug up to the machine's ceiling, memory
   through virtio-mem and the balloon's free page reporting, and disks attached and detached
   while the machine runs: a workspace grows, shrinks and gives memory back without a restart.
   Nothing trades any of it for boot time - not a lower ceiling, not a missing device, not a
   machine without ACPI. Make what they cost cheaper instead.

4. **systemd runs the guest.** Much of what runs in a workspace expects it: units, logind
   sessions, user managers. An init without systemd is never a candidate. Measured, it is only a
   bound on what systemd costs, and the cost is made cheaper inside systemd.

**The fingerprint says which checkpoints a machine resumes, nothing more.**
`machine.Spec.Fingerprint` hashes what a resumed guest sees (`Spec.Contract`, the machine type,
the shape, the BIOS's size, the host CPU under `-cpu host`); a resume across two fingerprints is
undefined, so they are refused. Changing the machine is allowed whenever it serves the goal: the
fingerprint moves, the release notes say so, and checkpoints from before it boot cold instead.
`task boot:compat` and `task boot:roms` check that the fingerprint tells the truth.

## Simplicity

Prefer the boring solution. The measure of a change here is not what it makes possible but
what it makes clear.

- **Build what is needed now.** Not what will probably be needed. A flag with one caller
  should be that caller's constant; an interface with one implementation should be the
  implementation; a configuration option nobody sets is a branch nobody tests. When a
  second case actually arrives, generalise then — the second case tells you what the
  abstraction should be, and guessing beforehand almost always produces the wrong one.
- **Deleting is a change like any other.** If something no longer earns its place, remove
  it rather than leaving it for a future that may not come. Commented-out code is deleted
  code with extra steps; git remembers.
- **A little duplication is cheaper than the wrong abstraction.** Two similar things that
  drift apart for good reasons are healthier than one thing with a mode flag.
- **Put the thing where the thing belongs.** Static configuration goes in the image, next
  to the other configuration. A decision about one boot goes in whatever made that boot. A
  systemd unit written by a Go program at run time was the wrong answer to a real problem;
  the unit belongs in `image/`, and only the symlink that enables it belongs in the boot.

### Experiments

Explore freely; promote on evidence. `docs/lab.md`, "How a number is measured", is the method in
full; these are its rules.

- **Write the budget entry first.** Name the phase an experiment attacks in `docs/lab.md`'s
  budget, its p50 today, its ceiling (what the goal number gains if the phase cost nothing), and
  the result that would decide it. An experiment whose ceiling is under the noise needs more
  boots before it needs a run.
- **Measure spin's shape**: the CPUs, memory, ceilings and disks spin boots a workspace with, and
  name the shape on every table. A gain in another shape is a lead.
- **Decide on the end-to-end numbers in "The goal", interleaved in one run** against the
  release, on the kind of host the goal is measured on, one run at a time: two runs side by side
  measure each other, and two runs taken apart are two machines. A laptop finds leads.
- **One acceptance rule.** A change is a gain only when the bootstrap 95% interval of its Δp50
  excludes 0. At 20 boots a side the interval is about ±7-9 ms (`docs/lab.md`); a smaller effect
  needs more boots, not a second look.
- **One change per arm.** A comparison that decides a change carries that change alone. A floor
  or a bound may change as many things at once as its question needs: a floor built from every
  change together says what is possible before any one of them is tuned.
- **An instrument must see the phase it claims.** The kernel's "Freeing" clock starts after the
  bzImage is decompressed, and it recorded the bzImage's cost as zero. Check that something on
  the other side of a phase moved by the same amount before trusting the phase.
- **Measure the bounds each release**, the no-systemd floor first: a bound that is not
  re-measured stops being one.
- Keep experiment outputs, overlays, initrds, firmware and caches apart from release outputs,
  and never open the shared base image for writing.
- Remove an experiment once its question is answered. When a change decides something, kept or
  rejected, add its row to `docs/lab.md`'s tables and its entry, dated and with its run, to
  `docs/log.md`.
- **Read before starting.** `docs/lab.md`'s "Measured and rejected" table and `docs/log.md`'s
  rework cycles: an idea already there needs a new reason, not a new run. What the guest's memory
  and disk cost is `docs/memory-and-disk.md`.

## Go

- **Clarity beats cleverness.** This code is read far more often than it is written, and
  usually by someone trying to find out why a VM did not start.
- **Handle an error once.** Wrap it with what was being attempted — `fmt.Errorf("mounting
  %s at %s: %w", …)` — and return it. Do not log it and return it. An error message should
  say what failed, on what, so that the person reading a serial console at 2am does not
  have to guess. A bare `no such file or directory`, which this repository shipped once,
  names nothing.
- **Do not panic**, and do not exit from a library. `cmd/` decides what a failure means.
- **Accept interfaces, return structs**, and only introduce an interface when there is a
  second implementation or a test that genuinely needs one.
- **Make the zero value useful** where it is cheap to do so, and validate where it is not.
  `Spec.validate`, which `Args` runs, exists because a disk with no format is a guest that boots and finds a
  disk full of nothing — that is worth a check at the boundary, not a sensible default.
- **Name things for what they are** to the reader, not for their type. `Shape`, `Spec`,
  `Fingerprint` are the words used when talking about this machine; use those.

## Comments

Comments here are longer than usual, on purpose. The rule is what makes them worth reading:

- **Say why, not what.** The code says what. If a comment restates it, delete the comment.
- **Carry durable evidence.** When a production decision rests on a measurement, put a
  concise result and its context near the decision, or link to a maintained measurement
  record. Include a number when it is the reason for the decision; do not turn an exploratory
  observation into a permanent claim.
- **Record failed approaches selectively.** Keep a failed attempt near code only when it
  prevents a plausible, costly regression or repeats a non-obvious tool failure. Put raw
  samples and short-lived exploration in a measurement record, issue, or commit instead.
- **Date a fact that could go stale.** "measured 2026-09-07" tells a reader whether to
  re-check.
- **Do not write down that it works.** The README had a "Status" section listing what had
  been built and verified on a date. Every line of it was something CI answers
  continuously and can answer *red*, or something already said next to the decision it
  justified — so it could only rot, and it did. A claim about the state of the tree
  belongs in a workflow, a badge, or a commit message. Never in prose that nobody
  re-reads.
- **Name external contracts when relevant**: a consumer's protocol, provisioned file or lifecycle
  that this machine has to honour, described here and tested where it can be.

## Artefacts leave this machine

Host-executed binaries published in a release should be statically linked unless there is a
documented deployment reason not to be. This rule does not apply to guest userspace, build
tools, or diagnostic artifacts. A released host binary that depends on host libraries needs a
compatibility and packaging story, because its failure otherwise arrives on someone else's
machine at start-up.

## Verifying

- **Assert what came out, not what went in.** A build that produced an empty filesystem
  produces a perfectly valid empty filesystem. `image/build.sh` opens the finished image
  and looks for `/sbin/init`; the QEMU build asks the binary it just produced which devices
  it has; `spin-machine-dev verify kernel` reads the ELF notes out of the kernel it just stripped.
- **Check the tool's output, not only its exit status.** `debugfs -R` exits 0 whether or
  not the file it was asked about exists, which made every filesystem check in this
  repository vacuously pass until someone looked.
- **Fail where the cause is.** A missing option ROM should fail in the build that did not
  ship it, not in a VM three weeks later that will not start. Every workflow ends in the
  verification that belongs to it.
- **A tarball with something missing is worse than no tarball**: it installs, and the
  missing piece surfaces somewhere else. `spin-machine-dev release` refuses rather than warning.
  A list of files to ship is how one goes missing: the release script named qboot's patches one
  by one, and v20261001.01 shipped a `qboot.bin` without the patch it was built with. Ship
  what a directory holds.
- **Ask the kernel for a feature, never for its module.** Spin OS's kernels have no loadable
  modules: what they have is built in, `modprobe` finds nothing, and a built-in driver without
  parameters has no `/sys/module` entry. Look for what the feature makes - `/dev/nbd0`,
  `/dev/vhost-vsock` - and only then load a module. Asking the module said no to features
  that were there, twice (#56, #82).

## Trying the machine

`spin-machine` (`cmd/spin-machine`, built by `spin-machine-dev build spin-machine`) is how this
machine is run by hand. Use whatever is quickest to explore with; a check that will be repeated, or that decides
something, ends up in `boot/` as a Task target, so the next person measures the same thing.

- **Rebuild it first.** `_output/bin/spin-machine` is whatever `spin-machine-dev build` last built, so
  a change to `machine/` is invisible until the tool is rebuilt. `spin-machine args` shows
  the command line that will actually run.
- **The base is safe by default.** Without `--disk`, `boot` puts the base image under a
  throwaway overlay (QEMU's `-snapshot`), and root is `/dev/vda`. `--disk` opens the named
  file as given, so point it at an overlay and never at `rootfs.qcow2`.
- **Driving a guest.** `--init /bin/sh` gives a shell with nothing mounted; its console is
  on stdin/stdout, or on a socket with `--console unix:PATH,server=on,wait=off`. `--qmp
  PATH` opens the socket that `attach`, `detach`, `save` and the others talk to.
- **Every machine starts held.** QEMU runs `-S` (or `-incoming defer` with `--restore`) until a
  monitor binds its disk and lets it go: `boot` starts itself beside QEMU to do that, and
  `boot --held` leaves it for `start`, as a machine started ahead of its workspace is.
- **A socket's path fits in 108 bytes.** `sun_path` is that long, and a QMP or console socket
  under `t.TempDir()`, a scratch directory or `_output/` passes it easily. QEMU then does not
  start, and what the probe reports is the guest never answering, not the path. Put sockets in a
  short directory of their own (`os.MkdirTemp("/tmp", …)`) and keep overlays and states where
  they belong; it has been hit more than once, again in boot:restore-first (2026-11-19).

```sh
spin-machine boot --hotplug-disks 2 --qmp /tmp/q.sock --init /bin/sh &
spin-machine attach --qmp /tmp/q.sock --disk disk.raw --disk-format raw --disk-serial ext   # sda, in the guest
spin-machine detach --qmp /tmp/q.sock --disk-serial ext
```

`detach` returns when QEMU reports the device free, but the guest notices the
ejection asynchronously: a check that greps for the node inside the guest runs
immediately after it can still see it, and saw it gone seconds later (measured
2026-10-03). Leave the guest a moment before treating a visible node as a
hot-unplug failure.

A one-off check needs no socket and no QMP: the shell's console is stdin/stdout, so
a script on stdin is what the guest runs, and `poweroff -f` ends the boot. `--init
/bin/sh` runs the script as PID 1 with nothing mounted, so it mounts `/proc` and
`/sys` itself when the check reads them, and `/proc` is required for `poweroff`.

```sh
printf '\n\n\n\n\n\n\n\n\n\nmount -t proc proc /proc\ncat /proc/cpuinfo | head -1\npoweroff -f\n' |
  spin-machine boot --release _output --init /bin/sh --console stdio --memory 1024 --cpus 2
```

The leading blank lines are load-bearing: the guest's shell is not reading its console
when QEMU starts feeding it, so the first bytes of the first line are lost, and a
leading `sleep` would be the thing eaten — `mount` arrived as `unt` and `sleep` as
`ep` before the blanks absorbed it. How far the window reaches is host timing, not a
line count, so keep the number of blanks generous rather than tuning it; a blank
costs only a prompt.

## spin-machine-dev and the Taskfile

`spin-machine-dev` (`cmd/spin-machine-dev`, `docs/cli.md`) is what a person types to build,
verify, boot, release and keep the pins: a verb is a goal, its state is files under `_output`,
and a workflow is the same verbs, a step each. `go tool task bin` builds it into `_output/bin`.
The Taskfile is go-tools' gates (`test`, `lint`, `mutate`, `coverage`) and CI's plumbing, and
the probes' targets in `boot/`. CI calls what a developer calls: a second definition of what
the gate is, kept in a workflow file, goes out of step with the first exactly when a check
is added.

## Licence and third-party source

Apache-2.0. New Go files carry `// SPDX-License-Identifier: Apache-2.0`.

Anything vendored into this tree goes in `NOTICE`, with its licence and — if it was
changed — a statement that it was. Anything that ends up *inside a release* goes in the
`SOURCES` file that `spin-machine-dev release` writes: what it is, which upstream tarball it was built
from, that tarball's SHA-256, and how to get it. The repository's licence covers the
recipes; a tarball of GPL binaries carries obligations of its own, and the release is where
they are answered.

An input downloaded from outside is pinned by hash and checked on every build, not only
after a download — the caches outlive the builds that filled them. The pin is written once, in
`versions.yaml`, and handed to the Dockerfile as a build argument with no default; a test fails
on a digest or a commit written anywhere else. `spin-machine-dev bump <entry>` moves one entry, and the
entry's note says what the bump is checked with.

## Versions

CalVer, `vYYYYMMDD.NN`. A release is the machine as it stood on a date. There is no API here
to promise compatibility about, and the one thing a version could promise — that checkpoints
still resume — is decided by the fingerprint, not by a number anybody chose.
