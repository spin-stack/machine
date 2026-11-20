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
building nothing). So are `publish qemu` (main's parts.yml), `changed` (whether a change can reach
a part), `needs` (whether every job a summary job needs passed) and `refresh` (refresh.yml's proposals).

## How GitHub Actions composes it

A workflow is the verbs in order, a step each. A job that runs spin-machine-dev runs
`go tool task bin` first, which builds `_output/bin/spin-machine-dev` and puts it on the job's
PATH: `go run` would exit 1 whatever the verb's status. taskgate (`-command spin-machine-dev`, in
`task lint`) holds each step to one invocation or one task.

```
ci.yml       task lint ; task test ; task coverage ; task mutate ; task bin ;
             spin-machine-dev build spin-machine ; spin-machine-dev fetch qemu ;
             spin-machine-dev vsock ; spin-machine-dev verify args
parts.yml    changed: spin-machine-dev changed --base $BASE --pins ... <paths>, once a part
             qemu:    spin-machine-dev build qemu --cache-registry ; vsock ; verify args ;
                      publish qemu (main)
             kernel:  spin-machine-dev build kernel --cache-registry
             image:   spin-machine-dev build e2fsprogs ; spin-machine-dev build image
             parts:   spin-machine-dev needs
compat.yml   task boot:buildkit ; spin-machine-dev vsock ;
             spin-machine-dev build qemu kernel spin-machine ; task boot:here, boot:roms, ...
release.yml  spin-machine-dev release version ; release ; release notes ; release provenance ;
             release sign ; release tag ; then the publish action
refresh.yml  task git:app ; spin-machine-dev refresh kernel|systemd|builders
report.yml   task bin ; task vsock ; spin-machine-dev releases measure TAG... ;
             task git:app ; spin-machine-dev releases propose
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

`spin-machine-dev lab` is how this machine is measured: here, where a laptop finds leads, and on the
bench fleet, where a number decides something (AGENTS.md, "The goal"). Its code is
`internal/lab`; each verb's `--help` says what it takes.

| noun | is | where |
|---|---|---|
| tree | a whole machine - QEMU and its firmware, a kernel, the image, a spin-machine - laid out as a release, with a `manifest.json` naming where each part came from and the edits made to every boot's overlay | `_output/trees/NAME/` |
| probe | a measurement of a tree: it boots it, records named numbers for each repetition, and says what it lost | `internal/lab`, one file each |
| experiment | a probe over trees, repetitions of each, interleaved, written as `result.json`: every sample, the options, each tree's manifest, the host | `_output/lab/results/`, or a lab run's artifact |

A tree starts from a release (`--from release`, `--from release:v20261119.01`) or a directory laid
out as one (`--from _output`), and replaces what is asked for: `--kernel run:N` (a parts.yml run's
bzImage, or a Kernel workflow run's from before parts.yml; a run from before 2026-11-19 has none and is refused by name), `--qemu run:N`,
`--spin-machine here` (built from this checkout; a release ships none, so a tree from one always
gets this checkout's, as the `boot:` targets' `:tools` dependency gave the probes before),
`--spin-machine release` (built, through the module proxy, from the commit the release's
`machine.env` names: the machine that release defined, which the ladder boots each release with),
`--append ARGS`, `--edit lane` (the first login's
lane as this checkout builds it, put into a published release's overlays), `--edit nolinger` (spin
not lingering, as this checkout builds the image), `--systemd PATCH`
(image/systemd/experiments). Nothing is written through the tree it starts from: parts are links,
and a replaced part is a copy. A tree whose manifest is missing, names no source for a part, or
does not match the file is refused before anything boots - a probe is handed trees, never a path
in an environment variable it may not read.

```
spin-machine-dev lab tree NAME [tree flags]              make _output/trees/NAME
spin-machine-dev lab run PROBE [TREE...] [--tree NAME=SPEC]... [--reps N] [--opt K=V]...
spin-machine-dev lab show RUN_ID|DIR                     a lab run's result.json, downloaded, as its table
spin-machine-dev lab dispatch PROBE --tree NAME=SPEC... [--reps N] [--opt K=V]... [--host bench|bench-metal]
spin-machine-dev lab check TARGET [--tree NAME=SPEC]     boot:TARGET on one tree
spin-machine-dev lab ladder [--releases N] [--newest vX] the newest releases measured together: ladder.json
spin-machine-dev lab compat [--here DIR] [--releases N]  a checkpoint of every release since the contract moved, resumed
```

Two releases are compared by `lab ladder`, not by comparing two runs taken apart: the newest
releases up to `--newest` (five unless told), each a tree with `--spin-machine release`, every goal
probe over the releases whose spin-machine can run it (a probe says which commands and flags it
needs; v20261119.01's has no `boot --restore`), interleaved on one host. Its table is the goal's two
numbers release by release, each with its Δ against the release above and the newest against 100
ms, the prestarted ones, and save, state, the kernel and QEMU's RSS as diagnostics, and the shape
every release booted.

### What a Δ says

Every comparison the lab prints - `lab run`'s and `lab show`'s table of trees against the first,
a ladder's, `releases compare`, the page's Δ column - is the Δ p50 of two sets of boots taken in
one run, interleaved, and its 95% interval: the bootstrap's, each side drawn again with replacement
2000 times from a fixed seed, so the same samples always print the same interval
(`lab.DeltaP50`). The cold first login of v20261119.01 against v20261114.02 reads
`-54.2 [-62.9, -49.7]`, and v20261119.02's against it `+3.0 [-0.8, +6.6] within noise` (lab
44641240861).

One rule decides: **a change is a gain only when the interval of its Δ excludes 0** - wholly below
it, for a time. An interval that includes 0 is within noise, whatever the Δ's size; there is no
threshold beside it. A Δ between two runs has no interval and is not read: the bench moves between
runs by as much as a release does. A side with one boot has no interval either, and its Δ is
printed bare, saying so.

### The shape

Every probe boots the machine spin boots a workspace in unless a run asks for another: one vCPU of
a ceiling of 16, 128 MiB of a ceiling of 8 GiB through virtio-mem, four hotplug disks
(spin-stack/spin's `internal/runner/vmm/vmm.go` and `internal/runner/qemu/spec.go`). The options
are `cpus`, `max-cpus`, `memory`, `max-memory`, `hotplug-disks` and `flags` (more spin-machine boot
flags); `--opt cpus=2 --opt memory=2048 --opt max-cpus=0 --opt max-memory=0 --opt hotplug-disks=0`
is the shape the probes booted before they booted spin's. Every table and record says the shape it
measured; a record that names none booted that older one.

Two options make a boot spin's after it starts, and are off unless asked for. `plugged=MIB` sizes
the guest through virtio-mem right after its start, as spin sizes every workspace before handing
it its filesystem (2048 MiB unless the workspace asks for more): left at 128 MiB the guest's OOM
killer runs through the boot and the logins (laptop, 2026-11-20). `login=spin` logs in as spin
does, as root through a forwarder from vsock to `ssh.service` on 127.0.0.1:22, where `lane`, the
default, logs in as spin through systemd's vsock socket and an `sshd@` a connection
(`internal/lab/login.go`, `spinsPath`).

Each release's own numbers are kept in the repository, one record a release in `docs/releases/`,
and `docs/releases.md` is the page written from them, with its chart, `docs/releases.svg`
(docs/releasing.md, "The record"). The same page is published at
https://spin-stack.github.io/spin-machine/:

```
spin-machine-dev releases measure [TAG...] [--reps N] [--run URL]  each release with the one before it, here: its record and the page
spin-machine-dev releases record LADDER_JSON [--run URL]          the record of a ladder's newest release, and the page
spin-machine-dev releases missing                                 the published releases with no record, oldest first
spin-machine-dev releases show RELEASE                            a record: its numbers, where, when, in what shape and against what
spin-machine-dev releases compare A B                             two records side by side: B's Δ when it was measured against A
spin-machine-dev releases doc [--check]                           docs/releases.md and .svg from the records; --check exits 1 when either differs
spin-machine-dev releases site --out OUT                          the same as a web page, OUT/index.html, each record beside it in OUT/releases
spin-machine-dev releases propose                                 the records this checkout wrote, as a pull request against main
```

`measure` runs a ladder of two for each release, the published release before it and the release,
as report.yml does when a release is published, and keeps each ladder.json in
`_output/lab/measured/TAG` (`lab show` reads that directory). The record is the release's, with an
`against` block: the release before it, its numbers from the same run, and the Δ of each number
with its interval. The page's Δ comes only from that block, and is blank for a record measured
alone, as every record was before they were measured in pairs, until `measure` measures it again.
`measure` over many tags, oldest first, backfills: each is measured with its own predecessor. `compare A B`
prints the pair from B's run when B was measured against A, and otherwise both records, each its
own run's, with no Δ and the `lab ladder` that measures them together. `record` takes a ladder
measured some other way and keeps only its newest release, against the release before it in that
ladder. `propose` commits what `git status`
shows changed under `docs/releases`, with the page and its chart, on a branch named for those
releases; a branch already on origin is a set proposed once, and is not proposed again. `site`
writes one HTML file that fetches nothing, the chart inline so its points' tooltips show, from the
same rows as the page's tables; `pages.yml` runs it on every merge to main that changes a record and
deploys it to GitHub Pages. Every verb takes `--dir` for the records' directory (default
`docs/releases`, the page and its chart beside it as `DIR.md` and `DIR.svg`).

`lab compat` is boot:compat over every release since the fingerprint last moved, newest first: a
host that skipped releases still resumes their checkpoints. A release is fetched checked against
its sha256 and its signature, as every release a tree is made from is. It stops at the first
release with another fingerprint, and holds `machine.CanResume` to QEMU's one-way promise against
each release's QEMU on the way.

The probes: `ssh-first` and `restore-first` are the goal's two numbers, `prestart` a QEMU started
ahead against today's, `initcalls` the kernel's boot by initcall. Every probe takes the shape's
options (above) and its own: `restore-first` `fast=true`, `initcalls` `top=N`. A name a probe does
not take is refused. `restore-first`'s cached row reads only the data of the saved state into the
page cache (`SEEK_DATA`), which is all QEMU loads from a mapped-ram state as long as the RAM
ceiling; its `listening` is the restoring QEMU's monitor accepting, not its machine made.

`lab dispatch` starts lab.yml once no other lab run is queued or running - the bench fleet
measures one at a time, and two runs side by side measure each other - waits for it, and prints
its result. It is the way to compare: `host=bench` (nested, the default) unless the question is
nested against metal. `lab run` exits 1 when a measurement was lost (a machine that never
answered, counted in the table's LOST) and 2 when the experiment could not run.

What is not a probe is a check, pass or fail, and stays a Go test with its `boot:` target:
`firmware`, `roms`, `compat` (one release; `lab compat` runs it over many), `hotadd`, `logind`, `lockdown`, and two probes not yet ported,
`firmware-stages` and `waits`. `lab check TARGET` runs one on any tree, here or as lab.yml's
`check:TARGET`.

lab.yml is one step, `task lab:ci`: it makes the trees its `trees` input names
(`NAME=SPEC; NAME=SPEC`), runs the probe, puts the table on the run's summary and keeps
`result.json` as the artifact `lab-RUN_ID`, which `lab show RUN_ID` reads.
