# The guest kernel.
#
# The build depends on no source but its own: a version and a config file.
#
# What is NOT here: an initrd. What runs inside a guest belongs to whoever runs guests —
# this repository builds the machine and boots it far enough to prove it works.
#
# The config is not stable furniture. Symbols get added and removed for measured reasons —
# a device a restored VM needs to reseed from, a crypto driver worth milliseconds of boot —
# and every one of those changes is every guest's kernel at its next boot.
#
# The toolchain is pinned, by the base image's digest and by the date of the Debian archive
# its packages come from. A different compiler, assembler or pahole is a different vmlinux,
# so it changes when this file says so, and not when Debian publishes a point release.
#
# Debian trixie: gcc 14.2, binutils 2.44, pahole 1.30, since 2026-09-14 (from bookworm: gcc
# 12.2, binutils 2.40, pahole 1.24).
#
# Both are versions.yaml's `debian` and `debian-snapshot`, handed in by `spin-machine-dev build kernel`
# with no default here. To move them: `spin-machine-dev bump debian` and `spin-machine-dev bump debian-snapshot`,
# together, and the boot measured again (`task boot:ssh-first`).
ARG DEBIAN_IMAGE
ARG DEBIAN_SNAPSHOT

FROM ${DEBIAN_IMAGE} AS kernel-build-base

ARG DEBIAN_SNAPSHOT
ENV DEBIAN_FRONTEND=noninteractive

RUN echo 'Binary::apt::APT::Keep-Downloaded-Packages "true";' > /etc/apt/apt.conf.d/keep-cache

# The archive as it stood on DEBIAN_SNAPSHOT, instead of the image's live mirrors. Valid-Until
# is not checked because a snapshot's Release files expire by design; the signatures still are,
# and they are what makes plain http enough — and it has to be http, since the image carries no
# CA certificates until ca-certificates is installed from this very archive.
RUN rm -f /etc/apt/sources.list.d/debian.sources && cat > /etc/apt/sources.list.d/snapshot.sources <<EOF
Types: deb
URIs: http://snapshot.debian.org/archive/debian/${DEBIAN_SNAPSHOT}
Suites: trixie trixie-updates
Components: main
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg
Check-Valid-Until: no

Types: deb
URIs: http://snapshot.debian.org/archive/debian-security/${DEBIAN_SNAPSHOT}
Suites: trixie-security
Components: main
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg
Check-Valid-Until: no
EOF

RUN --mount=type=cache,sharing=locked,id=kernel-aptlib,target=/var/lib/apt \
    --mount=type=cache,sharing=locked,id=kernel-aptcache,target=/var/cache/apt \
    apt-get update && apt-get install -y --no-install-recommends \
        build-essential libncurses-dev flex bison libssl-dev libelf-dev bc cpio git \
        wget xz-utils curl ca-certificates file \
        # The four that CONFIG_DEBUG_INFO_BTF brings with it, and none of them is obvious
        # from the symbol's name:
        #
        #   dwarves      provides pahole, which turns the DWARF the compiler emits into the
        #                .BTF section. Missing, the build fails near the end with
        #                `BTF: .tmp_vmlinux.btf: pahole (pahole) is not available`.
        #   python3      BTF makes the kernel build tools/bpf/resolve_btfids, which builds
        #                a copy of libbpf, which generates bpf_helper_defs.h with a python
        #                script. Missing, that fails as exit 127 — a "command not found"
        #                three Makefiles deep, naming a header.
        #   zlib1g-dev,
        #   pkg-config   what that same libbpf links and finds itself with.
        dwarves python3 zlib1g-dev pkg-config \
        # apparmor and procps are not kernel build dependencies: they are what
        # check-docker-config.sh below shells out to. Without apparmor_parser it reports
        # "apparmor: enabled, but apparmor_parser missing" and exits non-zero, failing a
        # configuration that is correct; without sysctl it prints three "sysctl: not found"
        # lines while inspecting the bridge driver.
        apparmor procps

# The version, and the tarball it is made of by content, from versions.yaml. See the same
# argument in qemu/Dockerfile: https says who sent it, not what they sent. It is also what
# makes the SOURCES file in a release true.
ARG KERNEL_VERSION
ARG KERNEL_SHA256
# The series, major.minor, which names the configuration: a stable release within it builds
# from the same file (`spin-machine-dev build kernel` derives it from the version).
ARG KERNEL_SERIES
ARG KERNEL_ARCH="x86_64"
ARG KERNEL_NPROC="16"

# moby's check-config.sh, with the SELinux and AppArmor checks removed: they are host
# policy, not kernel features this guest needs, and they fail a config that is correct.
#
# At the commit versions.yaml pins, checked by content. It was master, fetched at build time:
# what the build checked the config against was whatever moby had merged that day.
ARG MOBY_CHECK_CONFIG_VERSION
ARG MOBY_CHECK_CONFIG_SHA256
RUN curl -o /usr/local/bin/check-docker-config.sh -fsSL "https://raw.githubusercontent.com/moby/moby/${MOBY_CHECK_CONFIG_VERSION}/contrib/check-config.sh" \
  && echo "${MOBY_CHECK_CONFIG_SHA256}  /usr/local/bin/check-docker-config.sh" | sha256sum -c - \
  && chmod +x /usr/local/bin/check-docker-config.sh \
  && sed -i '/IP_VS_RR/s/\\$//; /SECURITY_SELINUX/d; /SECURITY_APPARMOR/d' /usr/local/bin/check-docker-config.sh

WORKDIR /usr/src

RUN --mount=type=cache,sharing=locked,id=kernel-src-${KERNEL_VERSION},target=/var/cache/kernel \
    KERNEL_MAJOR="$(echo "${KERNEL_VERSION}" | cut -d. -f1)" && \
    # A release candidate is on git.kernel.org as the gzip tarball torvalds tags, not on the
    # CDN; its checksum is pinned the same way.
    case "${KERNEL_VERSION}" in \
        *-rc*) url="https://git.kernel.org/torvalds/t/linux-${KERNEL_VERSION}.tar.gz" ;; \
        *) url="https://cdn.kernel.org/pub/linux/kernel/v${KERNEL_MAJOR}.x/linux-${KERNEL_VERSION}.tar.xz" ;; \
    esac && \
    tarball="/var/cache/kernel/${url##*/}" && \
    if [ ! -f "$tarball" ]; then \
        wget -O "$tarball" "$url"; \
    fi && \
    # Every build, not only after a download: the cache mount outlives the build that
    # filled it.
    echo "${KERNEL_SHA256}  $tarball" | sha256sum -c - && \
    tar -xf "$tarball" -C /usr/src && \
    mv linux-${KERNEL_VERSION} linux

