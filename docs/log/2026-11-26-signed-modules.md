---
date: "2026-11-26"
order: 10
area: kernel
title: loadable modules, signed by spin-stack only (#736)
issues: [736]
prs: [750, 756]
runs: [45760066064, 45760766064, 45761349799, 45761349927]
---
*Why:* a workspace has to load more of the kernel than it boots with, and only what spin-stack signed: nothing a tenant builds may run in the kernel. *Done:* `MODULES` with `MODULE_SIG_FORCE`; wireguard, fuse, squashfs and erofs are modules, signed in a release by spin-stack/ci's KMS key and installed in the image's `/usr/lib/modules`, and the kernel's one trusted key is that key's certificate, `kernel/module-signing.crt` (kernel/README.md, "Modules"). `VIRTIO_FS` went from built in to off: as a module, systemd's PID 1 loads it and fuse in every boot of a guest with a virtio PCI device. The first measurement of the whole change read first login +7.5 [+3.6, +16.2] against main's kernel (bench, spin's shape and login, 20 a side, lab 45760066064). Taken apart, the kernel alone read −2.3 [−6.0, +4.8] and the kernel with its modules in the image +5.8 [+1.4, +12.3] (30 a side, lab 45760766064): Ubuntu's `kmod-static-nodes.service`, which ran once the image had `modules.devname`, holds `sysinit.target`. It is masked, and `spin-machine-static-nodes.service` makes `/dev/fuse` in the boot's rest: first login −0.4 [−5.2, +7.0] (30 a side, lab 45761349799), restored login +0.4 [−0.5, +2.1] evicted and −0.1 [−1.3, +0.7] cached (20 a side, lab 45761349927). The kernel with `MODULES` holds 4.5 MiB more in QEMU's RSS and its state is 2.6 MiB larger. Its `.rodata` grew 0.4 MB with the exported-symbol tables that `MODULES` keeps, which accounts for part of that. *Status:* merged in #750, the certificate in #756; no release has shipped it yet.
