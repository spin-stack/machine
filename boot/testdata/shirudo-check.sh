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
