# EXPERIMENT (branch systemd-stage1): Ubuntu's own systemd, rebuilt from its deb-src with less in
# it, to measure what PID 1, udevd and journald stop doing at boot. Not a release input until the
# numbers say so; see docs/log.md.
#
# The source is Ubuntu's source package of the version the image would otherwise install, from
# the archive's signed Sources index: its patches, and so its security fixes, are Ubuntu's. What
# changes is the build profile:
#
#   stage1   Debian's own reduced build, maintained in debian/rules and debian/control: no audit,
#            cryptsetup, coredump, AppArmor, OpenSSL, TPM2, p11-kit, FIDO2, PCRE2, homed, userdb,
#            importd, curl, sysupdate, boot loader or ukify, and no BPF framework. That last one
#            is what PID 1 spends ~25 ms on in the verifier at every boot (socket-bind, docs/log.md)
#            and what fails to load under lockdown (restrict-fs, #261). None of the packages the
#            image installs is left out by it.
#   SELinux  off as well: the guest kernel's LSMs are lockdown, capability and bpf.
#
# The version gets a +spin1 suffix, so apt prefers it over the archive's and anything reading the
# image's package list can tell the two apart.
ARG UBUNTU_IMAGE
FROM ${UBUNTU_IMAGE}

ARG SYSTEMD_SOURCE_VERSION
ARG JOBS=8
ENV DEBIAN_FRONTEND=noninteractive \
    DEB_BUILD_PROFILES="stage1 nocheck noinsttest nodoc" \
    DEB_BUILD_OPTIONS="nocheck nodoc noautodbgsym"

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

RUN sed -i 's/^Types: deb$/Types: deb deb-src/' /etc/apt/sources.list.d/ubuntu.sources && \
    apt-get update && \
    apt-get install -y --no-install-recommends dpkg-dev devscripts && \
    apt-get build-dep -y --build-profiles="${DEB_BUILD_PROFILES// /,}" "systemd=${SYSTEMD_SOURCE_VERSION}"

WORKDIR /build
RUN apt-get source "systemd=${SYSTEMD_SOURCE_VERSION}" && \
    ln -s "systemd-${SYSTEMD_SOURCE_VERSION%%-*}" src && test -f src/debian/rules

WORKDIR /build/src
RUN grep -q -- '-Dselinux=enabled' debian/rules && \
    sed -i 's/-Dselinux=enabled/-Dselinux=disabled/' debian/rules && \
    DEBFULLNAME="spin-machine" DEBEMAIL="noreply@spin-stack.invalid" \
        dch --newversion "${SYSTEMD_SOURCE_VERSION}+spin1" --distribution resolute \
            "Rebuilt with the stage1 profile and SELinux off (spin-machine experiment)." && \
    dpkg-buildpackage -b -uc -us -j"${JOBS}"

# What came out: the packages the image installs, at the new version, and a PID 1 without the
# BPF framework or SELinux linked in.
RUN mkdir -p /debs && cp /build/*.deb /debs/ && \
    for p in systemd systemd-sysv udev libsystemd0 libsystemd-shared libudev1 libpam-systemd; do \
        f=$(ls /debs/${p}_*+spin1_*.deb) || { echo "ERROR: no ${p} built"; exit 1; }; \
    done && \
    dpkg-deb -x /debs/libsystemd-shared_*.deb /tmp/x && \
    ! find /tmp/x -name 'libsystemd-shared-*.so' -exec ldd {} \; | grep -E 'libselinux|libapparmor|libaudit|libcrypto' && \
    ls -la /debs

FROM scratch AS debs
COPY --from=0 /debs/ /
