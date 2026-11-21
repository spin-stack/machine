# Checks and releases

## CI

The workflows are split by cost. `ci.yml` runs on every pull request and every push to main,
and builds none of the three artefacts. Its gates are `task lint`, `task test`, `task coverage` and `task mutate`, and lint and
test are fast and catch most mistakes. It also *pulls* one artefact: `spin-machine-dev fetch qemu`
unpacks the published QEMU of the pinned version in seconds, and `spin-machine-dev verify args`
hands it the command line `machine.Spec.Args` builds. `qemu/devices.mak` decides the device set,
and `machine/machine.go` writes the device and property names out by hand. Nothing else connects
the two. A device dropped from the allowlist reads exactly like a typo added in
`machine/machine.go`: a VM that does not start, found by whoever boots one.

Each artefact is a job of `parts.yml` that runs when a change reaches it, because QEMU and the
kernel take tens of minutes each and the base image is ~1.5 GB of apt. The `parts` job says whether
every job that ran passed. Each artefact's job ends in the verification that belongs to it, so a
build that lost a device, a kernel that is not a relocatable bzImage, or an image that grew an identity fails
in the workflow that produced it.

CI calls the same targets a developer calls. A second definition of what the gate is, kept in a
workflow file, goes out of step with the first exactly when a check is added.

`spin-machine-dev releases measure` is the end-to-end check, and `report.yml` runs it on every
release. It boots the release from its published tarball to the first SSH login, and the
repository keeps its numbers (`docs/releases.md`).

## Releases

`release.yml` builds all three artefacts and packs one tarball. Versions are **CalVer**, such as
`v20260909.01`, because a release of this repository is the machine as it stood on a date. There is
no API here to promise compatibility about. The one thing a version could promise is that
checkpoints still resume, and the fingerprint decides that, not a number anybody chose. The
fingerprint is the machine's contract: a release that keeps it resumes every checkpoint the release
before it saved (`task boot:compat`), whatever QEMU, kernel or firmware it ships.

A release is release.yml dispatched on main, and nothing else. A pushed tag starts nothing. Given a
version, the workflow makes that one. With no input, it generates the next sequence for today.
Either way, it tags the commit once its files are signed, and writes the notes:

