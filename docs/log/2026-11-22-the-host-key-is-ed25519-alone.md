---
date: "2026-11-22"
order: 100
area: image
title: the host key is Ed25519 alone (lab 44979726719, this change)
runs: [44979726719]
---
spin-machine-sshd-keygen made an Ed25519 and an ECDSA key, each with its own ssh-keygen, before ssh.service starts, on the one vCPU. Ed25519 alone: the cold first login −5.0 [−8.3, −1.8], sshd accepting −4.4 [−6.9, −1.9], 60 boots each interleaved, spin's shape and login. Every client since OpenSSH 6.5 takes Ed25519; one that accepts only ECDSA host keys no longer gets in. The same run took `pam_selinux` and `pam_mail` out of sshd's PAM stack, −1.9 [−4.9, +2.8], not separable. *Status:* Ed25519 alone; the restore unchanged (in the description).
