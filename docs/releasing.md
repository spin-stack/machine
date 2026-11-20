# Checks and releases

## CI

The workflows are split by cost. `ci.yml` runs on every push and builds none of
the three artefacts — it is `task lint` and `task test`, which is fast and catches most
mistakes. It also *pulls* one: `spin-machine-dev fetch qemu` unpacks the published QEMU of the pinned
version in seconds, and `spin-machine-dev verify args` hands it the command line `machine.Spec.Args`
builds. The device set is decided in `qemu/devices.mak` and the device and property names
are written out by hand in `machine/machine.go`; nothing else connects the two, and a
device dropped from the allowlist reads exactly like a typo added here — a VM that does not
start, found by whoever boots one.

Each artefact is a job of `parts.yml` that runs when a change reaches it, because QEMU and
the kernel are tens of minutes each and the base image is ~1.5 GB of apt; its `parts` job
says whether every one that ran passed. Each ends in the verification that belongs to it, so a build that lost a
device, a kernel that lost its PVH notes, or an image that grew an identity fails in the
workflow that produced it.

CI calls the same targets a developer calls. A second definition of what the gate is, kept
in a workflow file, goes out of step with the first exactly when a check is added.

`spin-machine-dev releases measure` is the end-to-end check, and `report.yml` runs it on every
release: the release booted from its published tarball to the first SSH login, its numbers kept in
the repository (`docs/releases.md`).

## Releases

