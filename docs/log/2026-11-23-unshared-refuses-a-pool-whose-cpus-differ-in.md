---
date: "2026-11-23"
order: 250
area: machine
title: '`Unshared` refuses a pool whose CPUs differ in what taking features away leaves as it is (#587, this change)'
issues: [587]
---
*Why:* `Unshared` compared only the vendor and the model, so a pool whose hosts differ in stepping or physical address bits got a `CPUWithout` as if it made them share one CPU, and every cross-host resume was then refused by `CanResume`. *Read* (QEMU 11.2.0-rc0's `target/i386/cpu.c`, `host-cpu.c`, `kvm/kvm-cpu.c`, 2026-11-23): under `host`, `family`, `model`, `stepping` and `model-id` are the host's (`host_cpu_instance_init`), `phys-bits` and `guest-phys-bits` the host's and KVM's whenever `lm` is on, `pmu` is set by `kvm_cpu_max_instance_init`, and the cache switches by the machine type and `-cpu`. No feature taken away moves any of them. `level` and `xlevel` can move: `kvm_cpu_max_instance_init` starts the minimum leaves at KVM's, and `x86_cpu_expand_features` raises them for each feature word that is on (AVX10 to 0x24, APX to 0x29, SVM to 0x8000000A). Two hosts' levels can then agree or differ either way once `CPUWithout` is applied, and the record does not hold KVM's own leaf to say which. *Changed:* `Unshared` refuses, naming each, a pool whose hosts differ in any property but `level` and `xlevel`, and leaves those two to the records of the machines started with `CPUWithout`. *Status:* in place; spin-stack/spin#1417 told.
