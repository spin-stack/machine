# spin-machine-dev, the command

What a person types to work on this machine, and how GitHub Actions composes the same verbs.
Each verb's `--help` says what it needs, does, leaves and exits with; this page is how they fit
together. `spin-machine-dev --help` lists them. `go tool task bin` builds it into `_output/bin`.

It is not `spin-machine`. `cmd/spin-machine` is the machine's own tool - it boots one, prints its
fingerprint, attaches a disk, saves and restores - and a release ships it, statically linked,
for whoever runs the machine by hand. `cmd/spin-machine-dev` builds, measures and releases the
machine, and is never shipped.

## Principles

The same as spin-os's (spin-stack/ami, `docs/cli.md`), because they are the same problem:

1. **A verb is a goal, not a step.** `spin-machine-dev build image` builds the image and what it
   is made with that is missing or stale - e2fsprogs, the systemd it installs - and `release`
   builds every part before it packs them. Nobody builds e2fsprogs first.
2. **State is files, never variables.** Every verb reads and writes under `_output/` (`--out`).
   Each part a build makes leaves `_output/manifest/<part>.json`, naming a digest of its inputs -
   the build arguments versions.yaml hands its Dockerfile and every file under its directory - so
   a part whose inputs have not moved, and whose output still verifies, is not built again. A
   verb can run alone, again, or on another machine with the same `_output/`.
3. **Two outputs.** Progress and the builds' logs on stderr, for a person. With `--json`, one JSON
   document on stdout, for a program.
4. **Exit status means something, the same everywhere.** 0 done; 1 done and something to act on
   (a verify that failed, a pin behind, a file not attested); 2 could not do it (no network, a
   missing input, a build that failed). A workflow branches on the status, never on text.
5. **Flags for choices, the environment for credentials and the runner.** `--jobs`,
   `--cache-registry`, `--cache-export`, `--version` are flags. The environment carries only what
   is secret or the runner's: `GH_TOKEN`/`GITHUB_TOKEN`, `GITHUB_REPOSITORY`, `SOURCE_DATE_EPOCH`.
   In a workflow, a verb a later step needs also writes its result to `$GITHUB_OUTPUT` (`build`'s
   `qemu_version`, `release version`'s `version`, `release notes`' `notes`, `changed`'s `build`)
   and what it made to the run's summary - a copy, never the only place it is.

## Nouns

| noun | is | where |
|---|---|---|
| part | what a build makes: `qemu` (with qboot and the firmware), `kernel`, `e2fsprogs`, `systemd`, `image`; and `spin-machine` | `_output/{bin,qemu,kernel,e2fsprogs,systemd,image}`, `_output/manifest/<part>.json` |
| release | every part, packed as one tarball with one version, its manifest, SOURCES and SBOM | `_output/release/` |
| pin | an entry of `versions.yaml`: what a part is built from, by version and hash | `versions.yaml` |

## Working on the machine

```
spin-machine-dev build [part...]      what is missing or stale, every part when none is named; each verified
spin-machine-dev fetch qemu           main's published QEMU of the pinned version, in seconds
spin-machine-dev verify [part|args]   assert what _output holds, building nothing; args asks QEMU for the machine's command lines
spin-machine-dev shell                boot it and get a console in the base image, which is checked unchanged after
spin-machine-dev vsock                make /dev/vhost-vsock usable, for the probes and verify args
spin-machine-dev menuconfig           edit the kernel's configuration in place
spin-machine-dev clean [--cache]      remove _output, and the BuildKit and mkosi caches
```

A part's build empties what it writes first - `--output type=local` writes files and removes
none, so a file a build stopped producing would stay, indistinguishable from one it produces -
and ends in its verification: the binaries run here and only the CI QEMU can emulate, the kernel
is a relocatable bzImage and a vmlinux with its PVH notes, the e2fsprogs tools answer the pinned
version. `verify --help` says each.

## Keeping the pins current

```
spin-machine-dev outdated             which pins are behind their upstream (exits 1 when any is)
spin-machine-dev bump <entry> [ver]   move one pin; a QEMU bump prints what newer machine types change
spin-machine-dev lock                 the apk locks at what the pinned Alpine's mirror has today
spin-machine-dev osv [--issues]       OSV against the newest release's SBOM; an issue per package with findings
spin-machine-dev cves [--local]       the kernel CNA's CVEs against the newest release's kernel, or _output's
```

`outdated` and `bump` are go-tools' `versions` (go.mod's tool directive), which every spin-stack
repository shares; this command adds what is this repository's: the QEMU machine types, the apk
locks, OSV and the kernel's CVEs.

