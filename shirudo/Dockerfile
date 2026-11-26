# shirudo, the one a release ships: the static binary a guest runs, built from the commit the
# kernel's shirudo hooks come from (versions.yaml, `shirudo`; kernel/patches 0026-0031), so a
# release carries the hooks and the shirudo that needs them, never one without the other.
#
# Built as shirudo builds its own target (`make target`, its Makefile.target-deps and
# Makefile.config): a static PIE with no interpreter, OpenSSL, libcurl and TPM2-TSS built from
# source beside it, and libelf, zlib, zstd, LuaJIT and glibc from Ubuntu's static archives. In
# shirudo's own build environment (testing/container at the pin: Ubuntu 26.04, the packages it
# lists, the clang its install-clang.sh installs) and not Alpine's, where every other
# static binary here is built: musl is not what shirudo builds or tests against.
#
# The BPF objects shirudo embeds are compiled against the BTF of the kernel built beside it
# (the `kernel` build context, _output/kernel): left to itself shirudo's Makefile reads the
# build host's /sys/kernel/btf/vmlinux, and the binary would depend on whichever runner made it.
#
# Every tarball is checked against versions.yaml's sha256 and TPM2-TSS against its commit before
# shirudo's Makefile sees it: placed where Makefile.target-deps looks, it downloads nothing. The
# versions it builds are shirudo's, and the build refuses a pin that names other ones.
ARG ALPINE_IMAGE
ARG UBUNTU_IMAGE

FROM ${ALPINE_IMAGE} AS ca

# Ubuntu's archive as it stood on UBUNTU_SNAPSHOT, never the image's live mirrors, so two builds
# of one commit link the same static archives. snapshot.ubuntu.com serves only HTTPS and the image
# has no CA certificates until ca-certificates is installed from this very archive, so that first
# fetch trusts Alpine's bundle; every package is still checked against Ubuntu's keyring.
FROM ${UBUNTU_IMAGE} AS ubuntu
ARG UBUNTU_SNAPSHOT
ENV DEBIAN_FRONTEND=noninteractive
RUN rm /etc/apt/sources.list.d/ubuntu.sources && cat > /etc/apt/sources.list.d/snapshot.sources <<EOF
Types: deb
URIs: https://snapshot.ubuntu.com/ubuntu/${UBUNTU_SNAPSHOT}
Suites: resolute resolute-updates resolute-security
Components: main universe
Signed-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg
EOF
RUN --mount=type=bind,from=ca,source=/etc/ssl/certs/ca-certificates.crt,target=/run/alpine-ca.crt \
    apt-get -o Acquire::https::CAInfo=/run/alpine-ca.crt update && \
    apt-get -o Acquire::https::CAInfo=/run/alpine-ca.crt install -y --no-install-recommends ca-certificates

# The clang and LLVM shirudo's install-clang.sh names (its LLVM=) as LLVM releases them, by
# sha256: apt.llvm.org, where shirudo's CI takes them on Ubuntu, keeps only its newest build. Only
# what shirudo runs is unpacked, the tools install-clang.sh links and clang's own headers, and
# --long because LLVM compresses with a 1 GiB window, which zstd refuses by default.
FROM ubuntu AS llvm
ARG LLVM_VERSION
ARG LLVM_SHA256
WORKDIR /llvm
RUN apt-get install -y --no-install-recommends curl zstd && \
    curl -fsSL "https://github.com/llvm/llvm-project/releases/download/llvmorg-${LLVM_VERSION}/LLVM-${LLVM_VERSION}-Linux-X64.tar.zst" -o llvm.tar.zst && \
    echo "${LLVM_SHA256}  llvm.tar.zst" | sha256sum -c - && \
    mkdir /opt/llvm && \
    tar -I 'zstd -d --long=31' -xf llvm.tar.zst -C /opt/llvm --strip-components=1 --wildcards \
        '*/bin/clang' '*/bin/clang-[0-9]*' '*/bin/llvm-strip' '*/bin/llvm-objcopy' '*/bin/llvm-symbolizer' '*/lib/clang/*'

FROM ubuntu AS build
ARG SHIRUDO_SOURCE
ARG SHIRUDO_COMMIT
ARG OPENSSL_VERSION
ARG OPENSSL_SHA256
ARG CURL_VERSION
ARG CURL_SHA256
ARG TPM2_TSS_SOURCE
ARG TPM2_TSS_VERSION
ARG TPM2_TSS_COMMIT
ARG LLVM_VERSION
RUN apt-get install -y --no-install-recommends curl git
WORKDIR /src/shirudo
RUN git init -q . && git fetch -q --depth 1 "${SHIRUDO_SOURCE}" "${SHIRUDO_COMMIT}" && git checkout -q FETCH_HEAD && \
    test "$(git rev-parse HEAD)" = "${SHIRUDO_COMMIT}"
RUN grep -qx "LLVM=${LLVM_VERSION%%.*}" .github/scripts/install-clang.sh || \
      { echo "shirudo ${SHIRUDO_COMMIT} does not build with LLVM ${LLVM_VERSION}, which versions.yaml pins:"; grep '^LLVM=' .github/scripts/install-clang.sh; exit 1; }
RUN apt-get install -y --no-install-recommends $(cat testing/container/packages) && \
    rm -rf /var/lib/apt/lists/*
COPY --from=llvm /opt/llvm /opt/llvm
RUN for tool in clang llvm-strip llvm-objcopy llvm-symbolizer; do ln -sf "/opt/llvm/bin/$tool" "/usr/bin/$tool"; done && \
    test "$(clang -dumpversion)" = "${LLVM_VERSION}"
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
# bump fails here, by name, instead of entering the release unpinned. SOURCE_DATE_EPOCH is the
# commit's: OpenSSL writes its build's wall clock into the binary ("built on:") otherwise, and two
# builds of one commit differed by it (#771).
RUN --network=none --mount=type=bind,from=kernel,source=vmlinux,target=/kernel/vmlinux \
    SOURCE_DATE_EPOCH="$(git log -1 --format=%ct)" make -j"$(nproc)" BPF_VMLINUX_BTF=/kernel/vmlinux target
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

# The systemd initrd shirudo's init runs in, made by shirudo-gen-initrd with dracut from the
# machine's own base image: its files are the `rootfs` build context (boot/shirudo_test.go), so the
# initrd's systemd, udev and tpm2-tss are the image's, the +spin1 build with TPM2 (#767, #777).
# dracut and tpm2-tools, which the image leaves out, come from the snapshot the other stages take,
# in place of the image's own sources.
FROM scratch AS initrd
COPY --from=rootfs / /
COPY --from=ubuntu /etc/apt/sources.list.d/snapshot.sources /etc/apt/sources.list.d/
ENV DEBIAN_FRONTEND=noninteractive
RUN rm /etc/apt/sources.list.d/resolute.sources /etc/apt/sources.list.d/docker.sources && \
    apt-get update && \
    apt-get install -y --no-install-recommends dracut-core tpm2-tools cpio zstd file && \
    test -e /usr/lib/systemd/system/systemd-pcrphase-initrd.service && \
    dpkg-query -W -f '${Version}' systemd | grep -q '+spin' && \
    rm -rf /var/lib/apt/lists/*
COPY --from=build /src/shirudo/tools/gen-initrd /opt/gen-initrd
COPY --from=build /src/shirudo/systemd /opt/shirudo-units
