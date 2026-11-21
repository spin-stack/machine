# Moving a VM to another machine

A checkpoint is a state file that holds memory, device state and CPU state, written by a
`migrate` to a file, and a record beside it (`machine.Saved`): the state's format, the CPU the
guest was shown, the fingerprint and the QEMU that wrote it. Moving a VM is carrying both
somewhere else and resuming them there. `machine.CanResume` refuses a resume onto another
fingerprint, an older QEMU or a CPU that cannot show the guest's features again.

Stopping a VM here and resuming it there is a lifecycle, and this repository does not implement
it. The repository builds the machine that the lifecycle runs on, and offers the calls the
lifecycle needs. `Spec.Restore` starts a machine held for a checkpoint (`-incoming defer`).
`machine.VM.Resume` binds its disks and loads the state. It loads the state in full or, with
`Load.Fast`, runs the guest once its devices are in and reads the guest's memory as the guest
touches it (QEMU 11.2's fast snapshot load). A checkpoint is written in
`machine.FormatMappedRAM`. One saved before that format, in `FormatStream`, still loads, in full.

`spin-machine save --qmp … --to state` is the first half by hand, with the record written to
`state.json`. The second is `spin-machine boot --restore --qmp …` followed by
`spin-machine restore --qmp … --from state --disk …`.

## The CPU decides whether a VM can move at all

The default CPU model, `host`, shows the guest this host's own feature set through CPUID. The
guest reads CPUID once and never checks it again. Resume that guest on a host without AVX-512
and it executes an instruction that is not there. So a checkpoint records the CPU its guest was
shown: vendor, model and every feature (`machine.Saved.CPU`, from QEMU's
`query-cpu-model-expansion`). A machine resumes the checkpoint only when it can show all of that
CPU again (`machine.CanResume`). That record replaced the host's model name in the fingerprint
(2026-11-19). The model name was too strict, because a microcode or kernel update moved nothing a
guest sees. It was also too loose, because two hosts of one model name with different features
were one machine.

Naming a model makes every host show the same CPU, and a checkpoint resumes on any of them. The
model to name is the newest one every host of the fleet runs: `machine.Baseline` over each host's
`Models`. The cost is that a mixed fleet hides the newer hosts' features (AVX-512, AMX) from
every workspace. Measured 2026-09-07 (656d55b), restoring a state saved with `Broadwell-v4`:

| CPU on the second host | |
|---|---|
| `Broadwell-v4` | resumes |
| `Skylake-Client-v4` (richer) | resumes |
| `Nehalem` (poorer) | refuses: `Failed to set special registers` |

Save with the baseline, and any host that meets or exceeds it can take the VM.

**A named model carries `enforce=on`.** Without it, a model name does not fix the guest's CPU.
By default, QEMU warns about features the host cannot provide, removes them, and starts anyway.
The same model name then gives a different guest CPU on different hosts. Measured 2026-09-07: asking a
Raptor Lake host for `Skylake-Server-v4` gives five warnings about missing AVX-512 and exit 0.
With `enforce=on`, QEMU prints `Host doesn't support requested features` and exits 1, before
the VM exists.

## The devices have to match too

The devices must match down to whether there is a serial port: restoring without one fails
with `Unknown section or instance 'serial'`. That is why the fingerprint holds every device with
its properties ([machine.md](machine.md#the-fingerprint)). NICs are in it, by slot and MTU,
because a NIC is on the command line and so is present when the state is loaded. Disks are not.
They are the VM's and not the machine's, and whoever resumes a checkpoint gives it the disks it
was saved with.
