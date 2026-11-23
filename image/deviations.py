#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Every way the image at ROOT differs from what its packages installed, one "KIND PATH" a line:
# docs/image.md, "The inventory".
import glob
import hashlib
import os
import sys

root = sys.argv[1]

# Where a package's configuration is changed. A file here that no package owns and no maintainer
# script recorded was put there by the build.
SURFACES = ("/etc/systemd/", "/etc/tmpfiles.d/", "/etc/udev/", "/etc/environment.d/", "/etc/ssh/",
            "/etc/sudoers.d/", "/etc/sysctl.d/", "/etc/modprobe.d/", "/etc/docker/", "/etc/containerd/",
            "/etc/apt/", "/etc/profile.d/", "/etc/pam.d/", "/etc/security/", "/etc/default/")

# Files at the top of /etc that maintainer scripts write on every Ubuntu: the accounts, the
# linker's cache, the files base-files' and netbase's scripts make.
MADE_BY_PACKAGES = {
    "passwd", "passwd-", "group", "group-", "shadow", "shadow-", "gshadow", "gshadow-", "subuid",
    "subuid-", "subgid", "subgid-", ".pwd.lock", "shells", "hosts", "hosts.allow", "hosts.deny",
    "networks", "mtab", "ld.so.cache", "environment", "inputrc", "profile", "modules",
    "ca-certificates.conf", "vconsole.conf", "locale.gen", "localtime",
}

# What maintainer scripts make under the surfaces on every Ubuntu without recording it: systemd's
# postinst enables three units with systemctl, libpam-modules' makes opasswd and base-files'
# links /usr/local/man.
STOCK = {
    "/etc/systemd/system/getty.target.wants/getty@tty1.service",
    "/etc/systemd/system/multi-user.target.wants/remote-fs.target",
    "/etc/systemd/system/sysinit.target.wants/systemd-pstore.service",
    "/etc/security/opasswd",
    "/usr/local/man",
}

# A file a package makes from a template it ships, rather than a conffile: changed when it is not
# the template.
TEMPLATES = {
    "/etc/nsswitch.conf": "/usr/share/libc-bin/nsswitch.conf",
    "/etc/ssh/sshd_config": "/usr/share/openssh/sshd_config",
}

# pam-auth-update writes these from /usr/share/pam-configs; the build edits one line of one.
PAM_GENERATED = ("/etc/pam.d/common-auth", "/etc/pam.d/common-account", "/etc/pam.d/common-password",
                 "/etc/pam.d/common-session", "/etc/pam.d/common-session-noninteractive")


def path(p):
    return root + p


def read(p):
    with open(path(p), "rb") as f:
        return f.read()


owned = set()
for name in glob.glob(path("/var/lib/dpkg/info/*.list")):
    with open(name) as f:
        for line in f:
            p = line.rstrip("\n")
            owned.add(p)
            # Packages built before the merged /usr name /lib, /bin and /sbin.
            for old in ("/lib/", "/bin/", "/sbin/"):
                if p.startswith(old):
                    owned.add("/usr" + p)

conffiles = {}
pkg = None
in_conffiles = False
with open(path("/var/lib/dpkg/status")) as f:
    for line in f:
        if line.startswith("Package:"):
            pkg, in_conffiles = line.split()[1], False
        elif line.startswith("Conffiles:"):
            in_conffiles = True
        elif in_conffiles and line.startswith(" "):
            fields = line.split()
            if "obsolete" not in fields[2:] and "remove-on-upgrade" not in fields[2:]:
                conffiles[fields[0]] = fields[1]
        else:
            in_conffiles = False

# What deb-systemd-helper enabled or linked on a package's behalf, system and user.
recorded = set()
enabled = []
for helper, unit_dir in (("deb-systemd-helper-enabled", "/etc/systemd/system"),
                         ("deb-systemd-user-helper-enabled", "/etc/systemd/user")):
    base = path("/var/lib/systemd/" + helper)
    for d, _, files in os.walk(base):
        for n in files:
            rel = os.path.join(d, n)[len(base):]
            if rel.endswith(".dsh-also"):
                with open(base + rel) as f:
                    recorded.update(line.strip() for line in f if line.strip())
            else:
                recorded.add(unit_dir + rel)
                enabled.append(unit_dir + rel)

out = set()
for p, md5 in conffiles.items():
    if not p.startswith("/etc/"):
        continue
    if os.path.islink(path(p)) and os.readlink(path(p)) == "/dev/null":
        out.add(("masked", p))
    elif not os.path.lexists(path(p)):
        out.add(("deleted", p))
    elif os.path.isfile(path(p)) and hashlib.md5(read(p)).hexdigest() != md5:
        out.add(("modified", p))

for p, template in TEMPLATES.items():
    if os.path.lexists(path(p)) and read(p) != read(template):
        out.add(("modified", p))

if b"class=" in read("/etc/pam.d/common-session"):
    out.add(("modified", "/etc/pam.d/common-session"))

for d, dirs, files in os.walk(path("/etc")):
    for n in files + [x for x in dirs if os.path.islink(os.path.join(d, x))]:
        p = os.path.join(d, n)[len(root):]
        if p in owned or p in recorded or p in conffiles or p in TEMPLATES or p in PAM_GENERATED or p in STOCK:
            continue
        top = p.count("/") == 2
        if not (top and n not in MADE_BY_PACKAGES) and not p.startswith(SURFACES):
            continue
        if os.path.islink(path(p)) and os.readlink(path(p)) == "/dev/null":
            out.add(("masked", p))
        else:
            out.add(("added", p))

# Units, helpers and tools no package installed.
for top in ("/usr/lib/systemd/system", "/usr/lib/systemd/user", "/usr/libexec", "/usr/local"):
    for d, dirs, files in os.walk(path(top)):
        for n in files + [x for x in dirs if os.path.islink(os.path.join(d, x))]:
            p = os.path.join(d, n)[len(root):]
            if p not in owned and p not in STOCK:
                out.add(("added", p))

for p in enabled:
    if not os.path.lexists(path(p)):
        out.add(("unwanted", p))

for n in os.listdir(path("/var/lib/systemd/linger")) if os.path.isdir(path("/var/lib/systemd/linger")) else []:
    out.add(("linger", "/var/lib/systemd/linger/" + n))

# A package's file the image does not have, under the directory it was taken from: a whole tree
# under /usr/share, otherwise the file's own directory.
for p in owned:
    if not p.startswith("/") or os.path.lexists(path(p)) or p in conffiles:
        continue
    parts = p.split("/")
    where = "/".join(parts[:4]) + "/" if p.startswith("/usr/share/") else os.path.dirname(p) + "/"
    out.add(("deleted", where))

for kind, p in sorted(out, key=lambda x: (x[1], x[0])):
    print(kind, p)
