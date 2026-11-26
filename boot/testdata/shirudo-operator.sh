#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# What an operator makes for a shirudo machine (shirudo_test.go), as shirudo's boot integration
# guide makes it, with nothing but the release: run as root in a guest of it, with the release's
# guest/shirudo and its operator half on the disk whose serial is op. This run's key, the signed
# config image, the dm-verity runtime image around the release's own guest/shirudo, two upatches
# signed with the key (shirudo's CVE-2026-31431 mitigation, which refuses every AF_ALG socket,
# from `shirudo build`, and its kernel-obs package from `shirudo bpm build`), and the systemd
# initrd, checked. What it makes is left in /work on that disk.
set -eu
trap 'echo SHIRUDO_OPERATOR_FAILED' EXIT
# Under /run, not a new directory in /: a guest's mkdir in a directory rawRoot's debugfs edited
# found its entry corrupt (#808).
op=/run/op
mkdir -p "$op"
mount /dev/disk/by-id/virtio-op "$op"
release=$op/spin-stack
work=$op/work
# dracut's staging tree and every other scratch file go on the disk: the base image's root has
# about 100 MiB free, which the packages take half of.
export TMPDIR=$op/tmp
mkdir -p "$TMPDIR" "$work/keys" "$work/images" "$work/packages" "$work/config-root/etc/shirudo"
df -h / "$op"

"$release/shirudo-operator/install"
command -v shirudo shirudo-build shirudo-gen-image shirudo-gen-runtime shirudo-gen-initrd llvm-strip

keys=$work/keys
chmod 0700 "$keys"
shirudo genkey --keys-dir="$keys"
cert=$keys/cert.der

echo 'eventlog.max_bytes = 0' >"$work/config-root/etc/shirudo/shirudo.conf"
cat >"$work/runtime.conf" <<'EOF'
config.image = /var/lib/shirudo/images/initial
config.verification_key = /run/shirudo/runtime/etc/shirudo/config-cert.der
config.pcr = 14
tpm.tcti = device:/dev/tpmrm0
EOF
cd "$work/images"
shirudo-gen-image --source "$work/config-root" --output initial --keys-dir "$keys" --kind config
shirudo image verify initial --cert "$cert"
shirudo-gen-runtime --binary "$release/guest/shirudo" --bootstrap-conf "$work/runtime.conf" \
	--cert "$cert" --config-cert "$cert" --output runtime --keys-dir "$keys" \
	--unit-output "$work/runtime-service.conf"
printf 'runtime.image = /var/lib/shirudo/images/runtime\nruntime.root_hash = %s\n' \
	"$(cat runtime.roothash)" >"$work/uki-bootstrap.conf"

share=/usr/local/share/shirudo
shirudo build "$share/upatches/kernel-cve/sources/cve-2026-31431-crypto-algif-aead-copy-fail/mitigation/cve-2026-31431-af-alg-block.bpf.c" \
	-o "$work/af-alg-block.upatch" --name af-alg-block --keys-dir "$keys"
cp -R "$share/upatches/kernel-obs" "$work/kernel-obs"
cd "$work/packages"
shirudo bpm build "$work/kernel-obs" --version 1.0.0 --keys-dir "$keys"
ls -l "$work/packages"

for unit in shirudo.service shirudo-initrd.service; do
	sed 's|@bindir@|/usr/bin|g' "$share/systemd/$unit.in" >"$work/$unit"
done
shirudo-gen-initrd --kver "$(uname -r)" --no-kernel \
	--binary "$release/guest/shirudo" --binary-path /usr/bin/shirudo \
	--cert "$cert" --bootstrap-conf "$work/uki-bootstrap.conf" \
	--unit "$work/shirudo-initrd.service" --output "$work/initrd.img" \
	-- --no-hostonly --no-hostonly-cmdline --zstd
shirudo-gen-initrd --check "$work/initrd.img"

cd /
rm -rf "$TMPDIR"
umount "$op"
trap - EXIT
echo SHIRUDO_OPERATOR_OK
