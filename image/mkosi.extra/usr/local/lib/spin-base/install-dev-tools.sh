#!/usr/bin/env bash
set -euo pipefail

echo "Installing development tools..."

# docs/image.md, "The toolchain"
: "${TASK_VERSION:?}" "${TASK_SHA256:?}"
: "${GIT_LFS_VERSION:?}" "${GIT_LFS_SHA256:?}"
: "${BUILDKIT_VERSION:?}" "${BUILDKIT_SHA256:?}"
: "${DOCKER_CE_VERSION:?}" "${DOCKER_CE_SHA256:?}"
: "${DOCKER_CE_CLI_VERSION:?}" "${DOCKER_CE_CLI_SHA256:?}"
: "${DOCKER_CE_ROOTLESS_EXTRAS_VERSION:?}" "${DOCKER_CE_ROOTLESS_EXTRAS_SHA256:?}"
: "${CONTAINERD_IO_VERSION:?}" "${CONTAINERD_IO_SHA256:?}"
: "${DOCKER_BUILDX_PLUGIN_VERSION:?}" "${DOCKER_BUILDX_PLUGIN_SHA256:?}"
: "${DOCKER_COMPOSE_PLUGIN_VERSION:?}" "${DOCKER_COMPOSE_PLUGIN_SHA256:?}"

download() {
    local url="$1" output="$2" sha256="$3"
    echo "Downloading ${url}..."
    curl -fsSL --retry 5 --retry-all-errors "${url}" -o "${output}"
    echo "${sha256}  ${output}" | sha256sum -c -
}

echo "Installing Task v${TASK_VERSION}..."
download \
    "https://github.com/go-task/task/releases/download/v${TASK_VERSION}/task_linux_amd64.tar.gz" \
    "/tmp/task.tar.gz" \
    "${TASK_SHA256}"

tar -xzf /tmp/task.tar.gz -C /tmp
install -m 755 /tmp/task /usr/local/bin/task
task --version

echo "Installing Git LFS v${GIT_LFS_VERSION}..."
download \
    "https://github.com/git-lfs/git-lfs/releases/download/v${GIT_LFS_VERSION}/git-lfs-linux-amd64-v${GIT_LFS_VERSION}.tar.gz" \
    "/tmp/git-lfs.tar.gz" \
    "${GIT_LFS_SHA256}"

tar -xzf /tmp/git-lfs.tar.gz -C /tmp
install -m 755 "/tmp/git-lfs-${GIT_LFS_VERSION}/git-lfs" /usr/local/bin/git-lfs
git-lfs version

echo "Installing BuildKit v${BUILDKIT_VERSION}..."
download \
    "https://github.com/moby/buildkit/releases/download/v${BUILDKIT_VERSION}/buildkit-v${BUILDKIT_VERSION}.linux-amd64.tar.gz" \
    "/tmp/buildkit.tar.gz" \
    "${BUILDKIT_SHA256}"

tar -xzf /tmp/buildkit.tar.gz -C /usr/local
rm -rf /usr/local/bin/buildkit-qemu-* 2>/dev/null || true
buildctl --version

# docs/image.md, "Packages"
echo "Installing Docker ${DOCKER_CE_VERSION}..."
pool=https://download.docker.com/linux/ubuntu/dists/resolute/pool/stable/amd64
mkdir -p /tmp/docker
docker_deb() {
    local package="$1" version="$2" sha256="$3"
    download "${pool}/${package}_${version}~ubuntu.26.04~resolute_amd64.deb" \
        "/tmp/docker/${package}.deb" "${sha256}"
}
docker_deb docker-ce "${DOCKER_CE_VERSION}-1" "${DOCKER_CE_SHA256}"
docker_deb docker-ce-cli "${DOCKER_CE_CLI_VERSION}-1" "${DOCKER_CE_CLI_SHA256}"
docker_deb docker-ce-rootless-extras "${DOCKER_CE_ROOTLESS_EXTRAS_VERSION}-1" "${DOCKER_CE_ROOTLESS_EXTRAS_SHA256}"
docker_deb containerd.io "${CONTAINERD_IO_VERSION}" "${CONTAINERD_IO_SHA256}"
docker_deb docker-buildx-plugin "${DOCKER_BUILDX_PLUGIN_VERSION}-1" "${DOCKER_BUILDX_PLUGIN_SHA256}"
docker_deb docker-compose-plugin "${DOCKER_COMPOSE_PLUGIN_VERSION}-1" "${DOCKER_COMPOSE_PLUGIN_SHA256}"

apt-get update
apt-get install -y /tmp/docker/*.deb
docker --version
rm -rf /tmp/docker

echo "Cleaning up temporary files..."
rm -rf /var/tmp/* /usr/local/share/doc /usr/local/share/man

apt-get clean

echo "✅ Development tools installation complete"
