#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
set -eu
trap 'echo SHIRUDO_CHECK_FAILED' EXIT

echo "kernel: $(uname -r)"
echo "lockdown: $(cat /sys/kernel/security/lockdown)"
echo "cmdline: $(cat /proc/cmdline)"
ls -l /dev/tpm0 /dev/tpmrm0

# Both halves of init, each to its end: the initrd's, which enrols the certificate and seals the
# keyrings, and the root file system's, which adopts it. init --check-only passes its pre-flight
# checks when init itself fails, so what decides is init's own word and status, never those.
journalctl -b -u shirudo-initrd.service -u shirudo.service --no-pager -o short-monotonic
for unit in shirudo-initrd.service shirudo.service; do
	journalctl -b -u "$unit" --no-pager -o cat | grep -q '^init: complete' ||
		{ echo "$unit did not complete init"; exit 1; }
done
test "$(systemctl show -P Result shirudo.service)" = success

# PCR 11 holds systemd's boot phases, enter-initrd and leave-initrd at least, measured before
# shirudo's init in the initrd reads it: the machine asks a guest with a TPM for a measured OS on
# its command line, as it boots no UKI (#773). An image whose systemd measures only under a UKI
# (before 262) leaves PCR 11 empty and shirudo warns, which compat's shirudo job still boots: it
# runs on the newest release's image.
if grep -qx 'ConditionSecurity=measured-os' /usr/lib/systemd/system/systemd-pcrphase-initrd.service; then
	pcr11=$(cat /sys/class/tpm/tpm0/pcr-sha256/11)
	echo "PCR 11: $pcr11"
	case $pcr11 in *[!0]*) ;; *) echo "PCR 11 holds no measurement"; exit 1 ;; esac
	cat /run/log/systemd/tpm2-measure.log | tr '\036' '\n'
	for phase in enter-initrd leave-initrd; do
		grep -q "\"string\":\"$phase\"" /run/log/systemd/tpm2-measure.log ||
			{ echo "no $phase in PCR 11's log"; exit 1; }
	done
	if journalctl -b --no-pager -o cat | grep 'PCR 11 holds no phase measurement'; then
		echo "shirudo's init found PCR 11 empty"
		exit 1
	fi
fi

# The shirudo that runs is the one the release ships, from the dm-verity runtime image the
# initrd mounted: shirudo answers for its state only to that binary. SHIRUDO_SHA256 is the
# shipped file's (shirudo_test.go).
shirudo=/run/shirudo/runtime/usr/bin/shirudo
echo "$SHIRUDO_SHA256  $shirudo" | sha256sum -c -

status=$($shirudo status --show-full) || { echo "$status"; exit 1; }
echo "$status"
echo "$status" | grep -Eq '^Status:[[:space:]]+ok$'
echo "$status" | grep -Eq '^[[:space:]]+KeyringSealed[[:space:]]+OK[[:space:]]'
echo "$status" | grep -Eq '^[[:space:]]+LsmAttached[[:space:]]+OK[[:space:]]'

# socket(2) in the kernel's own words: AF_ALG (38) is the path the upatch blocks, AF_UNIX one it
# leaves alone. This kernel has no AF_ALG (CONFIG_CRYPTO_USER_API is off), so it answers
# EAFNOSUPPORT; the LSM hook runs before the family is looked up, so the upatch answers EPERM.
sock() {
	perl -MSocket -e 'socket(my $s, $ARGV[0], SOCK_SEQPACKET, 0) or do { print "$!\n"; exit 0 }; print "created\n"' "$1"
}
before=$(sock 38)
echo "AF_ALG before the upatch: $before"
test "$before" = "Address family not supported by protocol"

$shirudo upatch load /af-alg-block.upatch
$shirudo upatch list
blocked=$(sock 38)
allowed=$(sock 1)
echo "AF_ALG with the upatch: $blocked"
echo "AF_UNIX with the upatch: $allowed"
test "$blocked" = "Operation not permitted"
test "$allowed" = created

trap - EXIT
echo SHIRUDO_CHECK_OK