`release.yml` builds all three and packs one tarball. Versions are **CalVer**,
`v20260909.01`: a release of this repository is the machine as it stood on a date. There is
no API here to promise compatibility about, and the one thing a version could promise —
that checkpoints still resume — is decided by the fingerprint, the machine's contract, not by a
number anybody chose: a release that keeps it resumes every checkpoint the one before it saved
(`task boot:compat`), whatever QEMU, kernel or firmware it ships. A release is the workflow dispatched on main, and nothing else - a pushed
tag starts nothing: given a version it makes that one, with no input it generates the next
sequence for today; either way it tags the commit once its files are signed, and writes the
notes: what changed since the release before (`internal/releasenotes`: the commits that reached
the machine by the part they changed, the pins that moved, the kernel options turned on, off
or changed, and the patches added, changed or removed), whether checkpoints carry over
(`internal/release`'s verdict), and the checksums.

Each file the release offers - the tarball, its `.sha256` and `machine.env` - carries two
Sigstore bundles beside it: `<file>.sigstore.json`, the signature of the workflow that built it,
and `<file>.provenance.sigstore.json`, its SLSA provenance (go-tools' `provenance`: this workflow at
its commit, and every pin in `versions.yaml`). Whoever takes one checks both against
`release.yml` at the ref it ran on:

```sh
id=https://github.com/spin-stack/spin-machine/.github/workflows/release.yml@refs/heads/main
cosign verify-blob --bundle f.sigstore.json --certificate-identity "$id" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com f
cosign verify-blob-attestation --type slsaprovenance1 --bundle f.provenance.sigstore.json \
  --digest "$(sha256sum f | cut -d' ' -f1)" --digestAlg sha256 \
  --certificate-identity "$id" --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

The attestation is checked by the file's digest: given the file, `verify-blob-attestation` reads it
whole and refuses one over 128 MiB (`COSIGN_MAX_ATTACHMENT_SIZE`), which the tarball is.

Every release ran on `refs/heads/main`, and that is the ref its certificate names; which release
a file is, its provenance says (`buildDefinition.externalParameters.version`), and whoever takes it
holds it to the version they asked for.

What the release is made of is its CycloneDX SBOM, written by `spin-machine-dev release` from what the builds
recorded - the apk databases QEMU and e2fsprogs were statically linked in (each build's
`/lib/apk/db/installed`, extracted to `_output/sbom/`), the image's `packages.txt`, the pins in
`versions.yaml`, and each shipped file by its hash - and checked with go-tools' `sbom check`: a
component with no version fails the release. It is inside the tarball at
`usr/share/spin-stack/sbom/machine.cdx.json`, beside it as `<tarball>.cdx.json`, and attested
over the tarball as `<tarball>.cdx.sigstore.json` (`cosign verify-blob-attestation --type
cyclonedx`, with the identity above). QEMU, the kernel and qboot carry their patches as pedigree:
each patch file by name and hash, so the SBOM says the binary is upstream's source plus these.

The builders' packages are a commit's, not the mirror's on the day: QEMU and e2fsprogs are compiled
on builder images - Alpine with `qemu/apk.list` or `e2fsprogs/apk.list` installed
(`builder/Dockerfile`) - pinned by digest in `versions.yaml` (`qemu-builder`, `e2fsprogs-builder`).
An image keeps its packages after Alpine's mirror drops them, so a release builds from the same
libraries until a person takes new ones: `spin-machine-dev refresh builders` makes them again,
publishes them and pins them, weekly from refresh.yml, with the packages that moved in the pull
request. A part's build refuses a tree whose apk.list its pinned builder was not made with.
`workflows/workflows_test.go` holds every workflow to attesting before it publishes.

## The record

A release also says what it costs. After it publishes, the `report` job measures it on the bench
fleet (spin-stack/ci: a c8id.4xlarge, nested, the host the goal is measured on), interleaved with
the release before it, in the shape spin boots a workspace in: the two goal numbers - a cold boot
and a restore, each to the first SSH login - both again into a QEMU started ahead, and save, state,
the kernel's time and QEMU's resident memory beside them as diagnostics, twenty boots of each
(`spin-machine-dev releases measure`, docs/cli.md). What it keeps is the release's record,
`docs/releases/RELEASE.json`: each number's p50 and p95, the host, the shape, the date, the run
whose artifact holds every sample (90 days), and the release before it with its numbers from the
same run and each Δ with its 95% interval. `docs/releases.md` is written from every
record - a chart of the goal's numbers release by release (`docs/releases.svg`), the tables, and
where each was measured - and the job proposes them as a pull request, as the release app, so the
checks on it run. A test fails when the page or its chart is not what the records say, so neither
is edited by hand. Once that pull request merges, `pages.yml` publishes the same page, with each
record, at https://spin-stack.github.io/spin-machine/ (`spin-machine-dev releases site`): the
link to share.

With the release before it, and not alone, because a Δ is only read between two releases booted
in the same run. The bench moves between runs by as much as a release does: v20261120.01's restore
to first login was 158.9 ms p50 in one run and 167.0 in another (labs 44641240861, 44727451080),
and the page's Δ from records measured alone said v20261120.02's restore was 1.8 ms faster where,
interleaved, it was +0.6 [-2.1, +5.4], within noise. So the page's Δ comes only from a record's
pair, and is blank for the records measured alone until they are measured again; the second
release's boots are what each run pays for it. A decision between two releases further apart is
the two measured together: `spin-machine-dev lab ladder --newest B --releases N` boots the N
releases up to B interleaved on one host. Twenty boots, because at three a comparison of
v20261001.01 with v20260930.02 flagged every row 5-11% slower, and twenty alternated on one host
put them within 5 ms (2026-10-01).

Each release boots with the spin-machine of the commit it was made from (`--spin-machine release`):
a release ships no spin-machine, and the machine it defined - devices, places, checkpoints - is
that commit's `machine` package, which spin builds. A release whose spin-machine cannot run a
probe (one from before `boot --restore`, v20261119.02) has a dash for that number with the reason.

`report.yml` runs by hand too, for releases from before records: `spin-machine-dev releases
missing` lists them, and `gh workflow run report.yml -f versions="<release> <release>..." -f
record=true` measures each with the release before it, one after another on one host, and
proposes their records together. The runner needs `/dev/kvm` and `/dev/vhost-vsock` usable by its user, `gh`, and `sudo
-n` for `qemu-nbd`.

## What a release owes

Apache-2.0 covers the recipes, not what they build. A release tarball is almost entirely
other people's software: QEMU and Linux are GPL-2.0, and the base image is an Ubuntu
userland under a dozen licences. Shipping their binaries carries obligations a LICENSE file
in a source tree does not discharge — GPL-2.0 section 3, and section 6 of the LGPL for the
libraries inside the statically linked QEMU.

So a release answers them:

- `SOURCES` names every upstream source by version, URL and SHA-256, says how it was built,
  and carries the written offer.
- `packages.txt` lists every package in the base image with its exact version, which is
  what `apt-get source <package>=<version>` needs.
- The upstream tarballs are pinned by SHA-256 in `versions.yaml` and **verified on every
  build**, not only after a download — the source lives in a cache mount that outlives the
  build that filled it. That is also what makes `SOURCES` true rather than aspirational:
  what it names is what was compiled.
