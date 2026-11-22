# docs/image.md, "The toolchain"
ARG UBUNTU_IMAGE
FROM ${UBUNTU_IMAGE}

ENV DEBIAN_FRONTEND=noninteractive

RUN rm -f /etc/apt/apt.conf.d/docker-clean && \
    echo 'Binary::apt::APT::Keep-Downloaded-Packages "true";' > /etc/apt/apt.conf.d/keep-cache

RUN --mount=type=cache,sharing=locked,id=image-aptlib,target=/var/lib/apt \
    --mount=type=cache,sharing=locked,id=image-aptcache,target=/var/cache/apt \
    apt-get update && apt-get install --no-install-recommends -y \
        ubuntu-keyring \
        curl \
        apt \
        apt-utils \
        dpkg \
        ca-certificates \
        qemu-utils \
        python3 \
        xz-utils \
        zstd && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

ARG MKOSI_VERSION
ARG MKOSI_SHA256
RUN --mount=type=cache,sharing=locked,id=image-mkosi-src,target=/var/cache/mkosi-src \
    tarball="/var/cache/mkosi-src/mkosi-${MKOSI_VERSION}.tar.gz" && \
    if [ ! -f "$tarball" ]; then \
        curl -fsSL --retry 5 --retry-all-errors -o "$tarball" "https://github.com/systemd/mkosi/archive/refs/tags/v${MKOSI_VERSION}.tar.gz"; \
    fi && \
    echo "${MKOSI_SHA256}  $tarball" | sha256sum -c - && \
    mkdir -p /opt/mkosi && \
    tar -xf "$tarball" -C /opt/mkosi --strip-components=1 && \
    ln -s /opt/mkosi/bin/mkosi /usr/local/bin/mkosi && \
    test "$(mkosi --version)" = "mkosi ${MKOSI_VERSION}"

ARG TASK_VERSION
ARG TASK_SHA256_LINUX_AMD64
ARG GIT_LFS_VERSION
ARG GIT_LFS_SHA256
ARG BUILDKIT_VERSION
ARG BUILDKIT_SHA256
ENV TASK_VERSION=${TASK_VERSION} TASK_SHA256=${TASK_SHA256_LINUX_AMD64} \
    GIT_LFS_VERSION=${GIT_LFS_VERSION} GIT_LFS_SHA256=${GIT_LFS_SHA256} \
    BUILDKIT_VERSION=${BUILDKIT_VERSION} BUILDKIT_SHA256=${BUILDKIT_SHA256}

COPY image/mkosi.conf image/mkosi.postinst.chroot /work/
COPY image/mkosi.extra /work/mkosi.extra
COPY image/deviations.py image/deviations.txt /usr/local/share/spin-image/
COPY image/build.sh /usr/local/bin/build-base-image
RUN chmod +x /usr/local/bin/build-base-image /work/mkosi.postinst.chroot \
             /work/mkosi.extra/usr/local/lib/spin-base/*.sh

WORKDIR /work
ENTRYPOINT ["/usr/local/bin/build-base-image"]
