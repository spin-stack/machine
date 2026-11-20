<!-- Written by `spin-machine-dev releases doc` from docs/releases/*.json: edit those, never this. -->

# Releases, measured

Each release is measured on the bench fleet once it is published (report.yml): booted interleaved with the release before it, each by the spin-machine of its own commit, in the shape spin boots a workspace in; a release from before that by `spin-machine-dev releases measure`. Each record says the shape it booted, and a number is only read beside one in the same shape. The goal is 100 ms (AGENTS.md). A Δ is read only between two releases booted in the same run, because the bench moves between runs by as much as a release does: v20261120.01's restore to first login was 158.9 ms p50 in one run and 167.0 in another (labs 44641240861, 44727451080). So a release's Δ is against the release measured with it, with its 95% interval, and a Δ whose interval includes 0 is within noise, whatever its size. `spin-machine-dev releases compare A B` puts two side by side.

![First SSH login, p50 and p95, release by release](releases.svg)

## The goal, release by release

First SSH login, ms p50/p95. Δ is the p50 against the release before it, booted interleaved with it in the same run, and its [95% interval]: within noise when the interval includes 0. A release with no Δ was measured with no release before it in its run, and spin-machine-dev releases measure measures it again with the one before it.

| release | cold boot to first login | Δ | restore to first login | Δ | cold, QEMU started ahead | restore, QEMU started ahead |
|---|---|---|---|---|---|---|
| v20261113.01 | 462 / 470 |  | - |  | - | - |
| v20261113.02 | 466 / 483 |  | - |  | - | - |
| v20261113.03 | 377 / 387 |  | - |  | - | - |
| v20261114.01 | 376 / 386 |  | - |  | - | - |
| v20261114.02 | 376 / 389 |  | - |  | - | - |
| v20261119.01 | 332 / 345 |  | - |  | - | - |
| v20261119.02 | 330 / 345 |  | 157 / 165 |  | 299 / 313 | 131 / 142 |
| v20261120.01 | 338 / 351 |  | 159 / 166 |  | 309 / 317 | 131 / 138 |
| v20261120.02 | 328 / 346 |  | 158 / 184 |  | 308 / 322 | 134 / 137 |
| v20261120.03 | 473 / 487 | -38.0 [-42.3, -29.0] | 128 / 132 | -55.8 [-58.9, -52.1] | 439 / 451 | 99.5 / 103 |

v20261120.03 against the goal: cold +373 ms, restore +28 ms.

## Diagnostics

Not targets: what moves behind the goal's numbers (AGENTS.md, "The goal"). p50.

| release | save ms | state MiB | kernel to "Freeing" ms | QEMU RSS MiB |
|---|---|---|---|---|
| v20261113.01 | - | - | 86.2 | 215 |
| v20261113.02 | - | - | 86.3 | 213 |
| v20261113.03 | - | - | 76 | 215 |
| v20261114.01 | - | - | 75.1 | 215 |
| v20261114.02 | - | - | 75.3 | 215 |
| v20261119.01 | - | - | 76 | 209 |
| v20261119.02 | 160 | 156 | 64 | 207 |
| v20261120.01 | 164 | 155 | 65.7 | 207 |
| v20261120.02 | 162 | 156 | 65.9 | 207 |
| v20261120.03 | 126 | 178 | 70.1 | 234 |

## Where each was measured

| release | measured | host | shape | boots of each probe | against | run |
|---|---|---|---|---|---|---|
| [v20261113.01](releases/v20261113.01.json) | 2026-11-20 | c8id.4xlarge, nested | 2 vCPUs, 2048 MiB, no hotplug disks | 20 |  | [44737371130](https://github.com/spin-stack/spin-machine/actions/runs/44737371130) |
| [v20261113.02](releases/v20261113.02.json) | 2026-11-20 | c8id.4xlarge, nested | 2 vCPUs, 2048 MiB, no hotplug disks | 20 |  | [44737371130](https://github.com/spin-stack/spin-machine/actions/runs/44737371130) |
| [v20261113.03](releases/v20261113.03.json) | 2026-11-20 | c8id.4xlarge, nested | 2 vCPUs, 2048 MiB, no hotplug disks | 20 |  | [44737371130](https://github.com/spin-stack/spin-machine/actions/runs/44737371130) |
| [v20261114.01](releases/v20261114.01.json) | 2026-11-20 | c8id.4xlarge, nested | 2 vCPUs, 2048 MiB, no hotplug disks | 20 |  | [44737371130](https://github.com/spin-stack/spin-machine/actions/runs/44737371130) |
| [v20261114.02](releases/v20261114.02.json) | 2026-11-20 | c8id.4xlarge, nested | 2 vCPUs, 2048 MiB, no hotplug disks | 20 |  | [44737371130](https://github.com/spin-stack/spin-machine/actions/runs/44737371130) |
| [v20261119.01](releases/v20261119.01.json) | 2026-11-20 | c8id.4xlarge, nested | 2 vCPUs, 2048 MiB, no hotplug disks | 20 |  | [44737371130](https://github.com/spin-stack/spin-machine/actions/runs/44737371130) |
| [v20261119.02](releases/v20261119.02.json) | 2026-11-20 | c8id.4xlarge, nested | 2 vCPUs, 2048 MiB, no hotplug disks | 20 |  | [44737371130](https://github.com/spin-stack/spin-machine/actions/runs/44737371130) |
| [v20261120.01](releases/v20261120.01.json) | 2026-11-20 | c8id.4xlarge, nested | 2 vCPUs, 2048 MiB, no hotplug disks | 20 |  | [44737371130](https://github.com/spin-stack/spin-machine/actions/runs/44737371130) |
| [v20261120.02](releases/v20261120.02.json) | 2026-11-20 | c8id.4xlarge, nested | 2 vCPUs, 2048 MiB, no hotplug disks | 20 |  | [44737371130](https://github.com/spin-stack/spin-machine/actions/runs/44737371130) |
| [v20261120.03](releases/v20261120.03.json) | 2026-11-20 | c8id.4xlarge, nested | 1 of 16 vCPUs, 128 of 8192 MiB, 4 hotplug disks, sized to 2048 MiB, spin's login | 20 | v20261120.02 | [44805222647](https://github.com/spin-stack/spin-machine/actions/runs/44805222647) |

A dash is a number that release has not:

- v20261113.01, restore to first login: its spin-machine's boot has no --restore
- v20261113.01, cold, QEMU started ahead: its spin-machine's boot has no --held
- v20261113.01, restore, QEMU started ahead: its spin-machine's boot has no --held
- v20261113.01, save ms: its spin-machine's boot has no --restore
- v20261113.01, state MiB: its spin-machine's boot has no --restore
- v20261113.02, restore to first login: its spin-machine's boot has no --restore
- v20261113.02, cold, QEMU started ahead: its spin-machine's boot has no --held
- v20261113.02, restore, QEMU started ahead: its spin-machine's boot has no --held
- v20261113.02, save ms: its spin-machine's boot has no --restore
- v20261113.02, state MiB: its spin-machine's boot has no --restore
- v20261113.03, restore to first login: its spin-machine's boot has no --restore
- v20261113.03, cold, QEMU started ahead: its spin-machine's boot has no --held
- v20261113.03, restore, QEMU started ahead: its spin-machine's boot has no --held
- v20261113.03, save ms: its spin-machine's boot has no --restore
- v20261113.03, state MiB: its spin-machine's boot has no --restore
- v20261114.01, restore to first login: its spin-machine's boot has no --restore
- v20261114.01, cold, QEMU started ahead: its spin-machine's boot has no --held
- v20261114.01, restore, QEMU started ahead: its spin-machine's boot has no --held
- v20261114.01, save ms: its spin-machine's boot has no --restore
- v20261114.01, state MiB: its spin-machine's boot has no --restore
- v20261114.02, restore to first login: its spin-machine's boot has no --restore
- v20261114.02, cold, QEMU started ahead: its spin-machine's boot has no --held
- v20261114.02, restore, QEMU started ahead: its spin-machine's boot has no --held
- v20261114.02, save ms: its spin-machine's boot has no --restore
- v20261114.02, state MiB: its spin-machine's boot has no --restore
- v20261119.01, restore to first login: its spin-machine's boot has no --restore
- v20261119.01, cold, QEMU started ahead: its spin-machine's boot has no --held
- v20261119.01, restore, QEMU started ahead: its spin-machine's boot has no --held
- v20261119.01, save ms: its spin-machine's boot has no --restore
- v20261119.01, state MiB: its spin-machine's boot has no --restore
