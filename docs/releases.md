<!-- Written by `spin-machine-dev releases doc` from docs/releases/*.json: edit those, never this. -->

# Releases, measured

The bench fleet measures each release once it is published (report.yml). It boots the release interleaved with the release before it, each by the spin-machine of its own commit, in the shape spin boots a workspace in. A release from before report.yml was measured by `spin-machine-dev releases measure`. Each record says the shape it booted, and a number is read only beside one in the same shape. The goal is 100 ms (AGENTS.md). A Δ is read only between two releases booted in the same run, because the bench moves between runs by as much as a release does: v20261120.01's restore to first login was 158.9 ms p50 in one run and 167.0 in another (labs 44641240861, 44727451080). So a release's Δ is against the release measured with it, with its 95% interval. A Δ whose interval includes 0 is within noise, whatever its size. `spin-machine-dev releases compare A B` puts two side by side.

![First SSH login, p50 and p95, release by release](releases.svg)

## The goal, release by release

First SSH login, ms p50/p95. Δ is the p50 against the release before it, booted interleaved with it in the same run, and its [95% interval]: within noise when the interval includes 0.

| release | cold boot to first login | Δ | restore to first login | Δ | cold, QEMU started ahead | restore, QEMU started ahead |
|---|---|---|---|---|---|---|
| v20261119.02 | 518 / 536 |  | 181 / 183 |  | 476 / 485 | 154 / 162 |
| v20261120.01 | 508 / 516 | -3.2 [-10.1, +4.6] within noise | 185 / 305 | +2.1 [-2.9, +9.6] within noise | 482 / 491 | 156 / 172 |
| v20261120.02 | 502 / 513 | +1.2 [-4.0, +15.7] within noise | 182 / 632 | +1.1 [-2.0, +6.1] within noise | 472 / 484 | 155 / 162 |
| v20261120.03 | 474 / 485 | -35.7 [-43.1, -27.6] | 125 / 132 | -46.7 [-48.2, -43.5] | 446 / 459 | 102 / 106 |
| v20261120.04 | 456 / 477 | -14.3 [-20.4, -8.2] | 108 / 111 | -13.2 [-15.1, -11.4] | 428 / 440 | 83.2 / 87.1 |
| v20261121.01 | 464 / 486 | +1.1 [-1.7, +9.9] within noise | 110 / 116 | -2.2 [-4.7, +1.6] within noise | 442 / 461 | 83.7 / 90.8 |
| v20261122.01 | 464 / 485 | +5.0 [-0.2, +9.3] within noise | 110 / 115 | +2.5 [-0.8, +5.0] within noise | 432 / 449 | 83.6 / 88.4 |
| v20261122.02 | 411 / 425 | -57.4 [-65.4, -46.7] | 96.2 / 101 | -7.9 [-15.1, -4.0] | 383 / 395 | 80.1 / 92.7 |
| v20261122.03 | 410 / 429 | +2.3 [-3.7, +9.6] within noise | 90.2 / 98.6 | +4.5 [-7.1, +9.4] within noise | 381 / 388 | 82.4 / 91 |
| v20261122.04 | 418 / 438 | +6.8 [+0.1, +13.6] | 85.8 / 89.4 | -5.4 [-9.2, +0.1] within noise | 388 / 407 | 71.9 / 77.3 |
| v20261122.05 | 419 / 432 | +2.8 [-8.6, +9.9] within noise | 85.2 / 92.6 | +1.0 [-1.5, +4.2] within noise | 392 / 406 | 73.6 / 82.3 |
| v20261122.06 | 414 / 433 | -1.1 [-6.6, +11.9] within noise | 84.1 / 90 | -1.4 [-2.8, +3.6] within noise | 384 / 398 | 72.4 / 85.3 |
| v20261122.07 | 408 / 426 | -6.5 [-13.7, +4.3] within noise | 83.1 / 91.9 | -2.7 [-5.5, +1.1] within noise | 384 / 398 | 72.2 / 81 |
| v20261123.01 | 433 / 454 | -5.0 [-20.4, +6.8] within noise | 85.7 / 89.8 | -0.6 [-4.4, +3.4] within noise | 403 / 420 | 75.1 / 79.1 |
| v20261123.02 | 399 / 412 | -10.6 [-19.4, -0.5] | 83 / 86.3 | -0.1 [-2.4, +2.5] within noise | 372 / 386 | 72.9 / 78.6 |
| v20261123.03 | 393 / 410 | -23.5 [-30.5, -14.3] | 81 / 83.7 | -1.5 [-2.9, -0.1] | 366 / 381 | 70.3 / 76.7 |

v20261123.03 against the goal: cold +293 ms, restore -19 ms.

## Diagnostics

Not targets: what moves behind the goal's numbers (AGENTS.md, "The goal"). p50.

| release | save ms | state MiB | kernel to "Freeing" ms | QEMU RSS MiB |
|---|---|---|---|---|
| v20261119.02 | 180 | 183 | 90.2 | 240 |
| v20261120.01 | 181 | 183 | 88.8 | 240 |
| v20261120.02 | 176 | 182 | 89.3 | 238 |
| v20261120.03 | 122 | 178 | 70.8 | 234 |
| v20261120.04 | 122 | 178 | 79.9 | 232 |
| v20261121.01 | 125 | 179 | 82.6 | 232 |
| v20261122.01 | 122 | 179 | 82.2 | 232 |
| v20261122.02 | 138 | 179 | 83.2 | 230 |
| v20261122.03 | 139 | 179 | 82.4 | 230 |
| v20261122.04 | 139 | 179 | 81.9 | 228 |
| v20261122.05 | 139 | 179 | 83.7 | 228 |
| v20261122.06 | 138 | 179 | 81.9 | 228 |
| v20261122.07 | 139 | 179 | 82.8 | 228 |
| v20261123.01 | 140 | 179 | 86.8 | 228 |
| v20261123.02 | 138 | 178 | 83 | 228 |
| v20261123.03 | 133 | 183 | 86.2 | 226 |

## Bounds

Never candidates, measured with each release so that they stay bounds (docs/measuring.md). The no-systemd floor: the release's first SSH login by the lane, ms p50/p95, and the same release with no systemd before sshd, booted interleaved in the same run. Δ is the second's p50 less the first's and its [95% interval], read as a release's Δ is: the gap bounds what systemd costs.

| release | first login by the lane | no systemd before sshd | Δ |
|---|---|---|---|
| v20261119.02 | - | - |  |
| v20261120.01 | - | - |  |
| v20261120.02 | - | - |  |
| v20261120.03 | - | - |  |
| v20261120.04 | - | - |  |
| v20261121.01 | - | - |  |
| v20261122.01 | - | - |  |
| v20261122.02 | - | - |  |
| v20261122.03 | 399 / 414 | 232 / 239 | -166.7 [-171.9, -159.3] |
| v20261122.04 | 435 / 451 | 232 / 239 | -202.4 [-207.3, -198.6] |
| v20261122.05 | 442 / 460 | 237 / 246 | -205.4 [-212.9, -196.2] |
| v20261122.06 | 434 / 444 | 230 / 235 | -204.2 [-208.5, -200.3] |
| v20261122.07 | 436 / 459 | 232 / 240 | -203.8 [-210.1, -193.3] |
| v20261123.01 | 449 / 468 | 246 / 260 | -203.8 [-210.8, -196.5] |
| v20261123.02 | 413 / 426 | 229 / 236 | -184.3 [-189.2, -178.0] |
| v20261123.03 | 395 / 408 | 236 / 242 | -158.5 [-162.6, -152.0] |

## Where each was measured

| release | measured | host | shape | boots of each probe | against | run | bootchart | units | ledger |
|---|---|---|---|---|---|---|---|---|---|
| [v20261119.02](releases/v20261119.02.json) | 2026-11-20 | c8id.4xlarge, nested | 1 of 16 vCPUs, 128 of 8192 MiB, 4 hotplug disks, sized to 2048 MiB, spin's login | 20 | v20261119.01 | [44820465606](https://github.com/spin-stack/spin-machine/actions/runs/44820465606) |  |  |  |
| [v20261120.01](releases/v20261120.01.json) | 2026-11-20 | c8id.4xlarge, nested | 1 of 16 vCPUs, 128 of 8192 MiB, 4 hotplug disks, sized to 2048 MiB, spin's login | 20 | v20261119.02 | [44820465606](https://github.com/spin-stack/spin-machine/actions/runs/44820465606) |  |  |  |
| [v20261120.02](releases/v20261120.02.json) | 2026-11-20 | c8id.4xlarge, nested | 1 of 16 vCPUs, 128 of 8192 MiB, 4 hotplug disks, sized to 2048 MiB, spin's login | 20 | v20261120.01 | [44820465606](https://github.com/spin-stack/spin-machine/actions/runs/44820465606) |  |  |  |
| [v20261120.03](releases/v20261120.03.json) | 2026-11-20 | c8id.4xlarge, nested | 1 of 16 vCPUs, 128 of 8192 MiB, 4 hotplug disks, sized to 2048 MiB, spin's login | 20 | v20261120.02 | [44809542263](https://github.com/spin-stack/spin-machine/actions/runs/44809542263) |  |  |  |
| [v20261120.04](releases/v20261120.04.json) | 2026-11-20 | c8id.4xlarge, nested | 1 of 16 vCPUs, 128 of 8192 MiB, 4 hotplug disks, sized to 2048 MiB, spin's login | 20 | v20261120.03 | [44811347183](https://github.com/spin-stack/spin-machine/actions/runs/44811347183) |  |  |  |
| [v20261121.01](releases/v20261121.01.json) | 2026-11-21 | c8id.4xlarge, nested | 1 of 16 vCPUs, 128 of 8192 MiB, 16 hotplug disks, sized to 2048 MiB, spin's login | 20 | v20261120.04 | [44911249078](https://github.com/spin-stack/machine/actions/runs/44911249078) |  |  |  |
| [v20261122.01](releases/v20261122.01.json) | 2026-11-22 | c8id.4xlarge, nested | 1 of 16 vCPUs, 128 of 8192 MiB, 16 hotplug disks, sized to 2048 MiB, spin's login | 20 | v20261121.01 | [44973276221](https://github.com/spin-stack/machine/actions/runs/44973276221) |  |  |  |
| [v20261122.02](releases/v20261122.02.json) | 2026-11-22 | c8id.4xlarge, nested | 1 of 16 vCPUs, 128 of 8192 MiB, 16 hotplug disks, sized to 2048 MiB, spin's login | 20 | v20261122.01 | [44990321803](https://github.com/spin-stack/machine/actions/runs/44990321803) |  |  |  |
| [v20261122.03](releases/v20261122.03.json) | 2026-11-22 | c8id.4xlarge, nested | 1 of 16 vCPUs, 128 of 8192 MiB, 16 hotplug disks, sized to 2048 MiB, spin's login | 20 | v20261122.02 | [45029297865](https://github.com/spin-stack/machine/actions/runs/45029297865) |  |  |  |
| [v20261122.04](releases/v20261122.04.json) | 2026-11-22 | c8id.4xlarge, nested | 1 of 16 vCPUs, 128 of 8192 MiB, 16 hotplug disks, sized to 2048 MiB, spin's login | 20 | v20261122.03 | [45041820698](https://github.com/spin-stack/machine/actions/runs/45041820698) | [processes](releases/v20261122.04-bootchart.svg) | [units](releases/v20261122.04-plot.svg) |  |
| [v20261122.05](releases/v20261122.05.json) | 2026-11-22 | c8id.4xlarge, nested | 1 of 16 vCPUs, 128 of 8192 MiB, 16 hotplug disks, sized to 2048 MiB, spin's login | 20 | v20261122.04 | [45057290275](https://github.com/spin-stack/machine/actions/runs/45057290275) | [processes](releases/v20261122.05-bootchart.svg) | [units](releases/v20261122.05-plot.svg) |  |
| [v20261122.06](releases/v20261122.06.json) | 2026-11-22 | c8id.4xlarge, nested | 1 of 16 vCPUs, 128 of 8192 MiB, 16 hotplug disks, sized to 2048 MiB, spin's login | 20 | v20261122.05 | [45067270005](https://github.com/spin-stack/machine/actions/runs/45067270005) | [processes](releases/v20261122.06-bootchart.svg) | [units](releases/v20261122.06-plot.svg) |  |
| [v20261122.07](releases/v20261122.07.json) | 2026-11-22 | c8id.4xlarge, nested | 1 of 16 vCPUs, 128 of 8192 MiB, 16 hotplug disks, sized to 2048 MiB, spin's login | 20 | v20261122.06 | [45077375325](https://github.com/spin-stack/machine/actions/runs/45077375325) | [processes](releases/v20261122.07-bootchart.svg) | [units](releases/v20261122.07-plot.svg) | [ledger](releases/v20261122.07-ledger.txt) |
| [v20261123.01](releases/v20261123.01.json) | 2026-11-23 | c8id.4xlarge, nested | 1 of 16 vCPUs, 128 of 8192 MiB, 16 hotplug disks, sized to 2048 MiB, spin's login | 20 | v20261122.07 | [45085900639](https://github.com/spin-stack/machine/actions/runs/45085900639) | [processes](releases/v20261123.01-bootchart.svg) | [units](releases/v20261123.01-plot.svg) | [ledger](releases/v20261123.01-ledger.txt) |
| [v20261123.02](releases/v20261123.02.json) | 2026-11-23 | c8id.4xlarge, nested | 1 of 16 vCPUs, 128 of 8192 MiB, 16 hotplug disks, sized to 2048 MiB, spin's login | 20 | v20261123.01 | [45095165628](https://github.com/spin-stack/machine/actions/runs/45095165628) | [processes](releases/v20261123.02-bootchart.svg) | [units](releases/v20261123.02-plot.svg) | [ledger](releases/v20261123.02-ledger.txt) |
| [v20261123.03](releases/v20261123.03.json) | 2026-11-23 | c8id.4xlarge, nested | 1 of 16 vCPUs, 128 of 8192 MiB, 16 hotplug disks, sized to 2048 MiB, spin's login | 20 | v20261123.02 | [45105007693](https://github.com/spin-stack/machine/actions/runs/45105007693) | [processes](releases/v20261123.03-bootchart.svg) | [units](releases/v20261123.03-plot.svg) | [ledger](releases/v20261123.03-ledger.txt) |
