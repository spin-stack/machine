# docs/image.md, "The rebuilt packages"
ARG UBUNTU_IMAGE
FROM ${UBUNTU_IMAGE} AS build

ARG OPENSSH_SOURCE_VERSION
ARG OPENSSH_SOURCE_SHA256
ARG JOBS=8
ENV DEBIAN_FRONTEND=noninteractive \
    DEB_BUILD_PROFILES="noudeb pkg.openssh.nognome nocheck" \
    DEB_BUILD_OPTIONS="nocheck nodoc noautodbgsym"

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

RUN sed -i 's/^Types: deb$/Types: deb deb-src/' /etc/apt/sources.list.d/ubuntu.sources && \
    apt-get update && \
    apt-get install -y --no-install-recommends dpkg-dev devscripts

WORKDIR /build
RUN apt-get source --download-only "openssh=1:${OPENSSH_SOURCE_VERSION}" && \
    echo "${OPENSSH_SOURCE_SHA256}  openssh_${OPENSSH_SOURCE_VERSION}.dsc" | sha256sum -c - && \
    dpkg-source -x "openssh_${OPENSSH_SOURCE_VERSION}.dsc" src && test -f src/debian/rules && \
    apt-get build-dep -y --build-profiles="${DEB_BUILD_PROFILES// /,}" "openssh=1:${OPENSSH_SOURCE_VERSION}"

COPY image/openssh/*.patch /build/patches/
WORKDIR /build/src
RUN for p in /build/patches/*.patch; do patch -p1 --fuzz=0 < "$p"; done && \
    grep -q 'm == &method_pubkey' auth2.c && \
    DEBFULLNAME="spin-machine" DEBEMAIL="noreply@spin-stack.invalid" \
        dch --newversion "1:${OPENSSH_SOURCE_VERSION}+spin1" --distribution resolute \
            "Rebuilt by spin-machine with $(cd /build/patches && echo *.patch)." && \
    dpkg-buildpackage -b -uc -us -j"${JOBS}"

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
