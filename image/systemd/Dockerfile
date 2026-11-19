# The systemd the base image installs: Ubuntu's own, rebuilt from its source package with less
# in it, because what PID 1, udevd and the units' executor do at every boot is the first login's
# largest share after the kernel.
#
# The source is Ubuntu's source package of the version the image would otherwise install, by
# its .dsc: its patches, and so its security fixes, are Ubuntu's. What changes is the build:
#
#   stage1   Debian's own reduced build, maintained in debian/rules and debian/control: no
#            audit, cryptsetup, coredump, AppArmor, OpenSSL, TPM2, p11-kit, FIDO2, PCRE2, homed,
#            userdb, importd, curl, sysupdate, boot loader or ukify, and no BPF framework. That
#            last one is where the time was: PID 1's setup before the generators went from 25.8
#            to 5.6 ms (boot:waits), the BPF verifier loading socket-bind at every boot and
#            restrict-fs failing to under lockdown (#261). So no unit here can use
#            RestrictFileSystems=, SocketBind*= or RestrictNetworkInterfaces=; none does, and
#            spin's guard loads its own programs. None of the packages the image installs is
#            left out by the profile.
#   SELinux  off as well: the guest kernel's LSMs are lockdown, capability and bpf.
#   pcre2    back on, which stage1 turns off: without it `journalctl --grep` in a workspace says
#            "compiled without pattern matching support". journalctl dlopens libpcre2 only when
#            asked to grep, so a boot never loads it.
#   static   systemd-executor - exec'd once for every unit PID 1 starts - udevd and its
#            helpers, systemctl and journalctl link libsystemd-shared in instead of loading it.
#            PID 1, journald and logind have no such option.
#
# Measured on the bench fleet, 20 boots of each interleaved, p50 ms of the first SSH login: stage1
# −47, −38 and −47 against the archive's systemd (lab runs 44531842773, 44537549305, 44538877783);
# static over stage1 −10 and −1, within noise, with accept-to-login −7 in both runs. A checkpoint
# restored to the first login: unchanged, its guest part ~50 ms either way (44533227886). Not
# taken: -Db_lto=true (Ubuntu's dpkg-buildflags already compile with -flto=auto, and it built the
# same packages to within a few hundred bytes).
#
# The version gets a +spin1 suffix, so apt prefers it over the archive's, a reader of
# packages.txt can tell the two apart, and image/mkosi.postinst.chroot can hold it.
ARG UBUNTU_IMAGE
FROM ${UBUNTU_IMAGE} AS build

# versions.yaml's systemd-source: the .dsc, by sha256. dpkg-source checks the tarballs it names
# against the checksums inside it, so the pin holds the whole source.
ARG SYSTEMD_SOURCE_VERSION
ARG SYSTEMD_SOURCE_SHA256
ARG JOBS=8
ENV DEBIAN_FRONTEND=noninteractive \
    DEB_BUILD_PROFILES="stage1 nocheck noinsttest nodoc" \
    DEB_BUILD_OPTIONS="nocheck nodoc noautodbgsym"

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

# From the archive's deb-src, which apt checks against the signed Sources index, and then the .dsc
# against the pin; acl because the journal's install step runs setfacl and says "not found"
# without it, harmlessly (`|| :`, src/journal/meson.build). The archive keeps only the version
# resolute publishes now: once Ubuntu moves
# systemd, this fails at the download, which is when the pin has to move with it (task
# refresh:systemd proposes it). Launchpad keeps every upload, versions.yaml's source, but its
# librarian answered 502 to a whole build here (2026-11-19).
RUN sed -i 's/^Types: deb$/Types: deb deb-src/' /etc/apt/sources.list.d/ubuntu.sources && \
    apt-get update && \
    apt-get install -y --no-install-recommends dpkg-dev devscripts acl

WORKDIR /build
RUN apt-get source --download-only "systemd=${SYSTEMD_SOURCE_VERSION}" && \
    echo "${SYSTEMD_SOURCE_SHA256}  systemd_${SYSTEMD_SOURCE_VERSION}.dsc" | sha256sum -c - && \
    dpkg-source -x "systemd_${SYSTEMD_SOURCE_VERSION}.dsc" src && test -f src/debian/rules && \
    apt-get build-dep -y --build-profiles="${DEB_BUILD_PROFILES// /,}" "systemd=${SYSTEMD_SOURCE_VERSION}" && \
    apt-get install -y --no-install-recommends libpcre2-dev

# The options after the packaging's own, which meson takes over them: debian/rules hands
# $(CONFFLAGS_UPSTREAM) last and never sets it. meson-took.py then asks the configured build
# whether it took each, so a misspelt option is a failed build and not stage1 under another name.
COPY image/systemd/meson-took.py /build/meson-took.py
WORKDIR /build/src
RUN extra="-Dpcre2=enabled -Dlink-executor-shared=false -Dlink-udev-shared=false -Dlink-systemctl-shared=false -Dlink-journalctl-shared=false" && \
    grep -q -- '-Dselinux=enabled' debian/rules && \
    sed -i 's/-Dselinux=enabled/-Dselinux=disabled/' debian/rules && \
    DEBFULLNAME="spin-machine" DEBEMAIL="noreply@spin-stack.invalid" \
        dch --newversion "${SYSTEMD_SOURCE_VERSION}+spin1" --distribution resolute \
            "Rebuilt by spin-machine: the stage1 profile, SELinux off, ${extra}." && \
    CONFFLAGS_UPSTREAM="${extra}" dpkg-buildpackage -b -uc -us -j"${JOBS}" && \
    meson introspect --buildoptions obj-* | python3 /build/meson-took.py ${extra}

# What came out: every package the image installs, at the new version; libsystemd-shared without
# SELinux, AppArmor, audit or libcrypto; an executor that loads no libsystemd at all; and a
# journalctl that greps - run, not read about, over an empty journal directory.
RUN mkdir -p /debs && cp /build/*.deb /debs/ && \
    for p in systemd systemd-sysv udev libsystemd0 libsystemd-shared libudev1 libpam-systemd; do \
        ls /debs/${p}_*+spin1_*.deb > /dev/null || { echo "ERROR: no ${p} built"; exit 1; }; \
    done && \
    dpkg-deb -x /debs/libsystemd-shared_*.deb /tmp/shared && \
    so=$(find /tmp/shared -name 'libsystemd-shared-*.so') && test -n "$so" && \
    ! ldd "$so" | grep -E 'libselinux|libapparmor|libaudit|libcrypto' && \
    dpkg-deb -x /debs/systemd_*.deb /tmp/systemd && \
    ! ldd /tmp/systemd/usr/lib/systemd/systemd-executor | grep libsystemd && \
    mkdir -p /tmp/no-journal && \
    grep_out=$(/tmp/systemd/usr/bin/journalctl -D /tmp/no-journal --grep x 2>&1) ; \
    case "$grep_out" in *"No entries"*) ;; *) echo "ERROR: journalctl --grep: $grep_out"; exit 1 ;; esac && \
    echo "OK: $(ls /debs | wc -l) packages, libsystemd-shared without the LSMs it was built without, the executor static, journalctl --grep"

FROM scratch AS debs
COPY --from=build /debs/ /
