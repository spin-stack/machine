---
date: "2026-11-23"
order: 130
area: image
title: containerd's config is containerd.io's own (#510, labs 45111391250, 45111705358, 45112037022, this change)
issues: [510]
runs: [45111391250, 45111705358, 45112037022]
---
The image replaced containerd.io's `/etc/containerd/config.toml`, which disables CRI, with one that enabled it, a v3 schema with the v2 plugin name, with no consumer named since `2afac47`. dockerd does not use CRI. *Decided* by the owner (#510, option b): containerd.io's file. *Measured* with an overlay edit that wrote the package's setting over the image's (bench, nested, spin's shape and login, against v20261123.04, 20 a side): first login −4.9 [−12.2, +1.6], sshd accepting −5.0 [−12.1, +1.2], both within noise (lab 45111391250). Restored login: evicted −0.6 [−2.1, +1.5], cached −0.5 [−2.1, +0.9], both within noise (lab 45112037022). Neither number can see the file, because containerd starts at a workspace's first docker command (inferred). `boot:workspace` passed on that tree, with docker running a container (lab 45111705358). *Changed:* the image's file is deleted. A workspace that wants CRI, for crictl or a kubelet, writes its own config. *Status:* in place; the experiment branch is archived as `archive/510-containerd`.
