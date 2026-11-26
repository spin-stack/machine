#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# What an operator makes for a shirudo machine (shirudo_test.go), as shirudo's boot integration
# guide makes it, in shirudo/Dockerfile's operator image: this run's key, the signed config image,
# the dm-verity runtime image around the release's own guest/shirudo, and one signed upatch,
# shirudo's CVE-2026-31431 mitigation, which refuses every AF_ALG socket. $1 is the work
# directory, $2 the release tree.
set -eu
work=$1 release=$2
keys=$work/keys
mkdir -p "$keys" "$work/images" "$work/config-root/etc/shirudo"
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

src=/src/shirudo/upatches/kernel-cve/sources/cve-2026-31431-crypto-algif-aead-copy-fail/mitigation/cve-2026-31431-af-alg-block.bpf.c
shirudo build "$src" -o "$work/af-alg-block.upatch" --name af-alg-block --keys-dir "$keys" \
	--vmlinux /src/shirudo/vendor/linux/vmlinux.h --extra-cflags -I/src/shirudo/vendor/libbpf
for unit in shirudo.service shirudo-initrd.service; do
	sed 's|@bindir@|/usr/bin|g' "/src/shirudo/systemd/$unit.in" >"$work/$unit"
done
