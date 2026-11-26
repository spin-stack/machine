# shirudo, the one a release ships: the static binary a guest runs, built from the commit the
# kernel's shirudo hooks come from (versions.yaml, `shirudo`; kernel/patches 0026-0031), so a
# release carries the hooks and the shirudo that needs them, never one without the other.
#
# Built as shirudo builds its own target (`make target`, its Makefile.target-deps and
# Makefile.config): a static PIE with no interpreter, OpenSSL, libcurl and TPM2-TSS built from
# source beside it, and libelf, zlib, zstd, LuaJIT and glibc from Ubuntu's static archives. In
# shirudo's own build environment (testing/container at the pin: Ubuntu 26.04, the packages it
# lists, clang 23 from apt.llvm.org by its install-clang.sh) and not Alpine's, where every other
# static binary here is built: musl is not what shirudo builds or tests against.
#
# The BPF objects shirudo embeds are compiled against the BTF of the kernel built beside it
# (the `kernel` build context, _output/kernel): left to itself shirudo's Makefile reads the
# build host's /sys/kernel/btf/vmlinux, and the binary would depend on whichever runner made it.
#
# Every tarball is checked against versions.yaml's sha256 and TPM2-TSS against its commit before
# shirudo's Makefile sees it: placed where Makefile.target-deps looks, it downloads nothing. The
# versions it builds are shirudo's, and the build refuses a pin that names other ones.
ARG UBUNTU_IMAGE

FROM ${UBUNTU_IMAGE} AS build
ARG SHIRUDO_SOURCE
ARG SHIRUDO_COMMIT
ARG OPENSSL_VERSION
ARG OPENSSL_SHA256
ARG CURL_VERSION
ARG CURL_SHA256
ARG TPM2_TSS_SOURCE
ARG TPM2_TSS_VERSION
ARG TPM2_TSS_COMMIT
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl git
WORKDIR /src/shirudo
RUN git init -q . && git fetch -q --depth 1 "${SHIRUDO_SOURCE}" "${SHIRUDO_COMMIT}" && git checkout -q FETCH_HEAD && \
    test "$(git rev-parse HEAD)" = "${SHIRUDO_COMMIT}"
