---
date: "2026-11-22"
order: 90
area: kernel
title: vmlinux BTF parsed on first use moves the parse, not the login (labs 44982285400, 44982285406)
runs: [44982285400, 44982285406]
---
kernel/Dockerfile says the first kfunc registration of the boot parses the 4.6 MB of vmlinux BTF, today xfrm's, and that only `DEBUG_INFO_BTF=n` removes it. A patch (branch `exp/btf-first-use`) keeps vmlinux's kfunc sets, destructors and struct_ops registered before the parse on a list, and the first `bpf_get_btf_vmlinux()` parses and registers them, in order, before any other caller gets the BTF; a struct_ops init that registers a kfunc set in turn (io_uring's) registers it at once. A program calling kfuncs of the common set loaded and ran on it. The kernel to "Freeing" −7.2 [−8.6, −6.2], inet_init 6.85 → 0.14 ms (10 boots), but the cold first login −1.5 [−3.1, +1.6] (60 boots): journald's `DeviceAllow=` and `IPAddressDeny=` load cgroup BPF programs at its start, and `bpf_check()` asks for vmlinux BTF for every program, so the parse lands in journald's start, before the login, on the same vCPU. *Status:* not carried. A verifier that asks for BTF only when a program needs it would let the parse leave the boot.
