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

# The Ubuntu packages the operator half installs in a guest of the release that its image does
# not carry (docs/shirudo.md, "The operator half"): dracut and tpm2-tools for the initrd,
# cryptsetup's veritysetup for the images, binutils' readelf for shirudo-gen-runtime, zstd and cpio,
# python3 for kernel-obs's rodata generator. From the same snapshot as the build, as a flat apt
# repository: resolved against a bare Ubuntu, so a package the guest's image already has is in it
# too, and apt in the guest keeps the image's own (shirudo/operator-install). The names asked for
# are `wanted`, which the install reads; the packages' versions, the SBOM's.
FROM ubuntu AS operator-debs
ENV LC_ALL=C
WORKDIR /debs
RUN echo dracut-core tpm2-tools zstd cpio cryptsetup-bin binutils python3 > wanted && \
    apt-get install -y --download-only --no-install-recommends $(cat wanted) && \
    mv /var/cache/apt/archives/*.deb . && \
    for d in *.deb; do \
      dpkg-deb -f "$d"; \
      printf 'Filename: ./%s\nSize: %s\nSHA256: %s\n\n' "$d" "$(stat -c %s "$d")" "$(sha256sum "$d" | cut -d' ' -f1)"; \
    done > Packages && \
    mkdir -p /out/sbom && \
    for d in *.deb; do dpkg-deb --show --showformat '${Package} ${Version} ${Architecture}\n' "$d"; done > /out/sbom/shirudo-operator.dpkg

# The operator half, which a release ships as a tarball of its own beside the main one
# (docs/shirudo.md): shirudo-build, static as guest/shirudo is and from the same objects' commit and
# dependencies, the scripts, dracut module, units, headers and upatch sources around it
# (shirudo/operator.mk), the clang guest/shirudo's BPF objects are compiled with and its own
# headers, which shirudo-build execs at the path compiled in, and llvm-strip, which
# shirudo-gen-runtime strips guest/shirudo with. It runs in a guest of the release, as root
# (shirudo/operator-install); the main tarball, and every file a machine boots, is untouched by it.
FROM build AS operator
ARG LLVM_VERSION
ARG LLVM_LICENSE_VERSION
ARG LLVM_LICENSE_SHA256
COPY shirudo/operator.mk shirudo/operator-install /spin/
RUN --network=none --mount=type=bind,from=kernel,source=vmlinux,target=/kernel/vmlinux \
    SOURCE_DATE_EPOCH="$(git log -1 --format=%ct)" make -f Makefile.shirudo -f /spin/operator.mk -j"$(nproc)" \
      PROFILE=target CONFIG_MK=config-target.mk BPF_VMLINUX_BTF=/kernel/vmlinux \
      libexecdir=/usr/local/libexec sharedir=/usr/local/share \
      SHIRUDO_CLANG_PATH=/usr/local/libexec/shirudo/llvm/bin/clang OPERATOR=/out/shirudo-operator operator
RUN set -e; o=/out/shirudo-operator; l=$o/libexec/shirudo/llvm; \
    mkdir -p "$l/bin" "$l/lib/clang/${LLVM_VERSION%%.*}"; \
    llvm-strip -o "$l/bin/clang" /opt/llvm/bin/clang-${LLVM_VERSION%%.*}; \
    llvm-strip -o "$l/bin/llvm-objcopy" /opt/llvm/bin/llvm-objcopy; \
    ln -s llvm-objcopy "$l/bin/llvm-strip"; \
    cp -R "/opt/llvm/lib/clang/${LLVM_VERSION%%.*}/include" "$l/lib/clang/${LLVM_VERSION%%.*}/include"; \
    test "$("$l/bin/clang" -dumpversion)" = "${LLVM_VERSION}"; \
    install -m 0755 /spin/operator-install "$o/install"
COPY --from=operator-debs /debs /out/shirudo-operator/debs
COPY --from=operator-debs /out/sbom /out/sbom
# The licences of what the operator half carries beyond the debs, whose own are inside them: what
# shirudo-build links, which is what guest/shirudo links, and LLVM's, which its release tarball
# does not carry, from the tag's source (versions.yaml, llvm-license).
RUN set -e; \
    test "${LLVM_LICENSE_VERSION}" = "${LLVM_VERSION}" || { echo "llvm-license ${LLVM_LICENSE_VERSION} is not llvm ${LLVM_VERSION}'s"; exit 1; }; \
    curl -fsSL "https://raw.githubusercontent.com/llvm/llvm-project/llvmorg-${LLVM_LICENSE_VERSION}/llvm/LICENSE.TXT" -o /tmp/llvm-license && \
    echo "${LLVM_LICENSE_SHA256}  /tmp/llvm-license" | sha256sum -c - && \
    { cat /out/shirudo-licenses.txt; printf '\n==> LLVM %s: clang, llvm-objcopy and clang'"'"'s headers\n\n' "${LLVM_VERSION}"; cat /tmp/llvm-license; } \
      > /out/shirudo-operator/licenses.txt

FROM scratch AS extract
COPY --from=operator /out/ /
