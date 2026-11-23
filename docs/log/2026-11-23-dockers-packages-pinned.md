---
date: "2026-11-23"
order: 120
area: image
title: 'Docker''s packages pinned (#511, #609, Parts run 45111218531)'
issues: [511]
runs: [45111218531]
---
`install-dev-tools.sh` installed Docker's packages at whatever Docker's repository served that day and fetched its signing key with no hash. *Changed:* each `.deb` in Docker's resolute pool is a `download` in `versions.yaml`, by version and SHA-256, handed in as build arguments with no default and checked before apt installs it. There are six, not the five #511 named, because `docker-ce-rootless-extras` came in unpinned as docker-ce's recommend. The pins are v20261123.04's versions, and each SHA-256 is the one in Docker's `Packages` index, whose `InRelease` verifies against Docker's key. The build no longer reads Docker's repository. The key and the apt source a workspace's `apt upgrade` uses are files in `rootfs/`, the key unchanged from what Docker serves (fingerprint `9DC858229FC7DD38854AE2D88D81803C0EBFCD88`, no expiry). It is not a `versions.yaml` entry, because a `download` needs a version in its URL and the key's has none. *Proved:* one changed character in docker-compose-plugin's pin stops the image build at `sha256sum: WARNING: 1 computed checksum did NOT match` (Parts run 45111218531, branch archived as `archive/511-wrong-pin`). *Status:* in place.
