<!-- Written by `spin-machine-dev releases doc` from docs/releases/*.json: edit those, never this. -->

# Releases, measured

Each release is measured on the bench fleet once it is published (report.yml), booted by the spin-machine of its own commit; a release from before that by `spin-machine-dev releases measure`. The goal is 100 ms (AGENTS.md). Each release is measured in a run of its own, and the bench moves between runs by as much as a release does: two reports taken on different days said v20261119.02 accepted SSH 7.3% later than v20261119.01, and the two interleaved on one host said it did not (labs 44583379559, 44600043625; docs/log.md). A Δ of that size says nothing; to decide on one, measure the two together: `spin-machine-dev lab ladder --newest B --releases N`, N the releases from A to B. `spin-machine-dev releases compare A B` puts two side by side.

![First SSH login, p50 and p95, release by release](releases.svg)

## The goal, release by release

First SSH login, ms p50/p95. Δ is the p50 against the row above.

| release | cold boot to first login | Δ | restore to first login | Δ | cold, QEMU started ahead | restore, QEMU started ahead |
|---|---|---|---|---|---|---|
| v20261113.01 | 462 / 470 |  | - |  | - | - |
| v20261113.02 | 466 / 483 | +4.9 | - |  | - | - |
| v20261113.03 | 377 / 387 | -89.4 | - |  | - | - |
| v20261114.01 | 376 / 386 | -1.5 | - |  | - | - |
| v20261114.02 | 376 / 389 | +0.6 | - |  | - | - |
| v20261119.01 | 332 / 345 | -44.1 | - |  | - | - |
| v20261119.02 | 330 / 345 | -2.5 | 157 / 165 |  | 299 / 313 | 131 / 142 |
| v20261120.01 | 338 / 351 | +8.7 | 159 / 166 | +2.7 | 309 / 317 | 131 / 138 |
| v20261120.02 | 328 / 346 | -10.0 | 158 / 184 | -1.8 | 308 / 322 | 134 / 137 |

v20261120.02 against the goal: cold +228 ms, restore +58 ms.

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

## Where each was measured

| release | measured | host | boots of each probe | run |
|---|---|---|---|---|
| [v20261113.01](releases/v20261113.01.json) | 2026-11-20 | c8id.4xlarge, nested | 20 | [44737371130](https://github.com/spin-stack/spin-machine/actions/runs/44737371130) |
| [v20261113.02](releases/v20261113.02.json) | 2026-11-20 | c8id.4xlarge, nested | 20 | [44737371130](https://github.com/spin-stack/spin-machine/actions/runs/44737371130) |
| [v20261113.03](releases/v20261113.03.json) | 2026-11-20 | c8id.4xlarge, nested | 20 | [44737371130](https://github.com/spin-stack/spin-machine/actions/runs/44737371130) |
| [v20261114.01](releases/v20261114.01.json) | 2026-11-20 | c8id.4xlarge, nested | 20 | [44737371130](https://github.com/spin-stack/spin-machine/actions/runs/44737371130) |
| [v20261114.02](releases/v20261114.02.json) | 2026-11-20 | c8id.4xlarge, nested | 20 | [44737371130](https://github.com/spin-stack/spin-machine/actions/runs/44737371130) |
| [v20261119.01](releases/v20261119.01.json) | 2026-11-20 | c8id.4xlarge, nested | 20 | [44737371130](https://github.com/spin-stack/spin-machine/actions/runs/44737371130) |
| [v20261119.02](releases/v20261119.02.json) | 2026-11-20 | c8id.4xlarge, nested | 20 | [44737371130](https://github.com/spin-stack/spin-machine/actions/runs/44737371130) |
| [v20261120.01](releases/v20261120.01.json) | 2026-11-20 | c8id.4xlarge, nested | 20 | [44737371130](https://github.com/spin-stack/spin-machine/actions/runs/44737371130) |
| [v20261120.02](releases/v20261120.02.json) | 2026-11-20 | c8id.4xlarge, nested | 20 | [44737371130](https://github.com/spin-stack/spin-machine/actions/runs/44737371130) |

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
