# docs/image.md, "The rebuilt packages"
ARG UBUNTU_IMAGE
FROM ${UBUNTU_IMAGE} AS build

ARG SYSTEMD_SOURCE_VERSION
ARG SYSTEMD_SOURCE_SHA256
ENV DEBIAN_FRONTEND=noninteractive \
    DEB_BUILD_PROFILES="stage1 nocheck noinsttest nodoc" \
    DEB_BUILD_OPTIONS="nocheck nodoc noautodbgsym"

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

RUN sed -i 's/^Types: deb$/Types: deb deb-src/' /etc/apt/sources.list.d/ubuntu.sources && \
    apt-get update && \
    apt-get install -y --no-install-recommends dpkg-dev devscripts acl

WORKDIR /build
RUN apt-get source --download-only "systemd=${SYSTEMD_SOURCE_VERSION}" && \
    echo "${SYSTEMD_SOURCE_SHA256}  systemd_${SYSTEMD_SOURCE_VERSION}.dsc" | sha256sum -c - && \
    dpkg-source -x "systemd_${SYSTEMD_SOURCE_VERSION}.dsc" src && test -f src/debian/rules && \
    apt-get build-dep -y --build-profiles="${DEB_BUILD_PROFILES// /,}" "systemd=${SYSTEMD_SOURCE_VERSION}" && \
    apt-get install -y --no-install-recommends libpcre2-dev libtss2-dev libssl-dev python3-pyelftools

COPY image/systemd/meson-took.py /build/meson-took.py
COPY image/systemd/*.patch /build/patches/
WORKDIR /build/src
RUN for p in /build/patches/*.patch; do patch -p1 --fuzz=0 < "$p"; done && \
    grep -q 'unit_file_expand_dropin_names' src/shared/dropin.c
ARG SYSTEMD_EXPERIMENT=""
COPY image/systemd/experiments/ /build/experiments/
RUN if [ -n "${SYSTEMD_EXPERIMENT}" ]; then \
        patch -p1 --fuzz=0 < "/build/experiments/${SYSTEMD_EXPERIMENT}.patch"; \
    fi
RUN extra="-Dpcre2=enabled -Dlink-executor-shared=false -Dlink-udev-shared=false -Dlink-systemctl-shared=false -Dlink-journalctl-shared=false" && \
    grep -q -- '-Dselinux=enabled' debian/rules && \
    sed -i 's/-Dselinux=enabled/-Dselinux=disabled/' debian/rules && \
    DEBFULLNAME="spin-machine" DEBEMAIL="noreply@spin-stack.invalid" \
        dch --newversion "${SYSTEMD_SOURCE_VERSION}+spin1" --distribution resolute \
            "Rebuilt by spin-machine: the stage1 profile, SELinux off, ${extra}, $(cd /build/patches && echo *.patch)." && \
    prev="$(dpkg-parsechangelog --offset 1 --count 1 -SDate)" && \
    sed -i "1,/^ -- /s/^\( -- .*>  \).*/\1${prev}/" debian/changelog && \
    test "$(dpkg-parsechangelog -SDate)" = "${prev}" && \
    CONFFLAGS_UPSTREAM="${extra}" dpkg-buildpackage -b -uc -us -j"$(nproc)" && \
    meson introspect --buildoptions obj-* | python3 /build/meson-took.py ${extra}

COPY image/systemd/tests-passed.py /build/tests-passed.py
RUN tests="test-engine test-load-fragment" && \
    meson configure obj-* -Dtests=true && \
    ninja -C obj-* $tests && \
    meson test -C obj-* --no-rebuild --print-errorlogs $tests && \
    python3 /build/tests-passed.py obj-*/meson-logs/testlog.json $tests

RUN mkdir -p /debs && cp /build/*.deb /debs/ && \
    case "${SYSTEMD_EXPERIMENT}" in tpm2*) (cd /debs && apt-get download libtss2-esys-3.0.2-0t64 libtss2-mu-4.0.1-0t64 libtss2-rc0t64 libtss2-sys1t64 libtss2-tcti-cmd0t64 libtss2-tcti-device0t64 libtss2-tcti-mssim0t64 libtss2-tcti-swtpm0t64 tpm-udev) ;; esac && \
    for p in systemd systemd-sysv udev libsystemd0 libsystemd-shared libudev1 libpam-systemd; do \
        ls /debs/${p}_*+spin1_*.deb > /dev/null || { echo "ERROR: no ${p} built"; exit 1; }; \
    done && \
    dpkg-deb -x /debs/libsystemd-shared_*.deb /tmp/shared && \
    so=$(find /tmp/shared -name 'libsystemd-shared-*.so') && test -n "$so" && \
    ! ldd "$so" | grep -E 'libselinux|libapparmor|libaudit' && \
    dpkg-deb -x /debs/systemd_*.deb /tmp/systemd && \
    ! ldd /tmp/systemd/usr/lib/systemd/systemd-executor | grep libsystemd && \
    mkdir -p /tmp/no-journal && \
    grep_out=$(/tmp/systemd/usr/bin/journalctl -D /tmp/no-journal --grep x 2>&1) ; \
    case "$grep_out" in *"No entries"*) ;; *) echo "ERROR: journalctl --grep: $grep_out"; exit 1 ;; esac && \
    echo "OK: $(ls /debs | wc -l) packages, libsystemd-shared without the LSMs it was built without, the executor static, journalctl --grep"

FROM scratch AS debs
COPY --from=build /debs/ /