RUN apt-get install -y --no-install-recommends $(cat testing/container/packages) && \
    sh .github/scripts/install-clang.sh && \
    rm -rf /var/lib/apt/lists/*
RUN for v in OPENSSL:"${OPENSSL_VERSION}" CURL:"${CURL_VERSION}" TSS:"${TPM2_TSS_VERSION}"; do \
      grep -qx "${v%%:*}_VERSION ?= ${v#*:}" Makefile.target-deps || \
        { echo "shirudo ${SHIRUDO_COMMIT} does not build ${v%%:*} ${v#*:}, which versions.yaml pins:"; grep '_VERSION ?=' Makefile.target-deps; exit 1; }; \
    done
RUN mkdir -p build/target-deps/src && cd build/target-deps/src && \
    curl -fsSL "https://github.com/openssl/openssl/releases/download/openssl-${OPENSSL_VERSION}/openssl-${OPENSSL_VERSION}.tar.gz" -o "openssl-${OPENSSL_VERSION}.tar.gz" && \
    curl -fsSL "https://curl.se/download/curl-${CURL_VERSION}.tar.xz" -o "curl-${CURL_VERSION}.tar.xz" && \
    printf '%s  %s\n' "${OPENSSL_SHA256}" "openssl-${OPENSSL_VERSION}.tar.gz" "${CURL_SHA256}" "curl-${CURL_VERSION}.tar.xz" | sha256sum -c - && \
    git init -q "tpm2-tss-${TPM2_TSS_VERSION}" && cd "tpm2-tss-${TPM2_TSS_VERSION}" && \
    git fetch -q --depth 1 "${TPM2_TSS_SOURCE}" "${TPM2_TSS_COMMIT}" && git checkout -q FETCH_HEAD && \
    test "$(git rev-parse HEAD)" = "${TPM2_TSS_COMMIT}"
# Without a network: every input is in place above, and a download shirudo's Makefile adds after a
# bump fails here, by name, instead of entering the release unpinned.
RUN --network=none --mount=type=bind,from=kernel,source=vmlinux,target=/kernel/vmlinux \
    make -j"$(nproc)" BPF_VMLINUX_BTF=/kernel/vmlinux target
# What a guest is handed: static, no interpreter, nothing it would look for in the guest's
# /lib. shirudo's own link already checks this (check-static-target.sh); this is the binary
# asked again, as the release receives it. Stripped by the llvm-strip shirudo-gen-runtime strips
# it with, so the runtime image an operator makes carries this file byte for byte.
RUN readelf -lW shirudo-target | grep -q ' INTERP ' && { echo "shirudo-target asks for a loader"; exit 1; }; \
    readelf -dW shirudo-target | grep -Eq '\((NEEDED|RPATH|RUNPATH)\)' && { echo "shirudo-target names a shared library"; exit 1; }; \
    mkdir -p /out/guest && llvm-strip -o /out/guest/shirudo shirudo-target && chmod 0755 /out/guest/shirudo
# The Ubuntu packages whose static archives the binary links, each with its version, as packages.txt
# lists the image's: the SBOM's, and the copyright files of each beside the licences of what was
# built from source. Asked of dpkg for the archives the link took: the system archives
# Makefile.target-deps staged (the copies identical to Ubuntu's), glibc's and gcc's runtime.
RUN set -e; \
    { for a in build/target-deps/lib/*.a; do \
        f=/usr/lib/x86_64-linux-gnu/$(basename "$a"); if [ -f "$f" ] && cmp -s "$a" "$f"; then echo "$f"; fi; \
      done; \
      for f in libc.a libm.a libgcc_eh.a; do realpath "$(gcc -print-file-name="$f")"; done; realpath "$(gcc -print-libgcc-file-name)"; \
    } > /tmp/archives; \
    pkgs=$(xargs dpkg -S < /tmp/archives | cut -d: -f1 | sort -u); \
    for want in libc6-dev libelf-dev; do echo "$pkgs" | grep -qx "$want" || { echo "the link took nothing of $want"; exit 1; }; done; \
    mkdir -p /out/sbom && dpkg-query -W -f '${Package} ${Version} ${Architecture}\n' ${pkgs} > /out/sbom/shirudo.dpkg; \
    l=/out/shirudo-licenses.txt; \
    printf 'The licences of the code linked into guest/shirudo.\n' > "$l"; \
    for f in "shirudo ${SHIRUDO_COMMIT}:LICENSE" "libbpf, vendored by shirudo, under the BSD-2-Clause of the text above:vendor/libbpf/README.md" \
             "OpenSSL ${OPENSSL_VERSION}:build/target-deps/src/openssl-${OPENSSL_VERSION}/LICENSE.txt" \
             "curl ${CURL_VERSION}:build/target-deps/src/curl-${CURL_VERSION}/COPYING" \
             "TPM2-TSS ${TPM2_TSS_VERSION}:build/target-deps/src/tpm2-tss-${TPM2_TSS_VERSION}/LICENSE"; do \
      printf '\n==> %s\n\n' "${f%%:*}" >> "$l"; cat "${f#*:}" >> "$l"; \
    done; \
    for p in ${pkgs}; do \
      printf '\n==> %s, Ubuntu\n\n' "$(dpkg-query -W -f '${Package} ${Version}' "$p")" >> "$l"; \
      cat "/usr/share/doc/$p/copyright" >> "$l"; \
    done

FROM scratch AS extract
COPY --from=build /out/ /

# What a release's check of shirudo (boot:shirudo) runs on the host, from the same commit and
# against the same kernel as the binary it checks: the operator's shirudo and shirudo-build, which
# make a key, sign the images and the upatch, and shirudo-gen-runtime. Never shipped: the release
# ships what a guest runs, and an operator builds its own tools from the commit SOURCES names.
FROM build AS operator
RUN --network=none --mount=type=bind,from=kernel,source=vmlinux,target=/kernel/vmlinux \
    make -j"$(nproc)" BPF_VMLINUX_BTF=/kernel/vmlinux all shirudo-build
ENV PATH=/src/shirudo:/src/shirudo/tools/shirudo-build:/src/shirudo/tools/gen-image:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

# The systemd initrd shirudo's init runs in, made by shirudo-gen-initrd with dracut from Ubuntu
# 26.04's own systemd: the image's systemd is built without TPM2 (image/systemd), so an initrd
# made from it has no systemd-pcrphase-initrd.service, which shirudo-gen-initrd --check requires.
FROM ${UBUNTU_IMAGE} AS initrd
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
        dracut-core systemd udev kmod tpm2-tools libtss2-tcti-device0 \
        cpio zstd file e2fsprogs util-linux mount && \
    rm -rf /var/lib/apt/lists/*
COPY --from=build /src/shirudo/tools/gen-initrd /opt/gen-initrd
COPY --from=build /src/shirudo/systemd /opt/shirudo-units
