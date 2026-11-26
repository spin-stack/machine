---
date: "2026-11-25"
order: 90
area: kernel
title: what shirudo's flow needs of the kernel beyond its table (#740)
issues: [734, 740]
---
*Why:* booting shirudo on this kernel at integrity (#734) found four options its flow needs that its "Kernel configuration" table does not list. dracut's default initrd is zstd, and the kernel said `compression method zstd not configured` and booted the root without shirudo's initrd; `kprobe.multi` upatches failed to attach with `Operation not supported`; `fmod_ret` reached only syscalls and `security_*`; and its hardening checklist wants writes to a mounted block device refused, since its seal acts on file operations. *Done:* config-7.4-x86_64 turns on `CONFIG_RD_ZSTD`, `CONFIG_FPROBE` and `CONFIG_FUNCTION_ERROR_INJECTION` and turns `CONFIG_BLK_DEV_WRITE_MOUNTED` off, with what olddefconfig derives from them; its new prompts `CONFIG_FPROBE_EVENTS` and `CONFIG_BPF_KPROBE_OVERRIDE` stay off. `kernel/Dockerfile` checks all four. Lockdown governs none of the BPF use they give, at either level, and root had as much through `fentry` and `fmod_ret` on syscalls and `security_*` already (kernel/README.md, "shirudo's hooks"). Nothing in this machine or spin writes to a mounted block device: spin's growfs opens the disk read-only and grows through an ioctl on the mount, and the image masks `systemd-fsck-root`. *Status:* in place; a new kernel for every guest's next boot.
