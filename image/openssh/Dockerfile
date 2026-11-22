# The OpenSSH the base image installs: Ubuntu's own, rebuilt from its source package with the
# patches beside this file, because every first login, cold or restored, authenticates against it.
#
# The source is Ubuntu's source package of the version the image would otherwise install, by its
# .dsc: its patches, and so its security fixes, are Ubuntu's, and so is the build, profiles and
# configure flags included. What changes is the patches here, applied after Ubuntu's:
#
#   userauth-delay-failures.patch   sshd keeps OpenSSH's userauth minimum time (5 ms plus 0-4.2 ms
#            a user) for a request that fails, and skips it for one that authenticated or for a
#            public key query it answered with PK_OK, which had told the client its outcome
#            already. Both sat on every login's path, ~12 ms of a restored first login (#457).
#
# The version gets a +spin1 suffix, so apt prefers it over the archive's, a reader of packages.txt
# can tell the two apart, and image/mkosi.postinst.chroot holds it.
ARG UBUNTU_IMAGE
FROM ${UBUNTU_IMAGE} AS build

# versions.yaml's openssh-source: the .dsc, by sha256, its version without the epoch (1:), as the
# .dsc's file name has it. dpkg-source checks the tarballs it names against the checksums inside
# it, so the pin holds the whole source.
ARG OPENSSH_SOURCE_VERSION
ARG OPENSSH_SOURCE_SHA256
ARG JOBS=8
# No udebs and no ssh-askpass-gnome, which nothing installs and which would pull GTK into the
# build; nocheck because the regression suite starts sshds on the build host's ports.
ENV DEBIAN_FRONTEND=noninteractive \
    DEB_BUILD_PROFILES="noudeb pkg.openssh.nognome nocheck" \
    DEB_BUILD_OPTIONS="nocheck nodoc noautodbgsym"

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

# From the archive's deb-src, which apt checks against the signed Sources index, and then the .dsc
# against the pin. The archive keeps only the version resolute publishes now: once Ubuntu moves
# openssh, this fails at the download, which is when the pin has to move with it, as
# image/systemd's does.
RUN sed -i 's/^Types: deb$/Types: deb deb-src/' /etc/apt/sources.list.d/ubuntu.sources && \
    apt-get update && \
    apt-get install -y --no-install-recommends dpkg-dev devscripts

WORKDIR /build
RUN apt-get source --download-only "openssh=1:${OPENSSH_SOURCE_VERSION}" && \
    echo "${OPENSSH_SOURCE_SHA256}  openssh_${OPENSSH_SOURCE_VERSION}.dsc" | sha256sum -c - && \
    dpkg-source -x "openssh_${OPENSSH_SOURCE_VERSION}.dsc" src && test -f src/debian/rules && \
    apt-get build-dep -y --build-profiles="${DEB_BUILD_PROFILES// /,}" "openssh=1:${OPENSSH_SOURCE_VERSION}"

# Every patch in this directory, in name order, none of them allowed to apply with fuzz: a hunk
# that lands somewhere else in a moved auth2.c is a different change.
COPY image/openssh/*.patch /build/patches/
WORKDIR /build/src
RUN for p in /build/patches/*.patch; do patch -p1 --fuzz=0 < "$p"; done && \
    grep -q 'm == &method_pubkey' auth2.c && \
    DEBFULLNAME="spin-machine" DEBEMAIL="noreply@spin-stack.invalid" \
        dch --newversion "1:${OPENSSH_SOURCE_VERSION}+spin1" --distribution resolute \
            "Rebuilt by spin-machine with $(cd /build/patches && echo *.patch)." && \
    dpkg-buildpackage -b -uc -us -j"${JOBS}"

# What came out: every package the image installs, at the new version, and an sshd-auth, the
# process that runs userauth, that says it is this build in the version it sends every client.
RUN mkdir -p /debs && cp /build/*.deb /debs/ && \
    for p in openssh-server openssh-client openssh-sftp-server; do \
        ls /debs/${p}_*+spin1_*.deb > /dev/null || { echo "ERROR: no ${p} built"; exit 1; }; \
    done && \
    dpkg-deb -x /debs/openssh-server_*.deb /tmp/server && \
    said="OpenSSH_${OPENSSH_SOURCE_VERSION%%-*} Ubuntu-${OPENSSH_SOURCE_VERSION#*-}+spin1" && \
    if ! grep -aqF "$said" /tmp/server/usr/lib/openssh/sshd-auth; then \
        echo "ERROR: sshd-auth does not say $said"; exit 1; \
    fi && \
    echo "OK: $(ls /debs | wc -l) packages, sshd-auth says $said"

FROM scratch AS debs
COPY --from=build /debs/ /
