# The Alpine a static part is compiled on - QEMU's, e2fsprogs' - with that part's apk.list
# installed, published to ghcr and pinned by digest in versions.yaml (qemu-builder,
# e2fsprogs-builder). spin-machine-dev refresh builders makes it; nothing else does.
#
# An image and not an `apk add` in each part's Dockerfile, because Alpine's mirror serves only the
# newest build of each package. A build that installed from the mirror had to pin every package
# (the apk.lock each part had until 2026-11-20), and every build the mirror dropped broke the
# lock, so it moved every two or three days - and the `apk add` is the first layer of QEMU's
# build, so each move was QEMU compiled cold, on the pull request and again on main, for a
# libxml2 r2 to r3 nothing links. An image keeps its packages after the mirror drops them: a
# builder moves when a person takes one, and the digest is the lock.
#
# The list goes in with it, so a part's build can say when the tree asks for packages the pinned
# builder was not made with (qemu/Dockerfile, e2fsprogs/Dockerfile).
ARG ALPINE_IMAGE
FROM ${ALPINE_IMAGE}

ARG PART
COPY ${PART}/apk.list /usr/share/spin-machine/apk.list
RUN apk add --no-cache $(grep -v '^#' /usr/share/spin-machine/apk.list)
