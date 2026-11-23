#!/bin/bash
# Experiment #611 only: the host's settings that could limit QEMU, into $1.
out=$1
mkdir -p "$out"
cd "$out" || exit 1
{
  echo "## uname -r"; uname -r
  echo "## /proc/cmdline"; cat /proc/cmdline
  echo "## lscpu"; lscpu
  echo "## THP"
  for f in /sys/kernel/mm/transparent_hugepage/enabled /sys/kernel/mm/transparent_hugepage/defrag /sys/kernel/mm/transparent_hugepage/khugepaged/defrag /sys/kernel/mm/ksm/run; do echo "$f: $(cat $f 2>&1)"; done
  for m in kvm kvm_intel kvm_amd; do
    [ -d /sys/module/$m/parameters ] || continue
    echo "## /sys/module/$m/parameters"
    for f in /sys/module/$m/parameters/*; do echo "$(basename $f): $(sudo cat $f 2>&1)"; done
  done
  echo "## cpufreq"
  for f in /sys/devices/system/cpu/cpu0/cpufreq/scaling_governor /sys/devices/system/cpu/cpu0/cpufreq/scaling_driver /sys/devices/system/cpu/cpu0/cpufreq/scaling_min_freq /sys/devices/system/cpu/cpu0/cpufreq/scaling_max_freq /sys/devices/system/cpu/cpu0/cpufreq/energy_performance_preference; do echo "$f: $(cat $f 2>&1)"; done
  echo "governors: $(cat /sys/devices/system/cpu/cpu*/cpufreq/scaling_governor 2>/dev/null | sort | uniq -c | tr '\n' ' ')"
  for f in /sys/devices/system/cpu/intel_pstate/*; do [ -f "$f" ] && echo "$f: $(cat $f 2>&1)"; done
  echo "## cpuidle (cpu0)"
  echo "driver: $(cat /sys/devices/system/cpu/cpuidle/current_driver 2>&1) governor: $(cat /sys/devices/system/cpu/cpuidle/current_governor 2>&1)"
  for s in /sys/devices/system/cpu/cpu0/cpuidle/state*; do [ -d "$s" ] && echo "$(basename $s) $(cat $s/name) latency=$(cat $s/latency) disable=$(cat $s/disable) usage=$(cat $s/usage)"; done
  echo "disabled anywhere: $(cat /sys/devices/system/cpu/cpu*/cpuidle/state*/disable 2>/dev/null | sort | uniq -c | tr '\n' ' ')"
  echo "## ulimit -a ($(id -un))"; ulimit -a
  echo "## cgroup"; cat /proc/self/cgroup
  cg=/sys/fs/cgroup$(sed -n 's/^0:://p' /proc/self/cgroup)
  for d in "$cg" "$(dirname "$cg")" "$(dirname "$(dirname "$cg")")"; do
    echo "# $d"
    for f in cpu.max cpu.weight memory.max memory.high pids.max io.max cpuset.cpus.effective cpuset.mems.effective; do echo "$f: $(cat $d/$f 2>&1)"; done
  done
  echo "## sched"; for f in /proc/sys/kernel/sched_* /proc/sys/kernel/numa_balancing; do echo "$f: $(cat $f 2>&1)"; done
  echo "## vulnerabilities"; grep -r . /sys/devices/system/cpu/vulnerabilities/ 2>/dev/null
  echo "## nproc $(nproc), loadavg $(cat /proc/loadavg)"
} > host-settings.txt 2>&1
sudo sysctl -a > sysctl-a.txt 2>/dev/null
exit 0
