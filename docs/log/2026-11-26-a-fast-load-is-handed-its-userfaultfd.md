---
date: "2026-11-26"
order: 10
area: qemu
title: a fast load is handed its userfaultfd, and the host grants it to no one
issues: [1711]
---
*Why:* a fast load needs a userfaultfd that sees kernel faults, since KVM touches guest memory from the kernel. QEMU 11.2 opens `/dev/userfaultfd` by path or asks the system call, so `userfaults` accepted a host only where the device was open to every user or `vm.unprivileged_userfaultfd` was 1. Either gives every process on the host a primitive for widening kernel races, and Spin OS grants neither, so spin's hosts loaded every state in full (spin-stack/spin#1711). *Done:* qemu/patches/0010 adds `-userfaultfd PATH` and opens the device through `qemu_open`, which understands `/dev/fdset/N`. `machine.Start` opens `/dev/userfaultfd` for a machine held for a restore and hands QEMU the descriptor in a set (`Spec.UserfaultFDSet`), as it does `/dev/kvm`. A host whose device the starting process cannot open gets nothing and loads in full. `userfaults` is now whether this process opens the device read-write. The bench gives its runner's user the device (`chown`) instead of setting the sysctl. A QEMU that holds the descriptor gets a full userfaultfd, the same power the world-writable device gave it, limited to that QEMU. *Status:* in place; spin follows in its runner (`fastLoadRefusal`).
