# internal/lab's fio edit: fio compiled from the release tarball versions.yaml pins, for a lab
# overlay and never for the base image. The context is the directory holding the tarball, which
# fioEdit fetched and checked; it is checked again here, on the bytes that are compiled.
#
# On the Ubuntu the guest runs, so the binary links the guest's own libc and nothing else: the
# io probe's jobs use io_uring, which fio reaches through its own system calls, not a library.
ARG UBUNTU_IMAGE
FROM ${UBUNTU_IMAGE} AS build

ARG FIO_VERSION
ARG FIO_SHA256
ENV DEBIAN_FRONTEND=noninteractive

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

RUN apt-get update && \
    apt-get install -y --no-install-recommends gcc libc6-dev make

COPY fio-${FIO_VERSION}.tar.gz /build/source.tar.gz
WORKDIR /build
RUN echo "${FIO_SHA256}  source.tar.gz" | sha256sum -c - && \
    mkdir src && tar -xzf source.tar.gz -C src --strip-components=1 && \
    cd src && ./configure --disable-native && \
    make -j"$(nproc)" && \
    ./fio --enghelp | grep -qx '[[:space:]]*io_uring' && \
    test "$(./fio --version)" = "fio-${FIO_VERSION}" && \
    install -D -m 0755 fio /out/fio

FROM scratch AS bin
COPY --from=build /out/ /