## Releasing

```
spin-machine-dev release [--version v]   build every part and spin-machine, then pack them; refuses a tree with anything missing
```

The release is one tarball: QEMU and its firmware, the kernel, the base image, the e2fsprogs
tools, LICENSE and NOTICE, every patch each part was built with (a directory ships whole, never as
a list), `machine.env` - the manifest, whose `fingerprint_sha256` alone decides whether
checkpoints resume - `SOURCES` and a CycloneDX SBOM, inside it and beside it.

What only release.yml runs is hidden from `--help`, under `release`: `version` (the next
vYYYYMMDD.NN), `notes` (what changed since the release before, and the fingerprint verdict, which
never fails: changing the machine is allowed, being quiet about it is not), `provenance` (SLSA),
`sign` (cosign over every file, each bundle checked, then attestgate), `tag` (through the API, as
GITHUB_TOKEN cannot push a ref over a workflow change), and `pack` (pack what `_output` holds,
building nothing). So are `publish qemu` (main's qemu.yml), `changed` (whether a change can reach
a part) and `refresh` (refresh.yml's proposals).

## How GitHub Actions composes it

A workflow is the verbs in order, a step each. A job that runs spin-machine-dev runs
`go tool task bin` first, which builds `_output/bin/spin-machine-dev` and puts it on the job's
PATH: `go run` would exit 1 whatever the verb's status. taskgate (`-command spin-machine-dev`, in
`task lint`) holds each step to one invocation or one task.

```
ci.yml       task lint ; task test ; task coverage ; task mutate ; task bin ;
             spin-machine-dev build spin-machine ; spin-machine-dev fetch qemu ;
             spin-machine-dev vsock ; spin-machine-dev verify args
qemu.yml     changed: spin-machine-dev changed --base $BASE --pins alpine,qemu,... qemu ...
             build:   spin-machine-dev build qemu --cache-registry ; vsock ; verify args ;
                      publish qemu (main)
kernel.yml   changed ; spin-machine-dev build kernel --cache-registry
image.yml    changed ; spin-machine-dev build e2fsprogs ; spin-machine-dev build image
compat.yml   task boot:buildkit ; spin-machine-dev vsock ;
             spin-machine-dev build qemu kernel spin-machine ; task boot:here, boot:roms, ...
release.yml  spin-machine-dev release version ; release ; release notes ; release provenance ;
             release sign ; release tag ; then the publish action
refresh.yml  task git:app ; spin-machine-dev refresh kernel|systemd|apk
osv.yml      spin-machine-dev osv --issues ; spin-machine-dev cves
```

A build in CI reads BuildKit's cache from GitHub's registry (`--cache-registry`,
`internal/build.CacheRegistry`) and writes it back only on main (`--cache-export`): a pull
request's, a release's and compat's builds read main's, and what they compiled nobody reads after.
The registry and not the Actions cache, which the fleet's runners cannot read and which evicted a
lane's layers between a merge and the release that needed them.

## The Taskfile

What a person types is spin-machine-dev. The Taskfile is go-tools' gates, the same in every
spin-stack repository - `test`, `lint`, `lint:workflows`, `mutate`, `coverage` - and CI's
plumbing: `bin`, and `git:app` (git as the release app). The probes' targets (`boot:*`) and the
lab's and the report's run what the verbs built.

## Measuring

The probes, the lab and the report: `boot/`, `lab.yml` and `report.yml`.