# The patches this kernel carries, each of which says what it changes, why and what it was
# measured to be worth. Applied with no fuzz: a patch that no longer applies exactly after a
# kernel bump is one somebody has to read again, not one to apply near where it used to go.
# The release ships them beside the binary (hack/release), because a changed GPL source is
# distributed with its changes.
#
# They are written as kernel patches, to Documentation/process/coding-style.rst and
# submitting-patches.rst (kernel/README.md, "Changing it"): made with
# git format-patch against the pinned tarball, a subsystem-prefixed subject, a changelog that
# states the problem, the change, what it was measured to be worth and what it costs, and
# the pinned tree's scripts/checkpatch.pl --strict clean. A tool-assisted patch says so with
# Assisted-by: (coding-assistants.rst) and carries no Signed-off-by a tool added: that
# certifies the DCO, which only the human who takes the patch on can do.
COPY kernel/patches/ /usr/src/patches/

# That claim, checked, with the checkpatch.pl, spelling.txt and const_structs.checkpatch of the
# tree the patches apply to, already here and pinned with it: nothing fetched, so nothing to
# rate-limit a build (raw.githubusercontent.com answered one with 429, job 137831193347). Four
# things are left out, each by name. --no-signoff and the Assisted-by: trailer removed before the
# check, because checkpatch knows no Assisted-by: and the missing Signed-off-by is the rule
# above. UNKNOWN_COMMIT_ID, because a tarball has no history to find a cited commit in.
# FILE_PATH_CHANGES, "does MAINTAINERS need updating?" for a patch that adds a file (0016,
# 0017), because no patch here goes upstream until it has run in production, and MAINTAINERS
# is upstream's to change then. Each
# patch must come out "has no obvious style problems", read from the output rather than trusted
# from the exit status.
RUN cd /usr/src/linux && \
    failed=0 && \
    for p in /usr/src/patches/*.patch; do \
        out=$(grep -v '^Assisted-by: ' "$p" | perl scripts/checkpatch.pl --strict --no-signoff --ignore UNKNOWN_COMMIT_ID,FILE_PATH_CHANGES \
            --root=/usr/src/linux --summary-file --patch - 2>&1) || true; \
        case "$out" in \
            *"has no obvious style problems"*) echo "checkpatch: ${p##*/}: clean" ;; \
            *) echo "ERROR: checkpatch --strict: ${p##*/}:"; echo "$out"; failed=1 ;; \
        esac; \
    done && \
    [ "$failed" = 0 ]

RUN cd /usr/src/linux && \
    for p in /usr/src/patches/*.patch; do \
        patch -p1 --forward --fuzz=0 < "$p" || { echo "ERROR: $p does not apply to ${KERNEL_VERSION}"; exit 1; }; \
    done

COPY kernel/config-${KERNEL_SERIES}-${KERNEL_ARCH} /usr/src/linux/.config

# confidentiality for every kernel spin boots. none only for the lab's traceable kernel, built
# beside a release from this config with that one option swapped and never in it
# (kernel/README.md, "Lockdown and the BPF LSM"): spin-machine-dev release traceable-kernel
# holds its config to the release kernel's, less that option.
ARG KERNEL_LOCKDOWN

# Every check below is a symbol that olddefconfig can silently drop and whose absence
# presents as something other than a missing kernel option.
RUN <<EOT
    set -e
    cd /usr/src/linux

    case "${KERNEL_LOCKDOWN}" in
    confidentiality) ;;
    none) sed -i 's/^CONFIG_LOCK_DOWN_KERNEL_FORCE_CONFIDENTIALITY=y$/CONFIG_LOCK_DOWN_KERNEL_FORCE_NONE=y/' .config ;;
    *) echo "ERROR: KERNEL_LOCKDOWN is confidentiality or none, not '${KERNEL_LOCKDOWN}'"; exit 1 ;;
    esac
    make ARCH=${KERNEL_ARCH} olddefconfig

    echo "Verifying critical kernel configs after olddefconfig..."
    grep -q "CONFIG_VIRTIO_NET=y" .config || (echo "ERROR: CONFIG_VIRTIO_NET not enabled after olddefconfig!" ; grep VIRTIO_NET .config ; exit 1)
    grep -q "CONFIG_VIRTIO_PCI=y" .config || (echo "ERROR: CONFIG_VIRTIO_PCI not enabled!" ; exit 1)
    grep -q "CONFIG_NET_CLS_ACT=y" .config || (echo "ERROR: CONFIG_NET_CLS_ACT not enabled!" ; exit 1)

    # Resizing a VM's memory while it runs, which is virtio-mem and no longer ACPI DIMM
    # hotplug — the machine offers no memory slots to plug one into.
    #
    # VIRTIO_MEM is the driver; the other two are the core it plugs and unplugs through.
    # Without them the host can grow the device, QEMU will accept it,
    # query-memory-devices will show it, and the guest will never see a byte of it.
    #
    # Shrinking is the half that made this worth changing: unplugging a whole pc-dimm
    # needs the guest to offline an entire memory block and usually fails on one
    # unmovable page, while virtio-mem returns memory in small blocks and succeeds.
    grep -q "CONFIG_VIRTIO_MEM=y" .config || (echo "ERROR: CONFIG_VIRTIO_MEM not enabled (a VM cannot be resized while it runs)!" ; exit 1)
    grep -q "CONFIG_MEMORY_HOTPLUG=y" .config || (echo "ERROR: CONFIG_MEMORY_HOTPLUG not enabled (memory hotplug cannot reach the guest)!" ; exit 1)
    grep -q "CONFIG_MEMORY_HOTREMOVE=y" .config || (echo "ERROR: CONFIG_MEMORY_HOTREMOVE not enabled (unplug cannot work)!" ; exit 1)

    # An lz4 initramfs. lz4 is among the fastest formats to decompress and unpacking the
    # archive is one of the largest single items in kernel boot; without this the kernel
    # does not recognise such an archive at all and the VM does not boot.
    grep -q "CONFIG_RD_LZ4=y" .config || (echo "ERROR: CONFIG_RD_LZ4 not enabled (the lz4 initramfs cannot be unpacked)!" ; exit 1)

    # Pressure stall information, /proc/pressure/{cpu,memory,io}: how long tasks in the guest
    # waited for each, which is the question when a busy VM stops answering, and what
    # systemd-oomd acts on. Off, and on by default: a psi=1 on the command line would be one
    # more thing a machine can be started without. It came in off with the first import and
    # nothing said why; turned on 2026-09-28 with 7.2.8, one new machine for both.
    grep -q "CONFIG_PSI=y" .config || (echo "ERROR: CONFIG_PSI not enabled (no /proc/pressure in the guest)!" ; exit 1)
    ! grep -q "CONFIG_PSI_DEFAULT_DISABLED=y" .config || (echo "ERROR: CONFIG_PSI_DEFAULT_DISABLED is set (/proc/pressure is off without psi=1)!" ; exit 1)

    # What keeps a loaded guest answering, and says why when it does not. Turned on 2026-09-28
    # with 7.2.8, one new machine with PSI; none had a recorded reason to be off.
    #
    #   LRU_GEN(_ENABLED)       MGLRU, and with min_ttl_ms (the image sets it, see
    #                           etc/tmpfiles.d/lru-gen.conf) an OOM kill where the old LRU thrashes:
    #                           evicting and re-reading the code of running processes while page
    #                           cache still counts as reclaimable. Measured 2026-09-28, 1 GiB guest,
    #                           memory taken slowly while libraries are re-read, 3 boots: the worst
    #                           SSH login 611-1785 ms without it, 105-106 ms with it, and the
    #                           process killed ~20% sooner (the memory-policy probe, 337c08d).
    #
    #                           Not ZRAM: in the same test it gave the runaway process more room
    #                           and it was killed later (20 s against 14), with the worst login
    #                           at 3.8 s. Compressed swap is for cold memory, which this was not.
    #   PARAVIRT_SPINLOCKS      a vCPU waiting on a lock whose holder the host preempted yields
    #                           to the host instead of spinning: the stall of an overcommitted host.
    #   PARAVIRT_TIME_ACCOUNTING
    #                           steal time accounted as such, so the guest can see the host kept
    #                           it off a CPU (top's "st") rather than calling it its own load.
    #   DEFAULT_FQ_CODEL        fq_codel, not pfifo_fast, on every interface: an SSH packet does
    #                           not queue behind a saturating upload.
    #   DETECT_HUNG_TASK, SOFTLOCKUP_DETECTOR, WQ_WATCHDOG
    #                           a stuck task, CPU or workqueue is reported on the console. They
    #                           report and do not panic.
    for opt in LRU_GEN LRU_GEN_ENABLED PARAVIRT_SPINLOCKS PARAVIRT_TIME_ACCOUNTING \
            NET_SCH_FQ_CODEL DEFAULT_FQ_CODEL DETECT_HUNG_TASK SOFTLOCKUP_DETECTOR WQ_WATCHDOG; do
        grep -q "^CONFIG_${opt}=y" .config || { echo "ERROR: CONFIG_${opt} is not enabled after olddefconfig!"; exit 1; }
    done
    grep -q '^CONFIG_DEFAULT_NET_SCH="fq_codel"' .config || { echo "ERROR: the default qdisc is not fq_codel!"; exit 1; }
    for opt in BOOTPARAM_HUNG_TASK_PANIC BOOTPARAM_SOFTLOCKUP_PANIC; do
        ! grep -q "^CONFIG_${opt}=y" .config || { echo "ERROR: CONFIG_${opt} would turn a report into a panic!"; exit 1; }
    done

    # The filesystems the base image and the guest's overlay are made of. ext4 is what
    # image/ produces and what a writable layer is formatted as; overlayfs stacks them. A
    # base image in a filesystem this kernel cannot mount is a VM that boots and finds no
    # root, so changing image/mkosi.conf to xfs or squashfs starts here, not there.
    grep -q "CONFIG_EXT4_FS=y" .config || (echo "ERROR: CONFIG_EXT4_FS not enabled (the base image cannot be mounted)!" ; exit 1)

    # The guest defends spin's security from its own root (spin's F2b): the supervisor loads BPF
    # LSM programs that refuse an agent what its mark does not cover and refuse anybody
    # detaching them, and lockdown keeps root from reading or writing the kernel those
    # programs and the supervisor's keys live in. Without any one of these a root that wants
    # to, gets around them in silence. DEVMEM and PROC_KCORE are the two reads of the kernel's
    # memory, and DEVPORT the I/O ports, gone rather than left to lockdown alone. 7.4 made
    # DEVPORT its own option, on by default, where 7.3 built it only with DEVMEM.
    for opt in SECURITY SECURITYFS SECURITY_NETWORK SECURITY_PATH BPF_LSM SECURITY_LOCKDOWN_LSM \
            SECURITY_LOCKDOWN_LSM_EARLY "LOCK_DOWN_KERNEL_FORCE_$(echo "${KERNEL_LOCKDOWN}" | tr a-z A-Z)"; do
        grep -q "^CONFIG_${opt}=y" .config || { echo "ERROR: CONFIG_${opt} is not enabled after olddefconfig!"; exit 1; }
    done
    grep -q '^CONFIG_LSM="lockdown,bpf"' .config || { echo "ERROR: the LSMs are not lockdown and bpf!"; grep '^CONFIG_LSM=' .config; exit 1; }
    for opt in DEVMEM DEVPORT PROC_KCORE MODULES KEXEC; do
        ! grep -q "^CONFIG_${opt}=y" .config || { echo "ERROR: CONFIG_${opt} lets root reach the kernel's memory!"; exit 1; }
    done

    # No virtual consoles. This machine's QEMU ships no VGA at all and its console is one
    # 16550 on ttyS0, so CONFIG_VT builds 64 tty devices that nothing opens and that udev
    # walks on every boot — a quarter of the 266 devices it coldplugs. olddefconfig drops
    # VT_CONSOLE, CONSOLE_TRANSLATIONS, VGA_CONSOLE and DUMMY_CONSOLE with it; UNIX98_PTYS
    # does not depend on it, so ssh keeps its ptys.
    #
    # It is off because it is dead, not because it is slow. Together with the erofs removal it
    # measures 0.31 ms against a tinyconfig floor kernel over 40 interleaved boots on an idle host
    # (2026-09-26), which is nothing. An earlier 20-boot run on a host that was building a
    # kernel at the time read 2.09 ms for this symbol alone and does not reproduce — at this
    # size a single run of this probe is a guess, and 40 on a quiet host is the smallest thing
    # worth quoting.
    ! grep -q "CONFIG_VT=y" .config || (echo "ERROR: CONFIG_VT is back; there is no VGA and no virtual console here (see above)!" ; exit 1)
    grep -q "CONFIG_OVERLAY_FS=y" .config || (echo "ERROR: CONFIG_OVERLAY_FS not enabled!" ; exit 1)

    # The machine enters vmlinux through PVH, with no decompressor (#645). The decompressor is
    # where a bzImage's KASLR happens, and a vmlinux entered through PVH ran where it was linked,
    # ffffffff81000000, with RANDOMIZE_BASE and RANDOMIZE_MEMORY both =y (three boots, until
    # 2026-11-19). RELOCATABLE_UNCOMPRESSED_KERNEL (patches/0014-0020) has the kernel move its
    # own virtual base at startup_64, from a relocation table the relocs tool writes into it,
    # and QEMU (qemu/patches/0008) puts it at a random physical base. PVH is the entry itself.
    # boot:kaslr asks the booted kernel. Nothing builds a bzImage since #660, so the kernel
    # compression mode compresses nothing: the choice keeps Kconfig's default.
    for opt in RELOCATABLE RANDOMIZE_BASE RANDOMIZE_MEMORY PVH RELOCATABLE_UNCOMPRESSED_KERNEL; do
        grep -q "^CONFIG_${opt}=y" .config || { echo "ERROR: CONFIG_${opt} is not enabled after olddefconfig (the kernel would not be randomized)!"; exit 1; }
    done

    # BPF. Each of these is something whose absence looks like a broken program rather than
    # a missing kernel option, which is why they are checked rather than trusted.
    #
    #   BPF_JIT         without it every program runs in the interpreter — an order of
    #                   magnitude slower, and `bpf_jit_enable` is simply not there.
    #   DEBUG_INFO_BTF  the one that matters most. No BTF means no /sys/kernel/btf/vmlinux,
    #                   so no `bpftool btf dump`, no vmlinux.h to generate, and no CO-RE
    #                   relocations: a program built against one kernel will not load on
    #                   another, which is the entire point of modern BPF.
    #   FUNCTION_TRACER with the architecture's direct-call support it is what makes
    #                   fentry/fexit attachment possible; without it those programs fail to
    #                   attach with a message about the target.
    grep -q "CONFIG_BPF_JIT=y" .config || (echo "ERROR: CONFIG_BPF_JIT not enabled (every BPF program would run interpreted)!" ; exit 1)
    grep -q "CONFIG_DEBUG_INFO_BTF=y" .config || (echo "ERROR: CONFIG_DEBUG_INFO_BTF not enabled (no BTF means no CO-RE)!" ; exit 1)
    # BTF costs 9 ms of every boot, and it is worth knowing where the bill arrives so that
    # nobody spends a week trying to move it. Measured 2026-09-10, function_graph at depth 1:
    # the *first* call to register_btf_kfunc_id_set takes 9302 us and the other fifty in the
    # boot take between 0.3 and 7 us each. It is the one-time parse of the 4.35 MB .BTF
    # section, charged to whichever kfunc set registers first.
    #
    # It was XFRM's: xfrm_init measured 8.6-9.1 ms across three boots and
    # register_xfrm_state_bpf was essentially all of it, which made inet_init look like a
    # 9 ms initcall and CONFIG_XFRM=n look like a 9 ms saving. It was not one: turning XFRM
    # off moved the bill to the next registrant. patches/0012 keeps the registrations made
    # before the parse on a list for the first bpf_get_btf_vmlinux(), and patches/0013 has
    # the verifier ask for the BTF only where a kernel type enters a program, so neither the
    # boot nor journald's cgroup device and skb programs parse it: the first login -6.4
    # [-9.4, -1.0] ms, 60 boots each (lab 45118194310, 2026-11-23). The first program that
    # uses a kernel type pays for the parse when it is loaded.
    grep -q "CONFIG_FUNCTION_TRACER=y" .config || (echo "ERROR: CONFIG_FUNCTION_TRACER not enabled (fentry/fexit cannot attach)!" ; exit 1)
    # What it cost a boot, and what is left of that: 21.9 ms measured 2026-09-29 against a
    # kernel without it (1 vCPU, the lab runner), of which the two ftrace patches in patches/
    # take back 20.5 - the boot-time NOP rewrite of every fentry site and the kallsyms scan of
    # every record for weak functions. Tetragon, Cilium and bpftrace need the tracer itself.
    # patches/0003 takes another 9.0 ms from event tracing, which BPF_EVENTS needs: the eval map
    # rewrite of every event format, now done on first use. All three: 109.9 -> 78.5 ms; with
    # patches/0004 (PCI configuration through ECAM, one trap an access), 73.7.
    # patches/0005 has each device's capability list walked once rather than per search: the
    # kernel 95.1 -> 88.9 ms (lab run 43327612594, 2026-11-12). Dropped and brought back on
    # 2026-11-13: against a goal of 100 ms to the first SSH login, 6 ms is 6% of it.
    # patches/0007 builds ORC's lookup table in one merge pass instead of a binary search per
    # 256-byte block of text: 3.5 ms of CPU on the bench fleet, nested and on metal alike (lab
    # runs 44563496308, 44563512999), 4.5 -> 0.8 ms on a laptop (2026-11-19).
    # patches/0008 takes init's 1,989 ftrace records out with one memmove, not one each: 2.6
    # ms before "Freeing unused kernel image" on the bench fleet, nested and on metal alike;
    # ftrace_free_mem() is 59 us after it.
    # patches/0010 skips early_quirks()' port I/O scan of bus 0 in a KVM guest: none of its
    # quirks matches this machine's devices, and the scan was 2.1 ms of exits nested (lab run
    # 44563496308). Only the scan: pci=noearly, which also defers MMCONFIG, does not boot.
    # 0006 (BTF parsed on another CPU), 0009 (polling for a booting AP) and 0011 (queue_select
    # written only on a change) were here and are not: taken out together in spin's shape, one
    # vCPU, the first login read -2.5 [-8.4, +1.8] ms (lab 44822157367, 40 boots each; first
    # +1.2 [-4.5, +8.3] at 20, lab 44818953987). The first two need a second CPU at boot.

    # Boot performance: the RAID6 PQ benchmark probes all SIMD implementations at boot to
    # pick the fastest. It must stay disabled. (RAID6_PQ itself is not selected today, so
    # the symbol is normally absent.)
    ! grep -q "CONFIG_RAID6_PQ_BENCHMARK=y" .config || (echo "ERROR: CONFIG_RAID6_PQ_BENCHMARK must not be enabled (boot perf)!" ; exit 1)

    # Boot performance, the drivers this machine does not have: not worth it, measured.
    #
    # The standing suggestion is to trim the 8250 block, which carries PCI, EXAR, LPSS, MID
    # and RSA support and 32 UARTs for a machine whose console is one 16550 ISA port.
    # serial8250_init costs 154 us — measured 2026-09-10, initcall_debug with a *silent*
    # console, `spin-machine boot --profile`, dmesg read back from the guest.
    #
    # The trap that makes candidates like this look worthwhile: with console output at
    # loglevel 8 the same initcall reads 3.6 ms. That is the cost of the printk, not of the
    # driver. Every initcall in the top twenty shrank by 3-20x once the console was silent
    # and the total went from 103.9 ms to 42.8 ms, so any initcall_debug run with a console
    # attached is measuring the serial port. For scale, the kernel reaches /sbin/init at
    # 65 ms and acpi_init alone is 10 ms of it — and ACPI is what CPU and memory hotplug
    # ride on, so the big one is not available either.

    # The memory-management options, which look like the largest remaining candidate and are
    # not one. `task boot:initcalls` reports ksm_init at 2.00 ms, hugepage_init at 2.00 and
    # kcompactd_init at 2.00 — and a kernel built with CONFIG_KSM and
    # CONFIG_TRANSPARENT_HUGEPAGE off, together with the erofs removal, measures -0.50 ms
    # against this one, both beside a tinyconfig floor kernel: nothing. Those round 2.00 values are what
    # initcall_debug can resolve at CONFIG_HZ=1000, not what the initcalls cost.
    #
    # Nothing in this config has been found to be worth more than about half a millisecond,
    # CONFIG_VT included — see the note beside its assertion. The 45.6 ms that separated this
    # kernel from a tinyconfig floor one that reaches the same marker (2026-09-26) is BTF, ACPI
    # and ftrace, and BPF and hotplug need all three.
    #
    # COMPACTION is not available at all: CONFIG_VIRTIO_MEM depends on CONTIG_ALLOC, which is
    # def_bool (MEMORY_ISOLATION && COMPACTION) || CMA, so compaction is what this machine's
    # memory resize path rides on. The VIRTIO_MEM assertion above is what says so, before a
    # compile rather than after one.
    #
    # The same question in its more reasonable form — keep virtio and ext4 built in, move
    # what this machine cannot use to modules in the base image — was measured on
    # 2026-09-10 and buys 0.57 ms. Every candidate, summed from initcall_debug with a
    # silent console:
    #
    #     SCSI / ATA / libata   0.00 ms      IPVS                     0.02 ms
    #     USB                   0.00 ms      IPv6                     0.10 ms
    #     HID / input           0.02 ms      bridge/vxlan/veth/tun    0.19 ms
    #     DRM / framebuffer     0.00 ms      netfilter, all of it     0.24 ms
    #     ethernet, non-virtio  0.00 ms
    #
    # against 38.6 ms of initcalls in total, of which acpi_init is 10.1 ms and ksm_init
    # plus kcompactd_init are 6.0 ms — none of them modularisable, all of them bool.
    #
    # The reason it is so cheap is worth keeping: a built-in driver whose hardware is not
    # present costs its *registration*, not a probe. There is nothing on the bus for it to
    # find. What a driver like that costs is bytes, not time — 14.5 MB of .text in a
    # 33.8 MB vmlinux — and those bytes are read once from the host's page cache (a warm
    # read of the whole file is under 10 ms, inside a 35 ms launch phase) and then sit in
    # each guest's RAM.
    #
    # And CONFIG_MODULES is off entirely — 1379 symbols =y and 0 =m — which the lockdown
    # assertion above holds it to.

    echo "Virtio configuration:"
    grep "CONFIG_VIRTIO" .config | grep -v "^#" || echo "No VIRTIO options enabled!"

    echo "Verifying kernel config for Docker support..."
    /usr/local/bin/check-docker-config.sh /usr/src/linux/.config || (echo "Kernel config verification failed!" ; exit 1)

    echo "Using kernel config from kernel/config-${KERNEL_SERIES}-${KERNEL_ARCH}"
EOT

FROM kernel-build-base AS kernel-build

ARG KERNEL_ARCH
ARG KERNEL_NPROC

# No cache mount: kernel compilation is not incremental between builds, so Docker's layer
# cache is the effective one.
#
# Reproducible: the same inputs give the same vmlinux, so a release whose kernel did not
# change keeps its kernel_sha256. v20260914.01 and .02, same source and config, differed in
# 3,525,384 bytes, for two reasons:
#
#   The build's date, user and host, which the kernel writes into its banner (uname -v) unless
#   told otherwise. Fixed here: the date is the snapshot's, the day the toolchain is pinned to.
#
#   .BTF — 3,524,411 of those bytes. The kernel runs pahole with make's -j
#   (scripts/Makefile.btf), and bookworm's pahole 1.24 encoded in parallel in whatever order
#   its threads finished. trixie's 1.30 does not: five links of one tree, three with -j16 and
#   two without, gave one .BTF (measured 2026-09-14). With 1.24, taking -j away fixed it, with
#   a wrapper passed as PAHOLE — if a pahole bump brings it back, that is the fix.
#
# Two builds compared is how to find out, and they have to be built with --no-cache: with
# only the make step uncached, buildkit took the step that copies vmlinux out from the
# first build's cache, and the comparison compared a file with itself.
ARG DEBIAN_SNAPSHOT
RUN cd linux && make ARCH=${KERNEL_ARCH} -j${KERNEL_NPROC} \
        KBUILD_BUILD_TIMESTAMP="$(date -u -d "$(echo "${DEBIAN_SNAPSHOT}" | cut -c1-8)")" \
        KBUILD_BUILD_USER=spin-machine KBUILD_BUILD_HOST=build \
        vmlinux

# Each patch in patches/, asserted on the vmlinux it went into, while it still has its symbol
# table. `patch --fuzz=0` above says a patch applied; a patch deleted from the directory, or one
# whose effect a kernel bump took away while its text still applied, leaves a kernel that builds,
# boots and is a few milliseconds slower, and nothing else says so. Every failure is reported,
# not the first, so a kernel bump sees at once which patches to read again. Where the code a
# patch leaves behind is visible in the machine code, that is what is read; 0008's is not, and
# the tree that was compiled is. The guest cannot check any of them: what they save is CPU and
# VM exits, which it cannot count, and 0003's format files are behind lockdown.
RUN <<EOT
    set -u
    cd /usr/src/linux
    failed=0
    dis() { objdump -d --no-show-raw-insn --disassemble="$1" vmlinux; }
    lost() { echo "ERROR: patches/$1 is not in this vmlinux: $2"; failed=1; }
    for f in vfs_read ftrace_check_for_weak_functions trace_eval_init trace_format_open \
            raw_pci_read raw_pci_write pci_setup_device early_quirks btf_vmlinux_defer \
            enable_IO_APIC pci_bus_add_device vp_modern_probe; do
        dis "$f" | grep -q "<$f>:" || { echo "ERROR: no function $f in vmlinux; the check that names it reads nothing"; failed=1; }
    done

    # 0001: objtool wrote the fentry sites as NOPs, so ftrace_init does not rewrite ~41k calls
    # at boot (7.0 ms, 2026-09-29). Without it every traced function still calls __fentry__.
    ! dis vfs_read | grep -q '<__fentry__>' || lost 0001 "vfs_read still calls __fentry__"

    # 0002: the weak function scan is compiled out when sorttable has zeroed those entries, so
    # boot queues no kallsyms walk of every record (11.9 ms of a kworker). Without it, this
    # initcall allocates a workqueue and queues the scan.
    ! dis ftrace_check_for_weak_functions | grep -w call | grep -vq '<__fentry__>' ||
        lost 0002 "ftrace_check_for_weak_functions still calls out (alloc_workqueue, queue_work)"

    # 0003: boot does not queue the eval map rewrite of every event format (9.0 ms of a
    # kworker), and the first read of a format does it.
    ! dis trace_eval_init | grep -q '<queue_work_on>' || lost 0003 "trace_eval_init still queues the eval map rewrite"
    dis trace_format_open | grep -q '<trace_eval_ensure>' || lost 0003 "trace_format_open does not apply the deferred eval maps"

    # 0004: configuration space through ECAM first, one exit an access, not type 1's two. The
    # first of the two ops tables each function reads is the one it prefers.
    for f in raw_pci_read raw_pci_write; do
        first=$(dis "$f" | grep -Eo -m1 '<raw_pci_(ext_)?ops>')
        [ "$first" = '<raw_pci_ext_ops>' ] || lost 0004 "$f reads ${first:-neither ops table} first, not raw_pci_ext_ops"
    done

    # 0005: each device's capability list is walked once, at setup, and searches answer from it.
    dis pci_setup_device | grep -q '<pci_cache_capabilities>' || lost 0005 "pci_setup_device does not read the capability list once"

    # 0007: ORC's lookup table is built in one pass, so unwind_init has no binary search left
    # to fail, and the message it gave when one did is gone.
    ! grep -aqF 'Corrupt .orc_unwind table' vmlinux || lost 0007 "unwind_init still binary-searches for every block"

    # 0008: a freed range's ftrace records go with one memmove. The machine code of either loop
    # looks alike, so the source that was compiled is read.
    grep -qF 'memmove(first, stop,' kernel/trace/ftrace.c || lost 0008 "ftrace_free_mem moves the page once for every record"

    # 0010: early_quirks() asks whether it runs under KVM before scanning bus 0 through port
    # I/O. The call is there only when the patch is and CONFIG_KVM_GUEST is on.
    dis early_quirks | grep -q '<kvm_para_available>' || lost 0010 "early_quirks scans bus 0 in a KVM guest"

    # 0012: a kfunc set, destructor or struct_ops registered before vmlinux BTF is parsed waits
    # on a list for its first user, so no initcall parses it (~7 ms nested).
    # btf_vmlinux_defer is in the list of functions above; the registrations' calls to it are
    # static and may be inlined, so their source is read.
    grep -qF 'btf_vmlinux_defer(&r->list)' kernel/bpf/btf.c || lost 0012 "kfunc registration parses vmlinux BTF at boot"

    # 0013: bpf_check() asks for vmlinux BTF only where a kernel type enters a program, so a
    # cgroup device or skb program does not parse it. The helper may be inlined, so the source
    # that was compiled is read.
    grep -qF 'need_btf_vmlinux(env)' kernel/bpf/verifier.c || lost 0013 "bpf_check parses vmlinux BTF for every program"

    # 0014-0016: the relocs tool wrote the table into .data.reloc. It ends in the last 32-bit
    # relocation; a table it never filled ends in the zero it was reserved with, and the kernel
    # then runs where it was linked. A table that does not fit fails the link itself: relocs dies
    # naming the CONFIG_RELOCATION_TABLE_SIZE it needs. What it uses is printed, so a config
    # change that grows it is seen before it reaches the reserve.
    reloc=$(readelf -SW vmlinux | awk '{for (i=1;i<=NF;i++) if ($i==".data.reloc") print $(i+3), $(i+4)}')
    [ -n "$reloc" ] || lost 0016 "no .data.reloc section"
    set -- $reloc
    off=$((0x$1)); size=$((0x$2))
    used=$(od -An -v -t u4 -j "$off" -N "$size" vmlinux | tr -s ' ' '\n' | grep -v '^$' |
        awk 'NR > 1 && $1 != 0 && !n { n = NR } END { print n ? (NR - n + 2) * 4 : 0 }')
    [ "$used" -gt 0 ] || lost 0015 "the relocs tool did not fill .data.reloc"
    echo "relocation table: $used of $size bytes"

    # 0017: startup_64 relocates the kernel before it computes its own physical-to-virtual offset.
    dis startup_64 | grep -q '<__pi___relocate_kernel>' || lost 0017 "startup_64 does not relocate the kernel"

    # 0017's code runs before the kernel has relocated itself, so it may not call through the
    # GOT or an absolute address, and may not call instrumentation that is not there yet: an
    # ftrace hook (__fentry__), KASAN, KCOV, UBSAN or the stack protector. Under PIE, a call
    # to __fentry__ through the GOT in a PVM guest's self-relocation kept the guest from
    # starting (virt-pvm/linux#11, fixed by dropping CC_FLAGS_FTRACE there). The RFC puts the
    # code in arch/x86/boot/startup, whose Makefile builds it without those flags; this asks
    # the object that was compiled, kaslr.c with lib/kaslr.c and lib/cmdline.c included in it,
    # so a config or Makefile change cannot put one back unseen. Its relocations, not vmlinux:
    # the linker relaxes a GOT load away, and objtool turns __fentry__ calls into NOPs.
    kobj=arch/x86/boot/startup/kaslr.pi.o
    [ -f "$kobj" ] || kobj=arch/x86/boot/startup/kaslr.o
    if [ ! -f "$kobj" ]; then
        lost 0017 "no arch/x86/boot/startup/kaslr.o to check"
    else
        echo "pre-relocation functions ($kobj): $(nm --defined-only "$kobj" | awk '$2 ~ /[tT]/ { print $3 }' | tr '\n' ' ')"
        bad=$(objdump -dr "$kobj" | grep -E 'R_X86_64_(GOT|REX_GOT)|__fentry__|__(asan|kasan|ubsan|sanitizer_cov|stack_chk)_?' | sort -u)
        [ -z "$bad" ] || lost 0017 "the code that runs before relocation is instrumented or goes through the GOT: $bad"
    fi

    # 0019 and 0020: a kernel that moved itself says so, so RANDOMIZE_MEMORY moves the direct map,
    # vmalloc and vmemmap; and it draws its base from RDRAND and RDTSC, read from the registers
    # that hold them. Both are early startup code with no symbol of their own, so the source is read.
    grep -qF 'bp->hdr.loadflags |= KASLR_FLAG;' arch/x86/boot/startup/kaslr.c ||
        lost 0019 "a kernel that relocated itself does not set KASLR_FLAG"
    grep -A1 -F 'flag == X86_FEATURE_RDRAND' arch/x86/boot/startup/kaslr.c | grep -qF 'native_cpuid_ecx(0x1)' ||
        lost 0020 "has_cpuflag reads RDRAND from a register that does not hold it"

    # 0021: enable_IO_APIC() clears each pin from the read its ExtINT search made, so it no
    # longer reads the whole redirection table again through clear_IO_APIC().
    ! dis enable_IO_APIC | grep -q '<clear_IO_APIC>' || lost 0021 "enable_IO_APIC still reads every pin again through clear_IO_APIC"

    # 0022: a device's state is saved at add only where something can restore it unsaved; the
    # check of the root bus's host bridge is the gate's, and the call is there only with it.
    dis pci_bus_add_device | grep -q '<pci_find_host_bridge>' || lost 0022 "pci_bus_add_device saves every device's state"

    # 0023: virtio-pci's vendor capabilities are walked once, not once for each of four types,
    # so vp_modern_probe starts one walk.
    [ "$(dis vp_modern_probe | grep -c '<pci_find_capability>')" = 1 ] ||
        lost 0023 "vp_modern_probe walks the vendor capabilities once for each type"

    # 0024: queue_select is written only when the queue changes. The helper is static and
    # inlined into every accessor, so the source that was compiled is read.
    grep -qF 'mdev->selected_queue == index' drivers/virtio/virtio_pci_modern_dev.c ||
        lost 0024 "virtio-pci writes queue_select before every queue field"

    # 0025: msi_set_affinity() returns before the message write when the vector and the
    # destination did not change. A comparison in its machine code looks like any other, so the
    # source that was compiled is read.
    grep -qF 'cfg->dest_apicid == old_cfg.dest_apicid' arch/x86/kernel/apic/msi.c ||
        lost 0025 "msi_set_affinity writes an unchanged message"

    [ "$failed" = 0 ] || exit 1
    echo "kernel patches: each is in this vmlinux"
EOT

# What this kernel is made of, one source or header a line: the union of the dependency lists
# the build writes beside every object it compiled (.<object>.o.cmd: source_ and deps_). A CVE
# whose files are none of these is in code this kernel does not contain, and internal/scan
# reads it to say so from the build rather than from a guess about which CONFIG_ symbol builds
# which file. scripts/ and tools/ are the build's own programs, which never run in a guest, and an
# absolute path is the compiler's own header. A list of under 5000 is a build the pattern no longer
# reads, and refused here rather than reported as a kernel with nothing in it.
RUN mkdir -p /build && cd linux && find . -name '.*.o.cmd' -not -path './scripts/*' -not -path './tools/*' -print0 \
      | xargs -0 sed -n -e 's/^source_[^ ]* := //p' -e 's/^  *\([^ $][^ ]*\)\( \\\)\{0,1\}$/\1/p' \
      | sed 's|^\./||' | grep -v '^/' | sort -u > /build/sources \
    && n=$(wc -l < /build/sources) && echo "sources: $n files" \
    && [ "$n" -gt 5000 ] || { echo "ERROR: the kernel's sources list has ${n:-no} entries"; exit 1; }

RUN <<EOT
    set -e
    cd linux
    mkdir -p /build/patches
    cp .config /build/kernel-config
    cp /usr/src/patches/*.patch /build/patches/

    if [ "${KERNEL_ARCH}" != "x86_64" ]; then
        echo "ERROR: Only x86_64 architecture is supported, got: ${KERNEL_ARCH}"
        exit 1
    fi

    # Strip the ELF symbol table from the vmlinux that ships. QEMU enters this file through PVH
    # (as it did until 2026-11-19, and again since #645), and QEMU's load_elf() reads and
    # qsort()s the symbol table of every kernel it loads — 42k entries here. .data.reloc, the
    # table the kernel relocates itself from, is an allocated section and survives. Nothing in this stack
    # asks the stripped file for a symbol: the guest resolves its own names
    # through CONFIG_KALLSYMS, which lives in the loaded image data and is untouched by
    # this.
    #
    # PT_LOAD is byte-identical either way (30.68 MB), so the guest boots the same bits;
    # what goes away is 5.1 MB of file and the parse and sort of it.
    #
    # Measured while QEMU loaded vmlinux (before 2026-11-19): qemu_launch 36.1 ms -> 24.3 ms,
    # guest boot unchanged.
    cp vmlinux /build/vmlinux
    strip -s /build/vmlinux

    # .BTF is asserted to survive the strip, rather than reasoned about. `strip -s` removes the symbol
    # table and the DWARF the compiler emitted — which is the point, since neither is read
    # at run time — but .BTF is an allocated section the *guest* reads back out of its own
    # image and publishes at /sys/kernel/btf/vmlinux. Losing it here would not break the
    # build or the boot; it would break every CO-RE program somebody writes next month.
    readelf -SW /build/vmlinux | grep -q '\.BTF' || {
        echo "ERROR: the stripped vmlinux has no .BTF section — CO-RE would not work in the guest"; exit 1; }
    # The size of .BTF, reported because it is the largest thing BPF support adds to the
    # image and somebody bumping the kernel should see it move.
    #
    # readelf prints "[ 8] .BTF PROGBITS <addr> <off> <size>", so the section name is in
    # column 2 or 3 depending on whether the index is one digit or two: found by name, and
    # the size is four columns on. No strtonum — that is GNU awk and this image has mawk,
    # so it would have failed here rather than in the two earlier attempts, which printed
    # an empty string and then the offset.
    btf=$(readelf -SW /build/vmlinux | awk '{for (i=1;i<=NF;i++) if ($i==".BTF") print $(i+4)}')
    printf 'BTF: %d bytes\n' "0x$btf"
EOT

# sparse (make C=2) over every C file kernel/patches touches, once as built and once with the
# patches taken back out, and a warning the patches add fails the build. Upstream's own
# warnings in those files are counted on both sides, so they fail nothing; what is compared is
# each message with its line number removed, and how many times it appears, so a patch that
# moves a warning is not one that adds it. A touched file that is not a C or header file (0001's
# Kconfig) keeps its patched text in both, or the build reconfigures between the two. Each
# side must show make checking every file, or a file that stopped building would compare
# empty with empty. A file a patch adds (0017's arch/x86/boot/startup/kaslr.c) has no upstream
# side, so every warning in it is new. arch/x86/tools is left out by name: host programs, which
# C= never checks.
#
# sparse itself is built from versions.yaml's `sparse`, not taken from trixie: the kernel's
# scripts/checker-valid.sh asks the checker to parse __typeof_unqual__, which trixie's 0.6.4
# cannot, and make then prints "sparse is not available or not up to date", sets C=0 and
# checks nothing. The build is checked the way make checks it, by that same script.
ARG SPARSE_VERSION
ARG SPARSE_COMMIT
ARG SPARSE_ARCHIVE_SHA256
RUN git clone --quiet --branch "${SPARSE_VERSION}" https://git.kernel.org/pub/scm/devel/sparse/sparse.git /usr/src/sparse-git && \
    git -C /usr/src/sparse-git archive "${SPARSE_COMMIT}" > /usr/src/sparse.tar && \
    echo "${SPARSE_ARCHIVE_SHA256}  /usr/src/sparse.tar" | sha256sum -c - && \
    mkdir /usr/src/sparse && tar -xf /usr/src/sparse.tar -C /usr/src/sparse && \
    make -C /usr/src/sparse -j"$(nproc)" HAVE_LLVM=no HAVE_GTK=no HAVE_LIBXML=no PREFIX=/usr/local install && \
    { test "$(sh /usr/src/linux/scripts/checker-valid.sh sparse)" = 1 || \
        { echo "ERROR: the kernel's checker-valid.sh refuses sparse ${SPARSE_COMMIT}"; exit 1; }; }

RUN <<EOT
    set -eu
    cd /usr/src/linux
    files=$(sed -n 's|^+++ b/\(.*\.c\)$|\1|p' /usr/src/patches/*.patch | sort -u | grep -v '^arch/x86/tools/')
    others=$(sed -n 's|^+++ b/\(.*\)$|\1|p' /usr/src/patches/*.patch | grep -v '\.[ch]$' | sort -u)
    sparse_of() {
        : > "/tmp/sparse-$1.log"
        for f in $files; do
            [ "$1" = upstream ] && [ ! -e "$f" ] && continue
            make ARCH=${KERNEL_ARCH} C=2 "${f%.c}.o" >> "/tmp/sparse-$1.log" 2>&1 || {
                cat "/tmp/sparse-$1.log"; echo "ERROR: make C=2 ${f%.c}.o failed ($1)"; exit 1; }
        done
        for f in $files; do
            [ "$1" = upstream ] && [ ! -e "$f" ] && continue
            grep -q "^  CHECK  *$f\$" "/tmp/sparse-$1.log" || {
                cat "/tmp/sparse-$1.log"; echo "ERROR: sparse did not check $f ($1)"; exit 1; }
        done
        grep -E '^[^ :]+:[0-9]+:[0-9]+: (warning|error): ' "/tmp/sparse-$1.log" \
            | sed -E 's/:[0-9]+:[0-9]+: /: /' | sort | uniq -c > "/tmp/sparse-$1"
    }
    sparse_of patched
    for f in $others; do cp -p "$f" "$f.patched"; done
    for p in $(ls -r /usr/src/patches/*.patch); do patch -R -p1 --fuzz=0 < "$p" > /dev/null; done
    for f in $others; do mv "$f.patched" "$f"; done
    sparse_of upstream
    echo "sparse: $(wc -l < /tmp/sparse-upstream) distinct upstream warnings in $(echo $files | wc -w) files"
    awk '
        NR == FNR { n = $1; $1 = ""; base[$0] = n; next }
        { n = $1; $1 = ""; if (n > base[$0] + 0) { print "ERROR: sparse, new with the patches:" $0; bad = 1 } }
        END { exit bad }
    ' /tmp/sparse-upstream /tmp/sparse-patched
    echo "sparse: the patches add no warning"
EOT

# The tree this stage writes is the release tree: kernel/vmlinux and what it was built from, at the
# paths a release carries them and machine.Release reads them from. Extracting to the root and
# letting hack/release do the moving gives the same files one layout here, a second in the
# tarball and a third wherever a consumer rearranges them into — with a translation step
# between each pair, and each of those a place to be wrong.
FROM scratch AS kernel
COPY --from=kernel-build /build/vmlinux /kernel/vmlinux
COPY --from=kernel-build /build/kernel-config /kernel/kernel-config
COPY --from=kernel-build /build/sources /kernel/sources
COPY --from=kernel-build /build/patches /kernel/patches
