---
date: "2026-11-23"
order: 100
area: image
title: 'the inventory''s blind spot was mkosi (#518, image build on #517, this change)'
issues: [518]
prs: [517]
---
#518 asked the inventory to tell a path a package ships from one it only lists, because `/etc/systemd/system-generators/systemd-gpt-auto-generator` is in `systemd.list` and an image built without `optimize-systemd.sh`'s link to it had none. *Read* (2026-11-23): Ubuntu's `debian/rules` makes that link with `dh_link`, and `systemd_259.9-0ubuntu0.1_amd64.deb` carries it (`dpkg-deb -c`), so the package ships it and a `.list` holds only what the package's data does. mkosi 27.1's `installer/apt.py` unlinks it after every install, to turn gpt-auto on in mkosi's own images. The image's link puts back Ubuntu's mask, so the inventory is right not to list it, and it did see the deletion, as `deleted /etc/systemd/system-generators/`. Comparing against md5sums would have done harm: md5sums lists no symlinks, so every link a package ships would read as added. *Changed:* docs/image.md's inventory note and generator row say so; nothing in `image/`. *Status:* #518 closed without a change to the inventory.