- What changed since the release before (`internal/release`'s `Changes`): the commits that reached the
  machine, by the part they changed; the pins that moved; the kernel options turned on, off or
  changed; and the patches added, changed or removed.
- Whether checkpoints carry over (`internal/release`'s verdict).
- Whether the QEMU shipped offers a machine type newer than the one checkpoints are saved on.
- The release's `machine.env`, with the checksums of its parts.

Each file the release offers carries two Sigstore bundles beside it. The files are the tarball, its
`.sha256`, `machine.env` and the SBOM beside the tarball. The bundles are `<file>.sigstore.json`, the signature of the workflow
that built the file, and `<file>.provenance.sigstore.json`, its SLSA provenance (go-tools'
`provenance`: this workflow at its commit, and every pin in `versions.yaml`). [Verify a release file](#verify-a-release-file) says how to check them.

The release's CycloneDX SBOM says what the release is made of. `spin-machine-dev release` writes it
from what the builds recorded: the apk databases QEMU and e2fsprogs were statically linked in (each
build's `/lib/apk/db/installed`, extracted to `_output/sbom/`), the image's `packages.txt`, the
`versions.yaml` pins of the sources it is built from, and each shipped file by its hash. go-tools'
`sbom.Check` checks the SBOM, and a component with no version fails the release. The SBOM is inside
the tarball at `usr/share/spin-stack/sbom/machine.cdx.json`, beside it as
`spin-machine-VERSION-linux-x86_64.cdx.json`, and attested over the tarball as
`spin-machine-VERSION-linux-x86_64.tar.gz.cdx.sigstore.json` (`cosign verify-blob-attestation --type
cyclonedx`, with the identity in "Verify a release file"). QEMU, the kernel and qboot carry their
patches as pedigree: each patch by its file name and its text, so the SBOM says the binary is
upstream's source plus these patches.

The builders' packages come from a commit, not from the mirror on the day of the build. QEMU and
e2fsprogs compile on builder images, pinned by digest in `versions.yaml` (`qemu-builder`,
`e2fsprogs-builder`). Each is Alpine with `qemu/apk.list` or `e2fsprogs/apk.list` installed
(`builder/Dockerfile`). An image keeps its packages after Alpine's mirror drops them, so a release
builds from the same libraries until a person takes new ones. `spin-machine-dev refresh builders`
makes the images again, publishes them and pins them. refresh.yml runs it weekly, with the packages
that moved in the pull request. A part's build refuses a tree whose apk.list its pinned builder was
not made with. `workflows/workflows_test.go` holds every workflow to attesting before it publishes.

## The record

A release also says what it costs. After the release publishes, release.yml's `report` job
measures it on the bench fleet (spin-stack/ci: a c8id.4xlarge, nested, the host the goal is
measured on). The job boots it interleaved with the release before it, in the shape spin boots a
workspace in, twenty boots of each (`spin-machine-dev releases measure`, docs/cli.md). It measures
the two goal numbers, a cold boot and a restore, each to the first SSH login, and both again into a
QEMU started ahead. Save, state, the kernel's time and QEMU's resident memory are beside them as
diagnostics.

The job keeps the release's record, `docs/releases/RELEASE.json`: each number's p50 and p95, the
host, the shape, the date, the run whose artifact holds every sample (90 days), and the release
before it, with its numbers from the same run and each Δ with its 95% interval. `releases record`
writes `docs/releases.md` from every record: a chart of the goal's numbers release by release
(`docs/releases.svg`), the tables, and where each was measured. The job proposes the records and
the page as a pull request, as the release app, so the checks on it run. A test fails when the page
or its chart is not what the records say, so nobody edits either by hand. Once that pull request
merges, `pages.yml` publishes the same page, with each record, at
https://spin-stack.github.io/spin-machine/ (`spin-machine-dev releases site`). That URL is the link
to share.

The job measures a release with the release before it, and not alone, because a Δ is read only
between two releases booted in the same run. The bench moves between runs by as much as a release
does. v20261120.01's restore to first login was 158.9 ms p50 in one run and 167.0 in another (labs
44641240861, 44727451080). The page's Δ from records measured alone said v20261120.02's restore was
1.8 ms faster, where, interleaved, it was +1.1 [-2.0, +6.1], within noise (run 44820465606). So the page's Δ comes
only from a record's pair, and is blank for the records measured alone until they are measured
again. Each run pays for that with the second release's boots. To decide between two releases
further apart, measure the two together: `spin-machine-dev lab ladder --newest B --releases N` boots
the N releases up to B interleaved on one host. The job takes twenty boots because at three, a
comparison of v20261001.01 with v20260930.02 flagged every row 5-11% slower, and twenty alternated
on one host put them within 5 ms (2026-10-01).

Each release boots with the spin-machine of the commit it was made from (`--spin-machine release`).
A release ships no spin-machine, and the machine it defined (devices, places, checkpoints) is that
commit's `machine` package, which spin builds. A release whose spin-machine cannot run a probe has a
dash for that number, with the reason. A release from before v20261119.02, the release that brought
`boot --restore`, is one.

The job runs at two commits. It measures with the release's tag checked out, and `releases measure`
keeps only the ladders (`_output/lab/measured/RELEASE/ladder.json`). The record, the page and the
proposal are main's: a second checkout of main builds its own spin-machine-dev, `releases record`
writes each record and the page from the ladders, and `releases propose` opens the pull request
from there. The page is whatever main's code writes from the records, and main's test fails on any
other page. A page written by the tag's code therefore fails as soon as main's chart or tables
change after the tag. v20261120.04's proposal failed that way (#350), after #342 changed the chart.

[Measure releases from before records](#measure-releases-from-before-records) says how to
backfill them.

## What a release owes

Apache-2.0 covers the recipes, not what they build. A release tarball is almost entirely other
people's software: QEMU and Linux are GPL-2.0, and the base image is an Ubuntu userland under a
dozen licences. Shipping their binaries carries obligations that a LICENSE file in a source tree
does not discharge: GPL-2.0 section 3, and section 6 of the LGPL for the libraries inside the
statically linked QEMU.

So a release answers them:

- `SOURCES` names every upstream source by version, URL and SHA-256, says how it was built,
  and carries the written offer.
- `packages.txt` lists every package in the base image with its exact version, which is
  what `apt-get source <package>=<version>` needs.
- `versions.yaml` pins the upstream tarballs by SHA-256, and every build **verifies them**, not
  only the build after a download, because the source lives in a cache mount that outlives the
  build that filled it. The check is also what makes `SOURCES` true: what it names is what was
  compiled.

## Verify a release file

Check both bundles against `release.yml` at the ref it ran on:

```sh
id=https://github.com/spin-stack/spin-machine/.github/workflows/release.yml@refs/heads/main
cosign verify-blob --bundle f.sigstore.json --certificate-identity "$id" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com f
cosign verify-blob-attestation --type slsaprovenance1 --bundle f.provenance.sigstore.json \
  --digest "$(sha256sum f | cut -d' ' -f1)" --digestAlg sha256 \
  --certificate-identity "$id" --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

The second command checks the attestation by the file's digest. Given the file itself,
`verify-blob-attestation` reads it whole and refuses one over 128 MiB
(`COSIGN_MAX_ATTACHMENT_SIZE`), and the tarball is larger than that.

Every release ran on `refs/heads/main`, and that is the ref its certificate names. The provenance
says which release a file is (`buildDefinition.externalParameters.version`). Whoever takes the file
holds it to the version they asked for.

## Measure releases from before records

The runner needs `/dev/kvm` and `/dev/vhost-vsock` usable by its user, `gh`, and `sudo -n` for
`qemu-nbd`.

1. List the releases that have no record:

   ```sh
   spin-machine-dev releases missing
   ```

2. Run `report.yml` with them:

   ```sh
   gh workflow run report.yml -f versions="<release> <release>..." -f record=true
   ```

   It measures each release with the release before it, one after another on one host, and
   proposes their records together.
