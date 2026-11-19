# Moving a VM to another machine

A checkpoint is one state file — memory, device state and CPU state, written by a
`migrate` to a file — carried somewhere else and resumed there.

Stopping a VM here and resuming it there is a lifecycle, and this repository does not
implement one — it builds the machine that lifecycle runs on, and offers the arguments it
needs: `Spec.Restore` starts a machine held for a checkpoint (`-incoming defer`), and
`machine.VM.Resume` binds its disks and loads the state, in full or, with `Load.Fast`, running
the guest once its devices are in and reading its memory as it touches it (QEMU 11.2's fast
snapshot load). A checkpoint is written in `machine.FormatMappedRAM`; one saved before it, in
`FormatStream`, still loads, in full.

`spin-machine save --qmp … --to state` is the first half by hand, and the second is
`spin-machine boot --restore --qmp …` followed by `spin-machine restore --qmp … --from state
--disk …`.

## The CPU decides whether a VM can move at all

The default, `host`, shows the guest this host's own feature set through CPUID — which the
guest reads once and never questions. Resume that somewhere without AVX-512 and it executes
an instruction that is not there. So a checkpoint records the CPU its guest was shown -
vendor, model and every feature (`machine.Saved.CPU`, from QEMU's `query-cpu-model-expansion`)
- and a machine resumes it only when it can show all of it again (`machine.CanResume`). That
replaced the host's model name in the fingerprint (2026-11-19), which was too strict - a
microcode or kernel update moved nothing a guest sees - and too loose: two hosts of one model
name with different features were one machine.

Naming a model — the newest every host of the fleet runs, `machine.Baseline` over each host's
`Models` — makes every host show the same CPU, and a checkpoint resumes on any of them. The
cost is that the newer hosts' features (AVX-512, AMX) are hidden from every workspace on a
mixed fleet. Measured, restoring a state
saved with `Broadwell-v4`:

| CPU on the second host | |
|---|---|
| `Broadwell-v4` | resumes |
| `Skylake-Client-v4` (richer) | resumes |
| `Nehalem` (poorer) | refuses: `Failed to set special registers` |

Save with the baseline, and any host that meets or exceeds it can take the VM.

**A named model carries `enforce=on`**, and without it naming one means nothing. QEMU's
default is to warn about features the host cannot provide and start anyway, having quietly
removed them — so the same model name gives a different guest CPU on different machines.
Measured: asking a Raptor Lake host for `Skylake-Server-v4` gives five warnings about
missing AVX-512 and exit 0. With `enforce=on` it is `Host doesn't support requested
features` and exit 1, before the VM exists.

## The devices have to match too

Down to whether there is a serial port — restoring without one says `Unknown section or
instance 'serial'`. That is why every device, with its properties, is in the fingerprint
([machine.md](machine.md#the-fingerprint)). NICs are in it, by slot and MTU: a NIC is on
the command line, so it is there when the state is loaded. Disks are not: they are the VM's
and not the machine's, and whoever resumes a checkpoint gives it the disks it was saved with.
