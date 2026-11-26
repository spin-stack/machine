# swtpm's patches

The release's swtpm is built by qemu/Dockerfile's swtpm stage from the tarball versions.yaml
pins, with the patches here applied in name order. Each is `git format-patch` output against that
tarball. They are BSD-3-Clause, like upstream.

## No OpenSSL configuration file, and no algorithm test at each INIT

`openssl-unconfigured.patch`. At every CMD_INIT, swtpm asks whether the OpenSSL it runs with has
disabled an algorithm that the TPM's profile enables. It answers by signing with an RSA key of
every size the profile has (`tpmlib_maybe_configure_openssl`). The test is there for a libcrypto
shared with the host, whose configuration file or FIPS mode can disable algorithms. QEMU waits on
the INIT before the guest's first instruction, and on a restore it waits on the INIT after the
TPM's state blobs. callgrind put 89% of an INIT's instructions in the test, 86% in its RSA signs
(`check_rsa_sign`; one INIT, swtpm 0.10.2 as the stage builds it, 2026-11-26). Upstream's
main still runs it at every INIT (checked 2026-11-26).

This swtpm links its own libcrypto statically. The patch has `main` initialise it first with
`OPENSSL_INIT_NO_LOAD_CONFIG`, so it reads no configuration file. Nothing outside the binary can
then disable an algorithm, and the test would give the same answer every time. `tpmlib_start`
skips it then. It still runs if that initialisation fails. The stage holds the patch by what it
is for: given an OpenSSL configuration file whose provider cannot load, an unpatched swtpm finds
every algorithm disabled and exits at its TPM's start, and this one runs. The algorithms
themselves are tested by `task boot:tpm`, whose RSA-3072 primary is shirudo's, and by
`--print-capabilities` in the stage.

What it is worth on the goal's two numbers is in docs/boot-time.md, "A machine with a TPM".
