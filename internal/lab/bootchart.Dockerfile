# internal/lab's bootchart edit: systemd-bootchart compiled from spin-stack/systemd-bootchart at the
# commit versions.yaml pins, for a lab overlay and never for the base image. The context is the
# directory holding the commit's tarball, which bootchartEdit fetched and checked; it is checked
# again here, on the bytes that are compiled.
#
# On the Ubuntu the guest runs, so the binary links the guest's own libc. The paths are the ones
# the edit relies on: the configuration at /etc/systemd/bootchart.conf, and /usr/lib/systemd/systemd
# as the init it execs once its sampler has forked.
ARG UBUNTU_IMAGE
FROM ${UBUNTU_IMAGE} AS build

ARG SYSTEMD_BOOTCHART_VERSION
ARG SYSTEMD_BOOTCHART_SHA256
ENV DEBIAN_FRONTEND=noninteractive

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

RUN apt-get update && \
    apt-get install -y --no-install-recommends autoconf automake libtool pkg-config gcc libc6-dev make

COPY ${SYSTEMD_BOOTCHART_VERSION}.tar.gz /build/source.tar.gz
WORKDIR /build
RUN echo "${SYSTEMD_BOOTCHART_SHA256}  source.tar.gz" | sha256sum -c - && \
    mkdir src && tar -xzf source.tar.gz -C src --strip-components=1 && \
    cd src && ./autogen.sh && \
    ./configure --prefix=/usr --sysconfdir=/etc --with-rootprefix=/usr --without-libsystemd --disable-man && \
    make -j"$(nproc)" && \
    grep -aqF /etc/systemd/bootchart.conf systemd-bootchart && \
    help=$(./systemd-bootchart --help) && \
    grep -qF '[/usr/lib/systemd/systemd]' <<<"$help" && \
    grep -qF -- --control-group <<<"$help" && \
    install -D -m 0755 systemd-bootchart /out/systemd-bootchart

FROM scratch AS bin
COPY --from=build /out/ /
