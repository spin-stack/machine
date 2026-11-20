# Log: what was done, why, and what it measured

Built from `git log origin/main` (327 commits, to `3e8075f`), all 150 pull requests (merged, closed and open), the 44 releases' notes, the tree at `origin/main` (comments, `docs/`, `boot/*_test.go` row comments), and spin-stack/spin#1206 and spin-stack/open-remote-ssh#8. Hashes are short commit ids on `origin/main`; `#N` is a pull request here; "lab N" is a `lab.yml` run id as the tree or a PR quotes it. Nothing here was re-measured. Written 2026-11-13; add an entry with every change that decides something.

Where boot time stands, what each phase costs, how a number is measured, and the tables of what was kept, measured and rejected, and is next are `docs/lab.md`. This log is the chronology.

---

## 1. Chronological log

Format: **date · area · refs**, then what was done, why, the result and its status.

### Week 1: 2026-09-07 to 09-13

**09-07 · all · `2afac47`, `219b98a`.** The repository is created from spinbox's and storage's QEMU, kernel and image (image ported to mkosi, ext4 in a read-only qcow2). qemu-nbd dropped, Taskfile split per part, `task shell` added. *Why:* they were one machine, duplicated across repositories. *Result:* "boots as one machine … in 151 ms" (`2afac47`, consumer's initrd). *Status:* in place.

**09-07 · machine/fingerprint · `5e7d60e`, `b476f6d`, `57e827a`, `b882171`, `17d844f`, `02709d7`.** `machine/` defines the slot map, the shape and the cmdline. `Fingerprint` hashes QEMU, the kernel and the initrd by content, plus the shape. Same day:
- the device topology goes into the hash (`b476f6d`);
- the host CPU goes back in under `-cpu host` (`57e827a`);
- the topology is corrected to leave out disks and NICs (`b882171`: templates have neither);
- the encoding is made length-prefixed (`17d844f`);
- an identity split is added for memoising (`02709d7`).

*Status:* superseded. See the fingerprint cycle in §2.

**09-07 · machine · `656d55b` → `b882171`.** `spin-machine save` and `boot --incoming file:` are added, then reverted the same day as "lifecycle" that belongs to the consumer. Kept: `Spec.CPU` with `enforce=on`. Measured: a state saved with Broadwell-v4 resumes on Broadwell-v4 and Skylake-Client-v4, and Nehalem refuses (`656d55b`, now in `docs/migration.md`). *Status:* save/restore came back as checkpoint commands (`c202568`, 09-28; kept after #51).

**09-07 · tooling · `5e7d60e`, `8aee03a`, `24c8a06`.** `cmd/spin-machine-init` is a debug initramfs, and `spin-machine-console.service` is shipped but not enabled. *Why:* systemd gave no serial login, because the image masked udev, so `dev-ttyS0.device` never appeared. *Status:* the init was deleted `8eeb01c` (09-10) and the console unit deleted `3a7864d` (11-12). See §2.

**09-07 · qemu · `31a57e7`.** QEMU is linked statically against musl. *Measured:* process start to QMP quit, 44 ms glibc vs 43 ms musl over 5 runs. Not measured: a long-running guest under load (musl allocator, thread stack). *Status:* in place; the load question is still open.

**09-07 · image/kernel · `2df3dbf`.** Changes:
- root password removed;
- SSH host keys generated per boot (unit hung off ssh.service);
- chrony via `/dev/ptp0`;
- BPF_JIT and BTF on, at +21.4 ms of kernel boot and 5.0 MB (5 runs each way; also `kernel/README.md`);
- `-no-reboot`.

BPF_LSM was left off. *Status:* BPF on and in place; chrony removed 09-10; BPF_LSM turned on 10-01 (`1400f6a`); the keys moved 3 more times (§2).

**09-07 · qemu/machine · `8b7391a`.** aio=io_uring, discard=unmap, vhost=on requested, qom_cast_debug off. *Rejected:*
- `cache=none`: the shared base must stay in the host page cache, and O_DIRECT fails on tmpfs;
- `--enable-virtfs`;
- four configure flags that do not exist in 11.1.1.

*Status:* in place.

**09-07 · machine/kernel · `b476f6d`, `ee34d31`.** virtio-balloon with free-page-reporting. *Measured:* "costs nothing measurable", 158.9-162.5 ms with vs 157.9-171.6 without, noise ~14 ms. virtio-mem replaces DIMM slots, with `memhp_default_state=online` required: without it a request for 2048 then 3072 MiB plugged 1024 and stopped. Measured 0→2048→3072→512→2 MiB. *Status:* in place.

**09-07 · machine · `58d404e`.** `tsc=reliable` restored; it had been lost in the move from spinbox. Not measured. In place.

**09-08 · machine · `fa1803f`.** ICH9 `sata=off`, `smbus=off`. *Measured:* 400 boots per variant, kernel to init: 62.3 baseline, 61.3 sata=off, 61.7 smbus=off, 60.6 both; an earlier 150-boot series agrees. *Measured and rejected:* `usb=off` 63.0 vs 62.9; `vmport=off` 62.6; `smm=off` 62.3; `i8042=off` 62.5. *Status:* in place.

**09-08 · CI · `fe332b4` (#4).** `task verify:args` starts 9 Specs under the TCG binary with `-S` and requires a QMP answer. *Why:* nothing checked that QEMU accepts the command line. In place (it became `TestQEMUAcceptsEveryArgument`).

**09-09 · kernel cmdline · `9ca453a`.** `nohz=off` removed. *Measured:* a forced tick is 1.7% of a core per idle vCPU. Boot 821-853 ms vs 843-846; restore 113-221 vs 124-216 (template era). In place.

**09-09 · machine · `76aacf9`, `7cafdf6`, `6599829`, `2b46d78`, `354c8a6`.** Changes:
- PCIe root ports for hotplug: no measured change to boot or restore;
- a NIC through a root port via `netdev_add fd=`;
- NIC count and `host_mtu` go into the fingerprint, while MAC and fd stay out;
- a second QMP monitor.

*Status:* root ports were replaced by virtio-scsi on 09-29 (`2b19096`); NIC hotplug was dropped then; MTU and NIC in the fingerprint were superseded by `Spec.Contract` (`fa3041c`).

**09-10 · boot probes/image · `4cbd229`, `bab6f16`, `c2c4c6a`, `722d468`, `b5f361f`.** `task boot:matrix` (shell). *Found:* masking udev buys nothing (826 vs 829 ms), and masking it costs a 10 s `dev-ttyS0.device` timeout with no login. udev was unmasked (`722d468`) and `40-spin-hotadd.rules` added for CPU and memory. Two stalls of 334 ms waiting on the console (XTGETTCAP and a terminal reset) are fixed by `TERM=dumb` on the kernel cmdline: 687→19 ms, and the whole boot 855→163-207 ms (`b5f361f`). The harness itself was contaminated: `unmask_udev` had become a no-op, so the first udev rows (826/829) were wrong (`b5f361f`). *Status:* udev on and TERM=dumb in place. The memory half of the hot-add rule was removed 11-12 (`3c8da99`).

**09-10 · tooling · `8eeb01c`.** Debug initramfs deleted. "this repository does not ship an init". udev on → serial getty works: 204 ms to a login prompt. In place.

**09-10 · boot probes · `80f984b`.** The boot bench is ported to Go and the shell harness deleted. *Found:*
- `systemd.mask=` on the cmdline does nothing (debug-generator masked), so every earlier row that used it measured nothing;
- agetty has an unconditional `sleep(1)`;
- the shell harness throttled the console (it read 1424 ms where the guest said 190);
- a Percentile bug.

*Measured (15 boots, laptop):* as shipped 1256/1276; without agetty 253/320; 512 MiB 1241/1261; 1 vCPU 1309/1326; udev masked: 15 of 15 boots never reached a login. Superseded by `boot:bench`; `boot:bench` was deleted in #250.

**09-10 · kernel · `c03ed89`, `1a8e25c`, `a31d7b2`, `f32d527`, `64d2901`, `e285dec`.** A kernel-phase investigation. Results, each recorded in `kernel/Dockerfile`:
- initcall_debug with a console attached measures the console (103.9 vs 42.8 ms);
- the 8250 trim is worth 154 µs;
- a modular kernel buys 0.57 ms and was rejected;
- acpi_bus_scan scales with the vCPU ceiling: maxcpus=16 +5 ms and maxcpus=64 +9 ms of kernel (63.0/68.1/72.1 ms), recorded at `Spec.MaxCPUs`;
- four ACPI cmdline parameters are worth 0 (69.9 vs 68.6);
- ksm_init and kcompactd_init are worth 0;
- `event_trace_enable_again`'s 6 ms is the profiler;
- inet_init's 9 ms is BTF's first parse, not XFRM.

*Status:* rejected (docs/lab.md, "Measured and rejected").

**09-10 · image · `6aba37b`, `064c4f4`, `c7bab94`, `c7af4d3`, `08c56e1`.** Changes and measurements:
- logind 63 ms on the chain, worth 24 ms; chrony worth 10 (15 boots, laptop);
- Type=idle on the getty measured at 25 ms (291/344 vs 266/283);
- `disable-udev-settle.rules` deleted as wrong;
- chrony deleted: 248/322 vs 261/288; "a restore is invisible from the inside", and correcting the clock belongs to whoever restores;
- blame figures that summed to ~380 ms out of a 175 ms userspace are kept only as history.

*Status:* chrony removed and in place.

**09-10 · qemu · `b151186`.** The device list is turned into an allowlist (`--without-default-devices`): 363→225 instantiable types and a 23.7→8.7 MB binary. MICROVM rejected ("no ACPI CPU hotplug"). Boot unchanged (240/274 vs 245/265, laptop). In place.

**09-10 · boot probes · `c049a21`, `8eef89d`, `a0efd65`.** QEMU's startup split: 1.4 ms launcher, ~27 ms exec to first instruction, ~7 ms guest to first console byte. The QMP greeting is not a checkpoint: it comes before the board is built, so the real QMP reply is at 18-20 ms, not 8. Cold-start cost is the host zeroing pages (18.8%) plus rom_reset; a restore pays neither (09-26). Not acted on.

**09-11 · machine · `21a830c`.** `Disk.DirectOverBacking`, with a cache-modes bench of 16 VMs. Writes were inconclusive: the disk was 94% full and times moved tenfold. `mincore` reports every page resident for a file the caller does not own. *Status:* the field is still in `machine/machine.go:182`; the bench was deleted in #48; `Disk.Cache` was removed in #94.

**09-11 · image · `ec008b0`.** sshd `TrustedUserCAKeys` (the CA file is written by the consumer). #101 (10-03): without the CA nothing logs in. In place.

**09-12 · machine · `9177475`.** `Spec.Accel` (tcg). TCG guest to PID 1 in 6.5 s vs 33 ms under KVM. In place.

**09-12 · image · `bb6bb89` (#5).** logind out of the boot transaction; it is started by the first login through the varlink socket and a `/run/systemd/seats` drop-in (`10-seats.conf`). *Measured:* 249/287 vs 273/297, 25 boots, two runs (laptop). `boot:logind` holds sessions. *Status:* **reversed 11-12** (#240). See §2.

### Week 2: 2026-09-14 to 09-20

**09-14 · image · `9335335`, `fe7d4ab`, `891e2f7`, `b5908ee`.** Changes:
- the image is sized in two passes: 974 MiB with 132 free, was 1163 with 317;
- e2fsprogs 1.47.4 is built static and pinned;
- `-m 0` for reserved blocks;
- a 64 MiB journal.

Bug: `891e2f7` (shipped in v20260914.01) made a 4.08 GiB filesystem, because `-b` read the size as a block count. Fixed in `b5908ee`. In place.

**09-14 · kernel · `b9d1985`.** A trixie toolchain and a reproducible vmlinux. *Why:* v20260914.01 and .02 had different kernels from the same source. bookworm's pahole 1.24 encoded `.BTF` in parallel, in a non-deterministic order, and the banner carried the build date. Two `--no-cache` builds now give the same vmlinux. Usable 503/575 vs 509/547 with gcc 12 (laptop, 20 boots). In place.

**09-15 · CI · `c767cd5`.** The BuildKit `type=gha` cache had never stored anything: the runtime token does not reach `run:` steps, and every build shared one scope. Fixed with named scopes. Superseded by the registry cache (`1666cb5`, 11-12).

**09-18 · machine · `da17b34`, `f023b56`, `5d4a01c`, `4947281`.** A restore opens its template read-only. Disks can be given as a chain of descriptors (`-add-fd`), and so can the console, `/dev/kvm`, vhost-vsock and vhost-net. In place, apart from the template parts.

**09-20 · CI · `f63aa38` (#10).** Workflows move to Blacksmith. Superseded 10-24 (`f29e59d`).

### Week 3: 2026-09-21 to 09-27

**09-21 · machine · `3d10256`, `56f6fea`.** `FingerprintCache` added. *Status:* removed `2d9b7e3` (09-29), with no caller; the identity split went in `c5baaa7` (10-01).

**09-24 · machine · `35df9cf`.** No VMX/SVM in the guest. Measured with 3 hot-added cores: vmx=1,0,0,0 plus a warning before, vmx=0 on all after. Fingerprint moved. In place.

**09-24 · image · `a01c847`.** Host keys made without RSA. RSA was 309 ms in blame in a guest, and on the host 141 ms vs 6 (ECDSA) and 5 (Ed25519). In place.

**09-26 · image · `f36ae13`.** `/tmp` is a tmpfs again and tmpfiles-setup is unmasked; both had been masked by the imported optimizer. 223/230 vs 220/232, within noise. In place; `/tmp` was capped at a quarter of memory in `47aa681`.

**09-26 · machine · `8063bcd`.** discard=unmap on the format node of a chain. Before this, fstrim freed nothing and deleted data stayed readable. Not measured. In place.

**09-26 · firmware/qboot · `e4f456f` (#12).** The qboot experiment: `write-pointer.patch` (vmgenid needs WRITE_POINTER), built in a pinned container. The compiler moves the number: 6.32 ms saved with GCC 15.2 vs 3.86 with GCC 14.2 (`qemu/qboot/README.md`). Not adopted yet, because firmware was not in the fingerprint. *Status:* adopted 09-29 (`f2f4da0`).

**09-26 · boot probes · `5221690`, `c6e48de`, `adc52cd`, `e2f6b8f`, `b9707e3`, `7b08e5a`.** Probes added: `boot:initcalls`, `boot:userspace`, `boot:systemd`. Findings:
- p50 98.2 ms kernel to "Freeing", of which 54.5 ms is 680 initcalls;
- udev coldplugs 266 devices, 64 of them `ttyN`;
- `CONFIG_VT=n` was predicted at 5-12 ms and measured at 0 (228/237 vs 228/246);
- 19 udev rule files masked: 0;
- device-count boot parameters: 0;
- the console-unit swap measured 340 ms worse, and the cause was finally found to be a 334 ms XTGETTCAP timeout, fixed with `TERM=dumb` on that unit (221/233 vs 558/572).

*Status:* findings recorded. The rows were deleted in `3f669ef` and #90. `boot:userspace` and `boot:systemd` were deleted in #250.

**09-26 · kernel · `dae6a14`, `5a3d509`, `cda04b1`, `9b5f0dd`.** A floor kernel (tinyconfig + PVH): release 97.95 vs floor 52.33 ms, so the config costs 45.6 ms (BTF, ACPI, ftrace). lz4 instead of gzip for an initrd: 6.4 ms. erofs removed (100.46 vs 102.22, noise). KSM and THP off: -0.50 ms, rejected. `CONFIG_VT` off as dead config; the 2.09 ms quoted for it was withdrawn (0.31 ms over 40 boots on an idle host). *Status:* erofs and VT off are in place. The floor probe and `kernel/minimal` were deleted in #48 (09-30).

**09-26 · image · `9fab3f1`, `ad05544`, `9b5f0dd`.** Unit-file count does not matter: 300 extra units gave 236 vs 246. Unit loading costs 26 ms per `systemd --test`, not the 212 the debug log suggested. Masking the tpm2 and factory-reset generators is worth 1.0 ms of that 26. An empty `/etc/machine-id` skips the preset pass. *Status:* the masks are in place; `boot:unitload` was deleted in #88.

**09-26 · machine · `ab4844c`.** `drop-cache=off` on chain images: a restore had been issuing `POSIX_FADV_DONTNEED` on the shared base. Restore exec-to-running 29 ms with an overlay vs 24 without disks; `cont` 8.9→2.8 ms; a cold cache has been measured at 251 ms. In place.

**09-26 · CI · `aa157cf`, `1ef95d1`.** The Blacksmith builder v2 required a `cache-key` and silently fell back to a local builder without it. Fixed with `nofallback`. Superseded 10-24.

**09-27 · machine · `7ec3601`, `2f5a7b0`.** ACPI PCI hotplug is turned off and the guest's pciehp used instead. *Why:* device_del never completed, and spin's lane waited 30 s. Fingerprint moved. *Status:* superseded by virtio-scsi (09-29).

**09-27 · tooling · `0b1f470`, `51c1fe9`, `2f03d09`, `e7e52db`.** `boot` with no disk uses `-snapshot`. Added: attach/detach, and the rule that checks go through the CLI. boot now execs QEMU, and the QMP handshake has a 5 s deadline. Validation was hardened, and the fingerprint moved again (`-accel` became its own argument). In place.

### Week 4: 2026-09-28 to 10-04

**09-28 · machine/CLI · `c202568`.** `save --template` and `restore` added, plus `boot:restore`: restore ~20 ms; first request 14 ms warm, 33 cold, 31 after a reclaim. *Status:* the template parts were removed 09-30 (#51); restore was not measured again until 11-13.

**09-28 · tooling · `30b717b`, `a8e2737`.** Every pin goes into `versions.yaml` with a gate test; the versions tool moved to go-tools. In place.

**09-28 · kernel · `2fd0fff`, `430d2b3`.** 7.2.8 with PSI; then MGLRU, PARAVIRT_SPINLOCKS, PARAVIRT_TIME_ACCOUNTING, fq_codel and hung-task detectors. 322 vs 333 ms usable (15 boots). ZRAM rejected. In place.

**09-28 · boot probes/image · `f7cd1ab`, `47aa681`, `337c08d`.** `boot:ssh-load`: CPU load does not slow a login, and 16 MiB available doubles one to ~210-310 ms. CPUWeight/IOWeight/MemoryLow on ssh.service changed nothing and are not shipped. MGLRU `min_ttl_ms=1000` and `/tmp` at a quarter of memory. **The sources disagree** on min_ttl_ms: `47aa681` says worst login 1369-7106 ms without vs 94-128 with; `kernel/Dockerfile:192-194` says 611-1785 vs 105-106. *Status:* in place; `boot:memory-policy` was deleted in #88, and `boot:ssh-load` was deleted in #250.

**09-28 · CI · `5332e97`, `3bef421`, `e6a0a1b`, `6b0f338`.** Actions pinned by commit, actionlint, the race detector, `task mutate`, a coverage floor. The floor was set at 85% against a local 85.7%; CI measures 84.7%, so it broke main and was lowered to 80% (`e6a0a1b`). In place.

**09-29 · machine/qemu · `43fb03b` (#24), `2b19096` (#25).** Hotplugged disks go on virtio-scsi instead of PCIe root ports. *Measured 2026-09-29, 4 ports vs 1 controller:* boot -31 ms; attach 135 → 1.5 ms; detach 6.1 s → 0.7 ms. NIC hotplug dropped. In place.

**09-29 · boot probes/CI · `a503db7` (#23), `e9e0c8e`, `de9b7db` (#29).** A report feature matrix (96 rows) published with every release, and `lab.yml` on the self-hosted KVM runner. *Why:* a laptop read 61 vs 105 ms minutes apart. Fixed along the way:
- the lab went green having measured nothing (a skip) (`8d8e97b`);
- a concurrency group cancelled queued experiments (`a3ba5a6`);
- two runners both took nbd0 (`b21e100`);
- an NBD device was mounted before it had a size (`8946f67`).

In place, with the lab on the bench fleet since 10-24.

**09-29/30 · kernel · `349dacb` (#33), `20fd702`, `3e26963` (#40).** Four local patches: 0001 objtool writes NOPs, -7.0 ms; 0002 skips the weak-function scan, 11.9 ms; 0003 rewrites the eval maps on first use, -9.0 ms; 0004 PCI config through ECAM, -4.5 ms. Kernel boot 109.4 → 73.7 ms (lab runner, workspace shape, 15-20 boots); the whole report boots on it (run 36651858261). Fingerprint moved. In place.

**09-29 · firmware · `ae6ab48` (#37), `f2f4da0` (#39).** qboot becomes the default BIOS and the firmware goes into the fingerprint. SeaBIOS 121.89 vs qboot 114.76 ms p50, 7.14 ms saved (lab runner, run 36650049960); 106 rows boot (run 36650559189). In place; the firmware *contents* were later dropped from the fingerprint and only its size kept (`fa3041c`).

**09-30 · qemu/firmware · `6f65249` (#42), `f10383b` (#43).** qboot writes the PAM registers in 3 writes instead of 7, at ~0.5 ms each. QEMU patch 0001 wraps device creation in one memory transaction (438 flat-view rebuilds). The first version also wrapped board init and the guest printed nothing (lab run 36662499370). In place.

**09-30 · kernel · `98c3589` (#47).** 7.3-rc5, 1.2 ms slower than 7.2.8 (30 boots). Superseded by 7.3 (10-26) and then 7.3.x.

**09-30 · boot probes · `f2c47f7` (#48).** Probes deleted with their conclusions kept: `boot:floor` and `kernel/minimal`, `boot:kernel-pages` (#44, added the same day), `boot:memory-unplug` (#46, same day), `cache_bench`, the report's disk axis (96 → 32 rows), the console-swap rows, and the SeaBIOS timing. See §2.

**09-30 · machine · `a9d6083` (#51).** **Templates removed** (spin #194). `Memory.File`, `x-ignore-shared`, `save/restore --template`, `boot:restore` and `docs/templates.md` all go. The fingerprint now means "a checkpoint resumes only onto a machine with the same fingerprint". Fingerprint moved. In place.

**09-30/10-01 · boot probes (memory/disk) · #57-#62, #64, #67-#74, #77, #78, #83, #84, #89.** The page-cache lab on construct (AMD Ryzen 9 5900X, Spin OS metal), recorded in `docs/memory-and-disk.md`. Kept as defaults: writeback overlay; hourly fstrim; discard accounting; qboot MTRRs; `page_reporting_order` stays 9. Rejected: docs/lab.md, "Measured and rejected". Sub-probes deleted in #89; `caches` and `reclaim` remain.

**10-01 · firmware · `4511798` (#75).** qboot programs the MTRRs. virtio-pmem read at 8.3 MB/s uncached-minus vs 1.1 GB/s with SeaBIOS (runs 36818224412, 36818721809). No boot cost: 103.5 vs 103.1 ms. Fingerprint moved. In place.

**10-01 · image · `5b7b1dc` (#76).** fstrim hourly: 657 → 21 MB on disk, 657 → 20 MB in the host's cache (run 36815822140). In place.

**10-01 · qemu · `aaf2d86` (#79).** Upstream's virtio-blk discard accounting is carried as a patch: before it, `query-blockstats` showed 0 unmaps for 1.1 GiB trimmed (run 36815822140); with it, 10-12 operations and 1123 MB (run 36884702758). "to delete in the bump that brings it". In place.

**10-01 · kernel · `1400f6a` (#80).** Lockdown at confidentiality plus the BPF LSM; no `/dev/mem` or `/proc/kcore`. "No cost that can be told from noise." *Side effect, found later:* SPIN_GRAPH and SPIN_CPU_SHARE silently traced nothing until #240 (11-13). In place.

**10-01 · CI/release · `2cd98f0` (#81).** Release notes say what changed, and the report boots each row 20 times, KVM only. *Why:* at 3 boots a row every KVM row read 5-11% slower between two releases that 20 alternated boots put within 5 ms. Also: `v20261001.01` shipped a `qboot.bin` without `mtrr.patch` in the tarball, because hack/release named the patches one by one. In place.

**10-01 · boot probes · #88, #90.** `unitload` deleted (it compared the image with itself) and `memory-policy` deleted. Bench rows reduced to "as shipped" and "baseline", with findings left as notes. In place.

**10-01 · machine · `c5baaa7` (#97), `ca7b54f` (#98), #94.** The fingerprint becomes one hash, and `Incoming` one field. The release verdict was blind to `machine/` changes, so `fingerprint_sha256` is now recorded. `Disk.Cache` removed. In place.

### Week 5: 2026-10-05 to 10-11

**10-09 · qemu · `f0ec1e0` (#103).** QEMU 11.1.2. "A new machine: every checkpoint taken on 11.1.1 stops resuming." That stranded v20261002.01's checkpoints (v20261013.01). Led to the next entry.

### Week 6: 2026-10-12 to 10-18

**10-13 · CI/release · `d692a7c`, `03aeb28`, `96a39d2`, `845fb5d`, `f033995` (#109), `b207bee` (#110), `302ced2` (#111), `99e31f9`.** Supply chain:
- signing and attestation, an SBOM, release from main only, apk locks;
- a reusable builder (#109, #110), reverted the same day (#111): it never achieved SLSA L3 and needed guards;
- an OSV scan, kept;
- attestations checked by digest, because the 338 MB tarball was over cosign's 128 MiB limit.

In place, apart from the reusable builder.

**10-13 · machine/fingerprint · `fa3041c` (#114).** **The fingerprint is the machine's contract.** `MachineType=pc-q35-11.1`. It hashes: the shape; `Spec.Contract` (every device as Args writes it); the BIOS's size; and the host CPU under `-cpu host`. It no longer hashes the QEMU, kernel, initrd or pvh.bin bytes. Added `boot:roms`, `boot:compat` (a v20261002.01 checkpoint resumes on 11.1.2), and `hack/machinetype`. One break by design. In place; `fingerprint_sha256=2bd7da6b…` from v20261013.02 onward.

**10-13/14 · CI · `7628981`, `4f7d55e`, `8c6d1c7`.** compat runs on main and before a release, not on PRs, because the KVM runner refuses pull_request jobs. cosign is built from its pin. Docker Hub images come from mirror.gcr.io after a 429. In place.

### Week 7: 2026-10-19 to 10-25

**10-19 · image · `fb39fd5` (#210).** GNU coreutils instead of uutils: 21 OSV findings against rust-coreutils 0.10.0; image 855M vs 877M. In place.

**10-19 · CI · `7bf5429` (#197).** One OSV issue per package; there had been 77 open. In place.

**10-24 · CI · `c75f144` (#214), `f29e59d` (#215), `d67afc4`, `97a6de1`.** Every workflow step is one task. Builds move to GitHub's runners. **Report and lab move to the bench fleet** (one instance type) and compat to the kvm fleet. A fix: vhost-vsock permissions were reset by udev after `modprobe` (`97a6de1`). In place. The first report on the fleet (v20261026.01) compared a Ryzen 9 5900X with a Xeon 6975P-C, and its notes say so (docs/lab.md, "Measurement pitfalls").

### Week 8: 2026-10-26 to 11-01

**10-26/29 · kernel · `1c12413`, `0bb00ce`, `fc1079c`.** Kernel 7.3; the apk locks moved; the kernel now tracks mainline. In place.

### Week 9: 2026-11-02 to 11-08

**11-03 · qemu/kernel · `7c37270`, `b4b67c3`, `6e7ba47`.** QEMU 11.1.3, kernel 7.3.1. The apk lock broke because Alpine's mirror dropped python3 3.14.8. In place.

### Week 10: 2026-11-09 to 11-13

**11-09 · kernel/CI · `00db122` (#223), `0ce27ad` (#224), `8d495c5` (#225).** Kernel 7.3.3, with the config named by series and a daily proposal job. A kernel CVE judge: of 18221 CVEs against 7.3.1, 9 apply with a fix later in 7.3 and 1 has no fix. The apk lock broke again (zlib 1.4). In place.

**11-09 · tooling · `ed4ad86`, `e1d40ad`, `6e25bb3`.** Retries and tokens for GitHub downloads after 504s. Task and go-tools as released binaries (455 → 81 modules). In place.

**11-11 · kernel · `cce320f` (#229).** 7.3.4. In place.

**11-12 · docs/image · `e452afc` (#232), `3a7864d`.** A comment sweep: several comments still said a QEMU or kernel bump strands checkpoints, which has been false since `fa3041c`. Dead code deleted: `spin-machine-console.service`, the modprobe blacklist, and the getty@tty2-6 masks. In place.

**11-12 · image · `02c58b9` (#235).** bubblewrap, socat and ripgrep for agent sandboxes. In place.

**11-12 · CI · `c6c29b5` (#236), `ec2c7fb` (#237), `1666cb5` (#239).** A daily apk-lock refresh PR. *Why:* the lock broke three times (`6e7ba47`, `8d495c5`, release run 43157106109). The BuildKit cache moved to ghcr: on the fleet the gha cache read a store nobody wrote, so compat compiled cold on every run and failed on main 4 times from 11-09; both release attempts on 11-12 failed. In place.

**11-12 · image · `3c8da99` (#238).** The hot-add rule onlines CPUs only. udev's memory "online" write raced the kernel's `memhp_default_state=online` and logged EINVAL for every block. Closes #230. In place.

**11-12/13 · image/kernel/boot probes · #240 (`bc41f44`, `a8df865`, `28823fb`, `8fb1dd9`, `e5b8003`).**
- Kernel patch 0005, each PCI capability list walked once: kernel 95.1 → 88.9 ms; ECAM accesses 1,032 → 680 (lab 43327612594).
- **logind back in the boot, and spin lingers:** first SSH login 570/626 → 510/525 (lab 43308825692, 10 boots).
- MOTD and pam_motd off: -7 ms per login, -12 on the first.
- `sshd@` waits for the host keys. Before this, SSH over vsock "worked not at all".
- New probes: `boot:waits`, `boot:jobs` (+ jobwatch, godbus), `boot:ssh-first` (+ vsockproxy), `boot:exits`, `boot:mmio`.

Measured and not shipped:
- the getty without BindsTo: 342 vs 345 (lab 43303190733);
- keygen in the boot: 518 vs 514 (lab 43312419007);
- virtio-rng removed: -13.6 ms to the prompt (lab 43321809471), not done because it would move the fingerprint;
- `pci=noearly`: the guest does not boot;
- PID 1 spends ~25 ms in the BPF verifier.

*Side effect:* v20261113.01's console prompt came 21-25% later (release notes). *Status:* patch 0005 and the exits/mmio/jobs probes were deleted in #249 (11-13).

**11-13 · image · `43de41e` (#241).** serial-getty Type=simple. *Why:* with linger, `user@1000` runs inside the boot transaction, and Type=idle held the getty for 91.8 ms (lab 43361486792). 346/358 vs 423/441 (lab 43362067503); v20261112.01 was 344/353. Released in v20261113.02. *Status:* removed in #249 (11-13): nothing a workspace does waits for the console's prompt.

**11-13 · boot probes/machine · #242, #246 (both closed).** Restore measured, and a **mapped-ram** checkpoint layout proposed twice:
- #242 (draft): `--channels`; mapped-ram ×4 restore-to-login 394/410 vs stream 877/1888 (lab 43364516874). Those numbers were later declared contaminated by restored VMs leaking between repetitions.
- #246: mapped-ram put in `Spec.Contract`, which would move the fingerprint, plus `boot:score`. On the fleet, with every cold boot run before every restore: restore login 385/400 mapped-ram vs 386/399 stream (lab 43483852339); also lab 43482829958, 373/402 vs 376/416. Closed: "does not earn moving the fingerprint". A laptop lead of 188/358 vs 412/732 was called "a lead, not a finding".

*Status:* rejected; #242 and #246 closed.

**11-13 · image · `d895543` (#245), `5e0cc2f`.** sshd offers only `mlkem768x25519-sha256`. ML-KEM costs the same as curve25519: first login 461 for both; a second login 40 vs 39; OpenSSH 9.6 with sntrup761 took 489 first and 104 for a second (lab 43479854381, also 43463472762). `boot:ssh-first` logs in with x/crypto/ssh, and vsockproxy is removed. *Status:* merged 18:30Z. #245's own body said "Do not merge before the clients move": open-remote-ssh#8 merged at 18:57Z, and spin-stack/spin#1206 (the spin CLI refusing OpenSSH < 9.9) merged after its CI and KVM lanes passed. Not yet released.

**11-13 · image · `3e8075f` (#247 → #248).** SSH host keys are made from the boot's first dispatch (wanted by sysinit.target, `DefaultDependencies=no`). First login 489/506 → 468/494; accept to login 177 → 154 (lab 43483372488). Rejected in the same run: masking two generators (489), and masking 11 always-failing units (484). Not released.

**11-13 · boot probes · #249, #250.** Removed:
- kernel patch 0005 ("6 ms of a ~470 ms first SSH login, for a local patch carried across every kernel bump");
- the getty Type=simple drop-in ("nothing a workspace does waits for that prompt");
- `boot:exits`, `boot:mmio`, `boot:jobs`/jobwatch/godbus.

#250 also removes `boot:bench`, `boot:trace`, `boot:userspace`, `boot:systemd` and `boot:ssh-load`, keeping "a gate …, the release's report, the metric a workspace waits for (ssh-first), and one tool per layer".

**11-13 · goal · #251, #252, #254.** The goal is written down: a cold boot to the first SSH login in under 100 ms (AGENTS.md), with restore as the second number. The rules that held exploration back are removed (no init in a release, the fingerprint as an event to avoid, one default per experiment, the bench fleet alone deciding); hotplug and dynamic memory become an invariant that is never traded for boot time.

**11-13 · boot probes · branch `floor-kernel` (experiment).** The floor without systemd: PID 1 a script that mounts what sshd needs, makes an Ed25519 key and has socat hand vsock connections to `sshd -i`. First login 467 → 220 ms (lab 43492686353), 506 → 240 (lab 43493135230); guest uptime puts init at ~90 ms of kernel and the keys 10-20 ms later. What is left at the floor: QEMU and firmware ~44 ms (boot:firmware-stages, lab 43494513791: QEMU's own start 35.9, qboot ~8.5, of QEMU's start 7.9 copying vmlinux), kernel ~80-90, init ~20, the login ~44. `/proc/uptime` starts partway into the kernel, so it hides ~35 ms that an earlier reading charged to QEMU.

**11-13 · CI · spin-stack/ci#14, #15, spin-machine#253.** A `bench-metal` fleet (c8i.metal-48xl, then r7i.metal-24xl behind it) and lab.yml's `host` input. It has not run: metal spot scored 1-3 of 10 for placement and failed `UnfulfillableCapacity` in every zone; on-demand failed `VcpuLimitExceeded` at a quota of 96 vCPU. An increase to 288 was requested the same day (case open).

**11-13 · kernel · #255 (this change).** Where the kernel's ~100 ms goes on the bench fleet (boot:initcalls lab 43495583323): initcalls 52 (acpi_init 12, of which `acpi_pci_root_create` - bus 0's enumeration - 11.2 traced on a kernel without forced lockdown, lab 43499868986; inet_init 7.9, which is vmlinux BTF's first parse; the virtio probes ~17), before the first initcall ~15-20 (SPIN_EARLY, lab 43507472288; ~4 of it the probe's own 18 MB log buffer), SMP ~4, the root mount ~6. Kept: patch 0005 back, 0006 (BTF parsed in the background from a core_initcall), `CONFIG_DEFERRED_STRUCT_PAGE_INIT=y`: kernel 96.3 → 87.1 ms (lab 43505126131), first login 466 → 456 as built and 223 → 214 at the floor (lab 43501992969). `driver_async_probe` for rng, balloon and vsock on the command line: 214 → 209 at the floor (lab 43506390106).

**11-13 · image · the first login's lane (this change).** Where systemd's ~250 ms over the floor went, on the bench fleet: ~60 before sshd accepts (sysinit, udev, journald), ~105 in pam_systemd - logind answers a session of class `user` only once the user's manager has started and orders its scope after `systemd-user-sessions` (systemd v259.9 source, `logind-session.c`) - and ~65 in a handshake competing for the CPU. The vsock socket, `sshd@`, dbus, logind, `user-runtime-dir@` and `user@` without default dependencies, no `/run/nologin` at boot (pam_nologin refused the lane's first login: "Access denied for user spin by PAM account configuration"), sessions of class `user-early-light`, and udev's coldplug filtered to block, net, virtio, misc, input, rtc, ptp and ttyS0: first login 478 → 402 ms (lab 43518915618). Measured and left out: priority (`CPUWeight`, `Nice`) on the lane, −2 ms over the filter and a slower second login (63 vs 45 ms).

**11-13 · kernel/qemu · branches `kernel-snapshot`, `qemu-snapshot` (rejected).** QEMU publishes bus 0's configuration space over fw_cfg after the firmware (etc/spin/pci-config) and the kernel answers boot-time configuration reads from it until each dword is written. It works (`PCI: configuration space of 5 devices on bus 0`) and is worth -14 ms against the same kernel without it (lab 43514550646), but only 2-3 ms against 0005, which already removes most repeated reads: 209/217 against 211/222 (lab 43514884598). Not worth a QEMU patch and a 250-line kernel patch.

**11-13 · boot/qemu · branch `first-login-report` (this change).** v20261113.03's report read +11-18% on every row: the report times the serial console's prompt, and the getty drop-in removed in #250 left it Type=idle, waiting for the lane's units. The first login it does not time went 478/500 (v20261113.02, lab 43524334205) -> 381/410 ms (v20261113.03, lab 43524721490), two runs and not interleaved. The report now logs into the release over SSH too (`first_login`), and compare writes it first. The QEMU build asserts static-pie, full RELRO, an NX stack and stack canaries, which come from QEMU's and Alpine's defaults and no flag here. Measured and left out: one sshd for every connection (the vsock socket with Accept=no, an `sshd -D` from the first dispatch), first login 382/393 against 381/410 and a second 40 against 48 ms (lab 43524721490).

**11-13 · image · udev's coldplug keeps cpu (this change).** The coldplug filtered in `630fa2c` left out cpu, and a core attached before systemd starts - spin gives a workspace its cores while its own init is PID 1 - is onlined by that replay reaching `40-spin-hotadd.rules` and by nothing else: with v20261113.03 every workspace came up with one core whatever it was sized to (spin's kvm run 43527839765, four tests "still waiting for the guest to have its floor's two cores"). `task boot:hotadd` attaches a core before systemd and reads `online` after: `0` on v20261113.03's image, `0-1` with cpu in the filter. What cpu costs the first login was not measured; it is one device per core.


### Week 11: 2026-11-14 to 11-20

**11-19 · image · systemd from Ubuntu's source package, stage1 (this change).** The image installs Ubuntu's own systemd 259.9-0ubuntu0.1, rebuilt from its .dsc (versions.yaml `systemd-source`) by `image/systemd/Dockerfile`. The build uses Debian's `stage1` build profile (no BPF framework, audit, AppArmor, SELinux, OpenSSL, TPM2, homed, cryptsetup…) and links systemd-executor, udevd, systemctl and journalctl statically against libsystemd-shared. The packages are versioned `+spin1` and held. *Why:* PID 1 spent ~25 ms in the BPF verifier on socket-bind at every boot (#240, "measured, nothing done"), and restrict-fs failed to load under lockdown on every boot (#261). *Measured:*
- `boot:ssh-first` on the bench fleet, 20 boots of each, interleaved, first login p50 against the archive's systemd:
  - stage1: −47 (lab 44531842773), −38 (44537549305) and −47 ms (44538877783);
  - static over stage1: −10 and −1 ms, within noise. Accept → login was −7 ms in both runs.
- `boot:waits` on a laptop: PID 1's setup before the generators goes from 25.8 to 5.6 ms.
- `boot:restore-first` (new, below): unchanged. The guest's part, from running to login, is ~50 ms with either systemd (44533227886).

*Left out:* masking `systemd-udev-load-credentials` (−8, −6 ms) and `-Db_lto=true` (Ubuntu already builds with `-flto=auto`); see docs/lab.md, "Measured and rejected". The patch that would make restrict-fs load under lockdown, reading `s_magic` through BTF, loaded in this guest. It is for systemd upstream and is not needed here. *Cost:* a rebuild whenever Ubuntu publishes a systemd. The archive serves only the current source, so the build fails the day it moves, and `refresh:systemd` proposes the move.

**11-19 · boot · `boot:restore-first` (this change).** The goal's second number had had no probe in the tree since #246 closed. Every row of `boot:ssh-first` is booted cold, logged into and saved, then restored with its state evicted from the page cache (checked with mincore) and logged into again. Every cold boot runs before every restore. On the bench fleet a restore reaches the first login at 841/913 ms, ~790 of it before the guest runs, reading the state from the runner's gp3 volume (44533227886). #246 read 386 ms on 11-13; what moved is not known. On a c8i.metal-48xl it is 905/942 (44535167653), so the restore is the host's disk, not the virtualization.

**11-19 · lab · nested against metal.** `bench-metal` ran for the first time (spin-stack/ci#20 keeps it to c8i.metal-48xl, on-demand when spot has none; spin-machine#272 caps the job at 60 minutes). The same CPU (Xeon 6975P-C) on both, p50:
- first login: 378-392 ms nested (two runs) against 332 metal (44537498507), so **nesting costs ~50-60 ms**;
- `boot:firmware-stages`: QEMU start 30.5 against 28.1, the PCI walk 5.00 against 2.22 (173 exits, ~29 against 13 µs each), kernel and init 108.7 against 69.5 (44535161371, 44535156851).

An r7i.metal-24xl (Sapphire Rapids, slower per core) read 296 ms (44534028478), faster than the c8i.metal. Unexplained; repeat before quoting it. Every bench-fleet number in this log and in docs/lab.md is nested.

**11-19 · boot/image · where the first login's own time goes, and logind's socket early (this change).** `boot:login-lane` marks the guest's journal through the first login: accept → authenticated 55.5, PAM's session → logind's new session 29.0, → scope 4.3, → command 8.4 ms (v20261119.01, lab 44555905383). Its diagnostic rows (`SPIN_LANE_DIAG`, sshd at DEBUG1 and logind at debug, lab 44557162195) say what they are:
- **Before authentication.** PID 1 starting `sshd@` takes ~11 ms, and OpenSSH 10's three execs (`sshd` → `sshd-session` → `sshd-auth`) ~12. Loading PAM's modules cold takes 8.3 ms, against 0.9 at the second login. ML-KEM is 3.5.
- **The hop to logind** is logind not running yet. Its Varlink socket kept its default dependencies, so logind waited behind `sysinit.target` and udevd, ready ~230 ms into the boot (`boot:waits TARGET=systemd-logind.service`, lab 44557605832). The first login waited ~32 ms for it, the second 1.

The socket without default dependencies (`systemd-logind-varlink.socket.d/20-lane.conf`), 20 boots of each (labs 44558035927, 44558053261):
- the hop: 24.5/41.8 → 15.8/22.7;
- accept → login: 131/137 → 120/128;
- the first login: 332/355 → 328/337. The p50 moves little because logind starting sooner takes the CPU sshd was starting on.

Masking `modprobe@drm`, which logind is ordered after, added nothing (327/345). What is left of the login is CPU contention on 2 vCPUs, `user@1000` (38 ms) and the host key generation (21 ms) starting in the same window, rather than waits.

**11-19 · systemd/boot · where PID 1's gap between the unit load and the first job goes (experiment, tooling kept).** `image/systemd/experiments/pid1-marks.patch` makes PID 1 append its monotonic clock, its own CPU time, the units loaded and the time in `unit_load` after each step of `manager_startup` and `do_queue_default_job` to `/run/spin-pid1-marks`. `boot:waits` reads the file. `lab.yml`'s `systemd_experiment` builds the patched systemd on the runner and puts it into each overlay. Bench fleet, 10 boots (lab 44561762624), p50 ms:
- bus, varlink, coldplug, vacuum, ready: < 0.2 together;
- **loading what `default.target` reaches: 28.4 wall, 20.9 of it PID 1's CPU, 192 units** (~0.11 ms of CPU each; the rest is likely cold reads);
- building the transaction: 0.30;
- the first dispatch: 0.87.

The gap is unit loading, not the transaction that 11-13's single debug-logged boot named. 96 of the units a boot lists are inactive, loaded because another unit names them in `After=`/`Wants=`: `initrd-*`, `plymouth-*`, `modprobe@*`, hibernate, cryptsetup's targets, `firewalld` and `auditd` named by docker. Masking does not spare a load (a masked unit is still looked up and loaded as masked), which is why masking 11 units moved nothing (docs/lab.md, "Measured and rejected").

**11-19 · image/machine · the root mount's storage path (branch `ext4-mount`, not yet on the fleet).** The kernel gap `security_initcall_late_sync` → `EXT4-fs (vda): mounted` was 6.8 ms nested and 5.0 metal (`boot:initcalls`). Three changes, from reading Linux 7.3 and the image:
- the image without `orphan_file`, a mke2fs.conf default since 1.47.0: every mount, read-only included, reads it one block at a time (`ext4_init_orphan_info`, ~73 single-block reads with no readahead). Crash correctness is the classic orphan list's;
- `lazy_itable_init=0`: `mke2fs -d` left no group `ITABLE_ZEROED`, so each overlay's first read-write mount had `ext4lazyinit` zero every inode table in the first second of the boot. After one `--init /bin/sh` boot the overlay held 16.06 MiB of zero clusters, and none with the tables zeroed at build. The thread still starts, to prefetch block bitmaps (it does not with `no_prefetch_block_bitmaps`). The image is 256 KiB smaller;
- `rootfstype=ext4`: without it the root mount tries ext3, then ext2 (`EXT4_USE_FOR_EXT2`), on the superblock before ext4.

Laptop lead only, on a host at load ~30: the gap's p50 over three alternated rounds of `TestKernelInitcalls` (8-10 boots each) was 8.8/2.8/4.3 ms with the release, 4.1/3.2/2.9 with `rootfstype` alone, and under 2.4 (below the rows printed)/1.3/1.2 with all three. Not decided until `boot:ssh-first` and `boot:restore-first` run interleaved on the bench fleet.

**11-19 · kernel/qemu/machine · the kernel is entered as a bzImage (this change, functional: security).** KASLR was inactive under PVH. The PVH entry into `vmlinux` skips `arch/x86/boot/compressed`, where KASLR picks the random base and sets `KASLR_FLAG`, so `CONFIG_RANDOMIZE_BASE` and `RANDOMIZE_MEMORY` did nothing: every PVH boot had `_text` at `ffffffff81000000`. The machine now boots `kernel/bzImage` (LZ4, as the config already had: x86 has no `KERNEL_UNCOMPRESSED`), which qboot loads from fw_cfg itself (`boot_from_fwcfg`); `linuxboot_dma.bin`, the ROM QEMU adds for a bzImage and SeaBIOS runs, ships again. `vmlinux` and `pvh.bin` stay, for `--kernel vmlinux`, debugging and BTF. A tree without a bzImage (every release before this one) still boots its `vmlinux`, which boot:compat needs. Two builds, the second on a fresh BuildKit with no cache, gave the same bzImage (`ff9f0ec6…`) and vmlinux (`ff53d597…`). *Checked:* `boot:lockdown` now requires `_text` present and not `ffffffff81000000`, and no `nokaslr`; it fails on the PVH tree. Three bzImage boots: `_text` `ffffffffb5c00000`, `ffffffffa2a00000`, `ffffffff90800000`, the physical base moving too (`/proc/iomem`); the PVH `vmlinux`, three times `ffffffff81000000`. The fingerprint does not move (`2bd7da6b…` either way) and `boot:roms` passes: on q35 an option ROM is a fw_cfg file, not a RAM block. *Cost, laptop (i9-13900HK) - a lead only, the bench fleet decides:* `boot:firmware-stages` REPS=5, QEMU exec to the diagnostic init 105.6 vs 83.6 ms p50 summed (qboot copying the bzImage 2.3 ms; the rest the decompressor and the kernel after it); kernel start to init over 10 boots 34-92 ms, p50 ~51, against 34-37 for PVH, so the spread is the bzImage's and not yet explained. `boot:ssh-first` alternated A/B/A/B/A/B at REPS=5: first login 500/658/603 vs 504/587/764 ms p50 - the laptop's noise is larger than the difference. Kept whatever the bench fleet says: it is a security fix, and what it costs is the next thing to make cheaper.

Bench, nested (2026-11-19): it costs nothing measurable. `boot:initcalls` in the workspace shape, kernel start to "Freeing" 94.5 → 93.9 ms (lab 44577696958): the laptop's 34-92 ms spread did not reproduce. `boot:ssh-first` against the PVH release, sshd accepts 199 → 198 ms and first login 328 → 317 (lab 44583379559, 20 each); the login gain is in accept-to-login and is main's, not the bzImage's. The LZ4 decompression and the fw_cfg load do not show.

**11-19 · machine · one surface for spin: `machine.VM`, every machine started held (this change).** spin built a `Spec`, exec'd `Args()` and drove QMP with a client of its own, so what a checkpoint is (the migration capabilities both ends must agree on), how a disk reaches a device, and the `-S` before a CPU plug lived in spin. Now `machine.VM` (QEMU's: `machine.Attach`) is that conversation: `Boot`, `Resume` with `Load{Fast}`, `Loaded`, `Checkpoint` with a seal at pre-switchover, `Continue`, `AttachDisk`/`DetachDisk`, `SetCPUs`, `SetMemory`, `CPU`. An interface on purpose, so another VMM can be put behind it and compared. Every machine starts held, `-S` or `-incoming defer` (`Spec.Restore`): under `-incoming defer`, `cont` leaves QEMU in `inmigrate` (11.2.0-rc0), so one QEMU cannot wait for both. A disk is bound to its place (`Spec.Disks`, a raw node over an empty `null-co`) by serial, `blockdev-reopen` onto the chain handed over `add-fd`: the device's serial and read-only-ness are fixed at realize, and QEMU 11.2 has no `blockdev-replace`, so the raw node stays and a seal targets `machine.TopNode`. The prestart prototype measured it on the bench fleet: cold first login 313→291, restore 170→153 ms (lab 44573266140). Checkpoints are written in mapped-ram (`FormatMappedRAM`, recorded in `Saved`); a stream checkpoint from before still loads, in full (`boot:compat`). The host CPU's model name left the fingerprint: a checkpoint records the CPU its guest was shown and `CanResume` checks it (spin#1348). Fast load needs userfaultfd for QEMU's user and was not run here: an unprivileged QEMU refuses postcopy ("Userfaultfd not available"). *Status:* PR, measured on the bench fleet with `boot:restore-first` (`SPIN_RESTORE_FAST=1`) and `boot:prestart`.

**11-19 · machine · one rule for where a checkpoint resumes: `CanResume(Saved, Target)` (this change, functional).** After #293 a checkpoint recorded its format and CPU, but the machine it was of was still spin's `machine_identity` string, and nothing recorded the QEMU that wrote it: QEMU loads what an older QEMU of the same machine type wrote and promises nothing the other way, so during a rolling update a checkpoint from an updated host could be placed on one not yet updated with an identical fingerprint (spin-stack/spin#1348). `Saved` now carries `Fingerprint` and `VMM` ("qemu 11.1.90"), a machine says what it resumes (`VM.Target`: formats, fingerprint, VMM, CPU), and `CanResume` is the one rule a placement and `Resume` both ask: the format read, the same fingerprint, the same VMM at the writer's version or newer, the CPU shown again. `Args` names the machine by its fingerprint (`-name guest=spin-machine:<fingerprint>`, read back with `query-name`), which the guest does not see and `Contract` does not hash. Records from before a field was kept are not checked on it.

**11-19 · machine · a place is a filter node (this change, functional).** A place's node was raw over the bound disk's top image. spin's storage compacts a chain by rewriting a bound image's backing file (`change-backing-file`), which QEMU takes only on a root node whose chain, followed by backing and filter links, contains the image: on the root it answered "'slot0' and image file are not in the same chain", on the image "Need a root block node" (QEMU 11.2.0-rc0). A place is now a `throttle` node in a group with no limits: a filter, so the chain is seen through it, and pass-through. copy-on-read, compress and preallocate are filters too, and each changes what is written. Checked on QEMU 11.2.0-rc0: bind by `blockdev-reopen`, then `change-backing-file` through the place, then a seal under an overlay. The fingerprint does not move (`-object` is not in `Contract`). Its cost per I/O is the bench's to say.

**11-20 · machine · a named CPU model, what it costs (#222).** spin-machine can show the guest a named model the fleet shares (`CanResume`, `Models`, `Baseline`, #293); the question left was its cost. The bench host is nested, and its L0 hides features the Xeon 6 has (la57, TSX, fsrm): QEMU refuses GraniteRapids-v1, SapphireRapids-v1 and Icelake-Server-v2 there ("Host doesn't support requested features", lab 44657973748, cancelled after 50 minutes of boots each waiting out its login - `lab` now refuses such a tree at its first boot). The newest model it runs, chosen on the runner (`lab tree --cpu baseline`), is Cascadelake-Server-v5. Against `-cpu host`, 20 boots each, interleaved: the cold boot's first login 327/342 → 343/353 ms (+16.4, sshd accepts +12.6; lab 44664362024); the restore's 160/170 → 161/170 evicted and 167/188 → 165/203 cached (lab 44664603720). A fleet that takes a baseline model pays ~16 ms on every cold boot for checkpoints that cross host generations; on one generation `-cpu host` with `CanResume`'s feature check is free.

**11-20 · lab · density, the latency half (#273).** #278 answered the memory half (an idle workspace costs its host ~165-170 MiB). `lab` probe `density` boots the row's number of workspaces, logs into each, lets them settle 10 s, times one more's first login, and ends them all before the next measurement. On the bench (c8id.4xlarge, 16 vCPUs, 32 GiB, nested), 2 vCPU / 2 GiB workspaces, idle, 10 of each interleaved: with 0, 4, 8 and 16 already running the first login is 332/343, 328/340, 328/345 and 326/338 ms (lab 44667235868); each of the crowd took ~625 ms to boot and log into. Idle workspaces do not move the first number up to 16 on this host - the 25% point is past that. Not measured yet: a CPU-bound crowd (`load=cpu`), more than 16, and metal. A first run left every crowd up until the experiment ended (m.Cleanup runs then) and measured a host carrying a growing pile of machines (lab 44665519945); the probe ends its crowd itself.

**11-19 · kernel · branch `kernel-floor`, patches 0007-0011 and a config trim (this change).** The kernel was 78.0 ms nested against 45.0 on metal, same CPU (boot:initcalls, labs 44563496308, 44563512999). Five of its gaps, each a patch:
- 0007, ORC's lookup table by one merge pass instead of 59,650 binary searches: 3.5 ms on both hosts. Every block checked against the binary search, 0 differ.
- 0008, init's 1,989 ftrace records freed with one memmove: 2.6 ms on both hosts.
- 0009, the boot CPU polls for an AP by `local_clock()` while booting: 4.1 ms nested, under 1.4 on metal.
- 0010, no early-quirks PCI scan under KVM: 2.1 ms nested. pci=noearly also defers MMCONFIG; this skips only the scan.
- 0011, virtio-pci's `queue_select` written only when the queue changes: 84 → 22 writes.

Config: `X86_MSR`, `X86_CPUID` off (nothing in the image or spin opens `/dev/cpu`); `BLK_DEV_LOOP_MIN_COUNT=0` and `SERIAL_8250_RUNTIME_UARTS=1` (from `kernel-trim`). *Measured:* only on a laptop, not nested, 10 boots each interleaved, a lead: the ORC gap 4.59 → 0.93 and 4.45 → 0.72, the WAET gap 0.44 → 0.07 and 0.32 → 0.06, the ftrace gap 2.7 → under 1.1 ms. 0009 and 0011 cannot move there: the AP never waits and exits are cheap. The kernel's total read 61.5 → 55.7 in one run and 53.0 → 54.3 in the other on a host building QEMU at the time: noise. Not yet measured on the bench fleet or against the first login.

Bench, nested, workspace shape (2026-11-19, lab 44587603255, 20 boots each): kernel start to "Freeing" 96.4 → 86.6 ms (−9.8), in initcalls 61.7 → 58.7, outside them 35.1 → 27.8, where ORC and ftrace are. `virtio_pci_driver_init` 8.57 → 8.59: 0011 does not show even nested.

**11-19 · lab · the probes become `spin-machine-dev lab` (branch `dev-cli-lab`, this change).** Each probe was a Go test behind a `SPIN_*` switch of its own, and took a second kernel, a second QEMU, an edit of the image or a rebuilt systemd each through another environment variable (`SPIN_KERNEL_B`, `SPIN_KERNEL_C`, `SPIN_MACHINE_OUTPUT_B`, `SPIN_APPEND_B`, `SPIN_SYSTEMD_DEBS`), laid out by lab.yml's `variant=beside|instead`. Two failures in one day came from that shape: `boot:restore-first` read no `SPIN_MACHINE_OUTPUT_B` and measured the release's QEMU under the name of 11.2's (lab 44584214362), and a Kernel run from before the bzImage failed `lab:place` with no message (lab 44587254371). Now a tree is a whole machine with a manifest naming every part's source, refused when it is not whole, and a probe is handed trees (`internal/lab`, docs/cli.md). Ported: `ssh-first`, `restore-first` (its `fast` rows an option), `prestart`, `initcalls` (totals, the initcalls of interest per tree, each tree's slowest initcalls and largest gaps). The "as this branch" row is the tree edit `lane`. Not ported, and kept as `boot:` targets `lab check` runs on any tree: `firmware-stages`, `waits`. Deleted, each question answered and its answer in this log or beside it:
- `boot:login-lane` (`boot/login_lane_test.go`): where the first login's own time goes, on the guest's clock - the 11-19 entry above and docs/lab.md's cold-boot budget, lab 44555905383.
- `boot:density` (`boot/density_test.go`): one idle workspace's host memory, ~125 MiB - lab 44546329726.
- `boot:page-cache` (`boot/pagecache_test.go`): the page caches a machine has - docs/memory-and-disk.md, and docs/lab.md's "Measured and rejected".
- initcalls' extra passes, not ported: `SPIN_GRAPH` (function_graph under chosen functions, `boot/graph_test.go`), `SPIN_CPU_SHARE` (who had the CPU during one initcall, `boot/cpushare_test.go`), `SPIN_EARLY` (the lines before the first initcall) and `SPIN_FORMAT_CHECK` (an event's format after patch 0003). Each served one question, answered above; when another asks for one, it comes back from git (`boot/initcalls_test.go` and those two files before this change) as an option of the `initcalls` probe.
- `boot:ssh-first`, `boot:restore-first`, `boot:prestart`, `boot:initcalls` are now lab runs over `OUTPUT_DIR`, the same targets.

Checked against the probe it replaced on the bench fleet, `lab dispatch ssh-first`, the release and the release with the lane, 20 each (lab 44596983249): sshd accepts 215/228 and 216/229, first login 349/366 and 339/353 - the old probe's rows read 215/223 and 216/223, 348/360 and 335/345 (lab 44585268429). A release ships no spin-machine; the old targets' `:tools` built this checkout's into the release tree, and a tree does the same, named in its manifest (the first dispatch, 44596065601, failed on it).

**11-19 · lab · the release report is a ladder (this change).** The report measured each release alone, through the feature matrix (eight combinations of memory, vsock and hotplug, and the image's boot variants, each timed to the serial console's prompt), and compared its report.json with the previous release's, taken on another day on another instance. For v20261119.02 it said sshd accepted 7.3% later than v20261119.01 (202.6 → 217.3 ms); the two interleaved on one host said 199 → 198 (lab 44583379559). And the goal's second number, a restore to the first login, was not in it. Now `spin-machine-dev lab ladder` boots the newest five releases on one bench host, each with the spin-machine of its own commit, interleaved: a cold boot and a restore to the first login, both into a QEMU started ahead, and save, state, the kernel and QEMU's RSS as diagnostics. report.yml runs it after each release (ladder.json beside it, the table in its notes) and weekly. A release whose spin-machine cannot run a probe is a dash with the reason. boot:compat is now `lab compat`: every release since the fingerprint last moved, fetched against its sha256 and signature, and CanResume held to QEMU's one-way promise against each one's QEMU. The feature matrix is deleted: its last answer is v20261119.02's report.json asset and the "Feature matrix" section of its notes (no row moved beyond 5%, every row 344-389 ms usable), and git has the code (boot/report.go, boot/report_test.go).

**11-20 · lab/machine · multifd channels on the checkpoint, the no-systemd floor, and spin's shape (#320, lab 44750713447, lab 44750322384).** The two experiments of the methodology review, and the first runs in spin's shape (#319). *multifd:* `spin-machine save` and `restore` move a checkpoint's memory over `--channels` threads (QEMU's multifd on mapped-ram); with QEMU started ahead, in the ladder's shape, the restore's first login reads 140.9/152.0 at one channel, 140.5/178.0 at two (inside the noise), 101.5/117.4 at eight and 93.5/114.9 at sixteen, Δp50 −47.4 [−54.4, −39.4]: the first goal number under 100 ms. In spin's shape the p50 falls 149 → 114 but ~20% of restores with channels (56 of 280) stall ~400 ms between running and the login, against 5% at one, and the p95 goes 254 → 435. *Floor:* `lab tree --edit floor` boots sshd with no systemd before it, a bound only: the ladder's shape 322 → 219 (Δ −103.5 [−108.6, −100.5]), spin's 445 → 240 (Δ −204.8); sshd accepts at 173 and 189 ms with nothing before it. *Spin's shape:* the cold first login is 445 ms against 322, Δ +122.9 [+116.6, +129.8]; accept to login 214 against 114. *Later the same day:* the stall was a guest left at 128 MiB. At 128 MiB MGLRU's `min_ttl_ms` (armed by tmpfiles at boot) forbids eviction for the first second, so the guest OOM-kills sshd-session, logind and systemd units, and the client retries (lab 44784196287: kills up to 27, failed logins up to 14 a login); a second, idle ~400 ms stall in states saved with 8 channels sat inside sshd's key authentication. spin sizes every workspace to 2048 MiB right after its start (spin's `vmm.resize`), and sized so (`plugged=2048`) neither appears: no kills, every row's gap p95 under 69 ms (lab 44788801555). Restored into a QEMU started ahead, spin's shape and login: 150/156 at one channel, 103/108 at 8, 92.7/111 at 16, Δp50 −57.4 [−59.4, −54.3], and 16 against 8 −11.0 [−13.0, −7.9] (lab 44789688221). *Status:* channels kept; the default count is decided by restores run side by side; the floor edit kept as the bound's instrument.

---

**11-20 · CI · the builders are images, and the apk locks are gone (this change).** The daily apk-lock refresh (`c6c29b5`) kept the locks buildable, at a price: Alpine's mirror serves only the newest build of each package, so a lock moved every two or three days (11-09, two on 11-12, 11-19, #308 on 11-20), and the lock is the first layer of QEMU's build, so each move compiled QEMU cold on its pull request (17-21 min) and again on main (12 min), the export being main's alone - #308 was libxml2 r2 → r3, which nothing links. QEMU's and e2fsprogs' builders are now images, Alpine with the part's apk.list installed (`builder/Dockerfile`), published to ghcr and pinned by digest (`qemu-builder`, `e2fsprogs-builder`). An image keeps its packages after the mirror drops them, so nothing forces a move: refresh.yml makes them again weekly and proposes the ones whose packages moved, with the list of what did; the tag is a hash of the Alpine, the list and every package installed, so it never names two images. A part's build stops at its first step when the tree's apk.list is not the builder's. In place.

**11-20 · CI · qemu.yml, kernel.yml and image.yml are parts.yml (this change).** Three workflows of the same shape - a `changed` job and a build, path-filtered - and none of them a required check, since a path-filtered workflow does not report on a change outside its paths: a pull request could merge with its QEMU build red (#309's was not blocking). One workflow now, with no path filter: `changed` decides for each part, the builds run when it says so, and `parts` (`spin-machine-dev needs`) fails when one that ran did not pass - one check a ruleset can require. A change that reaches all three runs one `changed` where it ran three (each a billed minute). Over the 200 runs before it (gha-optimize, 2026-11-20): build p50 1m01s cached, cold 10-22 min; `changed` 20 s. In place.

**11-20 · lab · each release's numbers in the repository, measured alone (this change).** The ladder (11-19) made every release re-measure the newest five on the bench (~13 min of the `report` job), uploaded a 31 MB ladder.json as a release asset and ended the release's notes with a table: numbers no one could share or read back but by downloading the asset, and the same five releases booted again with every release. Now the `report` job measures the release alone (`spin-machine-dev releases measure`, a ladder of one), keeps its p50/p95s as `docs/releases/RELEASE.json` with its host, date and run (whose artifact keeps every sample, 90 days), writes `docs/releases.md` from every record - a Mermaid chart of each goal number release by release, the tables, where each was measured - and proposes both as a pull request as the release app. One release where it booted five; nothing goes on the release. *Why alone, not beside the release before:* every record is then read the same way, one run each, and the page says plainly what that costs: the 11-19 report said v20261119.02 accepted SSH 7.3% later than v20261119.01, and the two interleaved said they did not (labs 44583379559, 44600043625), so a Δ on the page is a lead and a decision is `lab ladder` over the two, interleaved. `releases compare A B` prints two records with each one's host and date, and that command. Past releases are `releases missing`, measured by `report.yml` dispatched with `versions` (each alone, one after another, proposed together). Seeded from the two ladder.json assets there were, taking only each one's newest release, the one that run was for: v20261120.01 (cold 327/337, restore 159/164, run 44641240861) and v20261120.02 (cold 323/329, restore 168/180, run 44727451080); the older releases inside them were measured interleaved, and are left for `releases measure`. `lab publish` and the release-notes section are deleted.

**11-20 · lab · the records' chart is an SVG, and the records a web page (this change).** The page's two Mermaid `xychart-beta` blocks drew one p50 line each and could not draw a gap: a release without a number was left off that chart's axis, so the cold and the restore charts had different x axes, and neither showed the p95. `spin-machine-dev releases doc` now writes `docs/releases.svg` beside the page, from the same code (`lab.Chart`, hand-written, no dependency): both goal numbers on one axis, each a p50 line over a band to its p95, broken where a release has not the number, the 100 ms goal dashed, a tooltip on every point. One SVG is shown on GitHub's light page and its dark one, and an image cannot follow the theme, so its two colours were chosen to clear 3:1 on both (#2a78d6, #d95926; dataviz's palette checks against #ffffff and #0d1117) with text in a mid grey (3.9:1 and 4.8:1). `releases site` writes the same page as one HTML file with the chart inline, so its tooltips show, and each record beside it; `pages.yml` publishes it at https://spin-stack.github.io/spin-machine/ from every merge that changes a record. The tables are one list of rows that the Markdown and the HTML both write, so the two cannot say different things. Each release in "Where each was measured" now links its record.

**11-20 · lab/docs · the methodology review, and docs/lab.md (this change).** Two adversarial reviews of how boot time is measured, each finding re-run on the ladders of runs 44641240861 and 44727451080 (five releases interleaved, 20 boots each). *Found:*
- No per-phase budget existed, so nothing said whether 100 ms is reachable. Split by phase, the cold boot spends ~125-135 ms in QEMU, qboot and the kernel before PID 1 runs; the no-systemd floor (209 ms) was measured once, on 11-13, on an 87 ms kernel.
- The goal probes boot a shape and a login path spin does not: no CPU or memory ceilings, where spin boots 1 CPU and 128 MiB with ceilings of 16 and 8 GiB (the kernel 86.6 ms against 62, lab 44587603255), and an `sshd-vsock.socket` that spin turns off.
- Effects inside the noise were accepted. Two releases booting the same guest read Δp50 of the first login in [−8.7, +4.8] ms (bootstrap, n=20), yet the logind socket's −4 and `driver_async_probe`'s −5 were kept while −8 and −6 were rejected as noise.
- `docs/releases.md`'s Δ is drift between runs: v20261120.01's restore login read 158.9 and 167.0 in the two runs.
- The bzImage's cost was recorded as zero by a clock that starts after decompression: sshd accepted ~10-13 ms later from v20261119.01 to .02 while "Freeing" went 76 → 62.5.
- `restore-first`'s `read` reads the mapped-ram file's holes (2 GiB for 155 MiB of data), and its `up` times the monitor socket.

*Decided by the maintainer:* the kernel, systemd, QEMU and qboot may all be patched without limit; systemd is a must, so a no-systemd init is only ever a bound. *Done:* `docs/lab.md` holds where both numbers stand, the per-phase budget, the method with one acceptance rule (a gain when the 95% interval of Δp50 excludes 0), the kept and rejected tables, the measurement pitfalls, and what is next. This log's former "Where we stand", "Measured and rejected", "Measurement pitfalls found" and "Open questions" moved there. *Next:* the probes in spin's shape (branch `lab-measure`); multifd channels on the restore and the no-systemd floor on today's kernel, both on the bench.
**11-20 · lab/image · spin's login, spin's memory, and spin no longer lingers (this change).** What spin expects of the guest's SSH server, read in spin-stack/spin at 1bd3414: sshd on 127.0.0.1:22 reached through its supervisor's vsock forwarder (a Go dialer, so TCP_NODELAY), ssh.service started by the supervisor's `systemctl reload-or-restart` when it installs the user CA and before it lets SSH through, a certificate for **root** (the CLI, the exec server and the terminal agent all log in as root), sessions in `user.slice` (spin writes its memory limits there, internal/guest/sessionmem), and a workspace sized to 2048 MiB through virtio-mem right after its start, before the guest's handoff (vmm.go, resize). The goal probes did none of it: they logged in as spin through systemd's vsock socket, an `sshd@` a connection, and left spin's shape at 128 MiB. *Found, on a laptop (leads, not nested):*
- At 128 MiB the guest's OOM killer runs through the boot and the logins: user@1000, logind, `sshd@`, `ssh.service` and session scopes killed, ssh.service hitting its start limit, logins lost and retried, in every boot of spin's shape. Sized to 2048 MiB right after its start (67-79 ms to plug) nothing is killed. Every number in spin's shape so far measured a guest spin never runs; the ~400 ms stalls between running and the login in ~20% of multifd restores (lab 44750713447) look like it.
- spin's path costs less than the lane's: accept to the first login 67-70 against 142-155 ms, a warm login 27-30 against 33-37 (6 boots each, 2048 MiB). socat as the forwarder without `nodelay` read 69 ms warm against 26 with it: Nagle against the delayed ACK, which spin's Go forwarder does not have.
- On spin's path, sessions of class `user-early-light` and nobody logged in as spin, the lingering user@1000 is a systemd --user started in every boot for nobody: without it accept to login 40 against 70 ms and the first login 294 against 316 (6 each); through `lab run`, 8 each, first login −13.1 [−33.3, +5.6], accept to login −7.1 [−28.5, −2.4], the console prompt −37.8 [−63.2, −22.4]. The lane's login as spin did not move (283 against 281).
- Measured and not kept, each a lead on spin's path: `UsePAM no` refuses root, whose password is locked, and would take the session and `user.slice` with it; pam_systemd removed, −15 ms on the first login and −5 warm, but no session, no `XDG_RUNTIME_DIR` and no `user.slice`, where spin holds a person's sessions below the machine's memory; the sshd PAM stack cut to pam_nologin, pam_unix, pam_loginuid and pam_systemd, nothing (70 against 67). Exec costs, warm: `sshd-auth` 1.7 ms a start against 0.56 for `true` (16 libraries, 7 of them Kerberos'), `sshd-session` 0.9, `sshd -t` 2.2; a rebuilt OpenSSH without GSSAPI has a ceiling of ~1-2 ms a login, under the bench's noise.

*Changed:* `lab`'s shape options `plugged=MIB` (the guest sized right after its start, as spin does) and `login=spin` (root, through a socat forwarder to ssh.service, systemd's vsock socket masked), both off by default; the image no longer lingers spin, and `--edit nolinger` puts that into a release's overlay. *Measured on the bench* (spin's shape, `plugged=2048`, `login=spin`, 20 each, interleaved): the first cold login 532/551 → 496/514, Δp50 −36.1 [−45.8, −23.6], accept to login −26.9 [−39.8, −18.9], the console prompt −89.5 [−103.6, −73.9], QEMU's RSS −7.9 MiB (lab 44785179901); `prestart` cold −27.4 [−32.7, −19.9], prestarted −25.8 [−35.2, −15.1], both restore rows within the noise (lab 44785565313). *Status:* kept.

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

**Lesson:** the 09-12 decision optimised the console prompt, a metric nobody waits for, and paid for it in the first login, which was not measured until 11-12. Name the metric that matters before optimising; the first SSH login probe arrived two months after the change it would have rejected.

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

**Lesson:** the product's path (template restore, then checkpoint resume) changed outside this repository, and probes were built and deleted around it within days. Restore of a checkpoint was not measured at all between 09-30 and 11-13. The mapped-ram decision now has a fleet result; do not reopen it without a new reason, for example a multi-VM concurrent restore or larger RAM, which #242's checklist names as unmeasured.

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

Each step moved every fingerprint. Comments that said "a QEMU or kernel bump strands checkpoints" survived until `e452afc` (11-12). The verdict tool was blind to `machine/` changes until #98.

**Lesson:** rule 1's meaning changed on 10-13. Check claims about the fingerprint against `Spec.Contract` and `boot:compat`, not against older comments, and treat any proposal that touches `Spec.Contract` (virtio-rng removal, mapped-ram) as a fleet-wide event.

### 2.7 Console unit (spin-machine-console.service)
Sequence: created 09-07 (`24c8a06`); made redundant by udev 09-10 (`064c4f4`); measured 340 ms worse as a swap; TERM=dumb fixed it, 09-26 (`7b08e5a`); "viable", but the exposure question was left open; deleted 11-12 (`3a7864d`).

**Lesson:** two months of a unit nothing enabled. Delete as soon as nothing references a unit.

### 2.8 Debug init and diagnostic initrds
`cmd/spin-machine-init` was created 09-07 and deleted 09-10 (`8eeb01c`). Diagnostic initrds came back as `boot/probeinit` (09-29, `a81b6b5`), now allowed by CLAUDE.md as isolated diagnostics.

**Lesson:** the rule ("no init in a release") was clarified rather than broken. CLAUDE.md's "Diagnostic input is different" paragraph now records this.

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

This is the cycle CLAUDE.md asks for ("remove … once it no longer has an active question"). The cost is that the answers live only in commit messages and comments.

**Lesson:** this log, or `docs/memory-and-disk.md`-style records, is where an answered probe's result should land. Before writing a probe, check docs/lab.md's "Measured and rejected" and the row comments in `boot/harness_test.go` and `boot/ssh_first_test.go`.

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
`6e7ba47` (11-03), `8d495c5` (11-09), release run 43157106109 / `ec2c7fb` (11-12); fixed by a daily refresh (`c6c29b5`), which then compiled QEMU cold twice every move; fixed structurally on 11-20 by pinning builder images instead of packages.

**Lesson:** a mirror that serves only the newest build cannot be pinned; pin something that keeps its contents - an image - and move it when the move is wanted.
