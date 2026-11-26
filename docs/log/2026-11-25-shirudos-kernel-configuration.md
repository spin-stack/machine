---
date: "2026-11-25"
order: 60
area: kernel
title: the rest of shirudo's kernel configuration (#720, #734)
issues: [719, 720, 734]
---
*Why:* with shirudo's hooks carried, what shirudo still lacked of the kernel was configuration: its system requirements' "Kernel configuration" table, of which #720 found five options off. *Done:* config-7.4-x86_64 turns on `CONFIG_BLK_DEV_DM`, `CONFIG_DM_VERITY` and `CONFIG_DM_VERITY_VERIFY_ROOTHASH_SIG`, which map shirudo's runtime and config images and check a config's signed root hash, and `CONFIG_SYSTEM_DATA_VERIFICATION` and `CONFIG_CRYPTO_MLDSA`, which verify its ML-DSA-87 signatures, with what olddefconfig derives from them. Every other row of the table was on already. `kernel/Dockerfile` checks each row after olddefconfig, so none drops out in silence. shirudo's `CONFIG_MODULE_SIG` row applies only to a kernel that loads modules, and this one has none; signing comes with modules, signed-only. Reading shirudo's hardening checklist found one conflict: its programs read kernel memory with `bpf_probe_read_kernel`, which this kernel's lockdown at confidentiality withholds (#734). *Not yet:* a TPM in the machine (#719), and #734. *Status:* in place.
