#!/usr/bin/env python3
"""The critical path of each ledger boot, and the phases of many boots with their intervals.

Reads the files the ledger probe keeps beside result.json (TREE-REP.ledger.json and
TREE-REP.trace.txt) for every repetition of every tree in a lab run's artifact, and prints:

1. each window of the ledger (docs/measuring.md, "The ledger") with its p50, mean and the
   bootstrap 95% interval of its p50 over the boots, so the phases add up to first_login (the
   means do exactly, by the ledger's construction);
2. milestones on the kernel's clock (the first exec of each program the login's chain and
   logind's need) and the spans between them, with the same intervals;
3. the login's critical path: walking back from /usr/bin/true's exit through each task's
   scheduler states, jumping to the waker at every wakeup a task made, so each millisecond of the
   path is one of
     run        a task on the path running (by its process),
     cpu-wait   a task on the path runnable while others held the vCPU (by who held it),
     disk       a task on the path in uninterruptible sleep woken from interrupt context,
     irq-wait   a task on the path asleep (S) woken from interrupt context: a timer, the
                host over vsock, a device;
   the path's categories per window are means over the boots, so they add up to the window's
   mean. Which task the path names is inferred: through PID 1's and logind's event loops the
   last wakeup before a fork need not be the message the fork waited for;
4. on a tree booted with the block, irq and vsock tracepoints, the disk's reads and the host's
   refused vsock connections before the login's connection, and each device's interrupt time.

Python 3 standard library only. Usage: ledgerpath.py ARTIFACT_DIR [TREE...]
"""

import collections
import glob
import json
import os
import random
import re
import statistics
import sys

LINE = re.compile(r"^\s*(.+?)-(\d+)\s+(?:\(.*?\)\s+)?\[(\d+)\]\s+(\S+)\s+(\d+\.\d+): (\w+): (.*)$")
SWITCH = re.compile(r"^prev_comm=(.*) prev_pid=(\d+) prev_prio=-?\d+ prev_state=(\S+) ==> next_comm=(.*) next_pid=(\d+) next_prio=-?\d+")
WAKE = re.compile(r"^comm=(.*) pid=(\d+) prio=-?\d+ target_cpu=(\d+)")
FORK = re.compile(r"^comm=.* pid=(\d+) child_comm=(.*) child_pid=(\d+)")
EXEC = re.compile(r"^filename=(.*) pid=(\d+) old_pid=\d+")
EXIT = re.compile(r"^comm=(.*) pid=(\d+) prio=-?\d+")
IRQ_ENTRY = re.compile(r"^irq=(\d+) name=(\S+)")
IRQ_EXIT = re.compile(r"^irq=(\d+) ret=")
BLOCK = re.compile(r"^(\d+,\d+) (\S+) (\d+ )?\(.*?\) (\d+) \+ (\d+)")
VSOCK = re.compile(r"op=(\w+)")


# The programs of the login's pipe, which talk over loopback TCP: the forwarder and sshd.
LOOPBACK = {"socat", "sshd", "sshd-session", "sshd-auth"}


def in_irq(flags):
    return len(flags) > 2 and flags[2] in "hHsZz"


class Boot:
    def __init__(self, trace_path, lag):
        self.lag = lag
        self.segs = collections.defaultdict(list)  # pid -> [(start, end, kind, info)]
        self.runs = []  # (cpu, pid, start, end)
        self.parent, self.forked, self.execs, self.comm = {}, {}, collections.defaultdict(list), {}
        self.irq_names = {}
        self.irq_time = collections.Counter()
        self.counts = collections.Counter()
        self.true_exit = None
        self.blocks = []  # (issued, completed, sectors, rwbs)
        self.vsock = []  # (at, op, kworker pid)
        self.irqs = []  # (start, end, name)
        self.first_exec = {}  # file, or "socat's first fork" -> first time
        self._replay(trace_path)

    def _replay(self, path):
        on = {}  # cpu -> (pid, since)
        off = {}  # pid -> (kind, since, info)
        irq_open = {}  # cpu -> (irq, at)
        pending = {}  # (device, sector) -> (issued, sectors, rwbs)
        socat_pid = None
        last = 0.0
        true_pids = set()
        for line in open(path, errors="replace"):
            if not line.strip() or line.startswith("#"):
                continue
            m = LINE.match(line.rstrip("\n"))
            if not m:
                continue
            cur_pid, cpu, flags = int(m.group(2)), int(m.group(3)), m.group(4)
            at = float(m.group(5)) * 1000 + self.lag
            ev, body = m.group(6), m.group(7)
            last = at
            self.counts[ev] += 1
            if ev == "sched_switch":
                f = SWITCH.match(body)
                prev, state, nxt = int(f.group(2)), f.group(3), int(f.group(5))
                self.comm.setdefault(prev, f.group(1))
                self.comm.setdefault(nxt, f.group(4))
                p, since = on.get(cpu, (prev, at))
                self.runs.append((cpu, prev, since, at))
                self.segs[prev].append((since, at, "run", cpu))
                if prev != 0:
                    if state.startswith("R"):
                        off[prev] = ("runnable", at, ("preempted",))
                    elif state.startswith("D"):
                        off[prev] = ("D", at, None)
                    elif state[0] in "XZ":
                        pass
                    else:
                        off[prev] = ("S", at, None)
                if nxt in off:
                    k, s, info = off.pop(nxt)
                    self.segs[nxt].append((s, at, k, info))
                on[cpu] = (nxt, at)
            elif ev in ("sched_wakeup", "sched_wakeup_new"):
                f = WAKE.match(body)
                pid = int(f.group(2))
                self.comm.setdefault(pid, f.group(1))
                cause = ("new" if ev == "sched_wakeup_new" else "wake", cur_pid, in_irq(flags), irq_open.get(cpu))
                prior = off.get(pid)
                if prior is None or prior[0] != "runnable":
                    if prior is not None:
                        self.segs[pid].append((prior[1], at, prior[0], None))
                    off[pid] = ("runnable", at, cause)
            elif ev == "sched_process_fork":
                f = FORK.match(body)
                child = int(f.group(3))
                self.parent[child], self.forked[child] = int(f.group(1)), at
                if int(f.group(1)) == socat_pid:
                    self.first_exec.setdefault("socat's first fork", at)
                self.comm[child] = f.group(2)
            elif ev == "sched_process_exec":
                f = EXEC.match(body)
                pid = int(f.group(2))
                self.execs[pid].append((at, f.group(1)))
                self.first_exec.setdefault(f.group(1), at)
                if f.group(1) == "/usr/bin/socat":
                    socat_pid = pid
                if f.group(1) == "/usr/bin/true":
                    true_pids.add(pid)
            elif ev == "sched_process_exit":
                f = EXIT.match(body)
                pid = int(f.group(2))
                if pid in true_pids and self.true_exit is None:
                    self.true_exit = (pid, at)
            elif ev in ("block_rq_issue", "block_rq_complete"):
                f = BLOCK.match(body)
                if f and f.group(1) != "0,0":
                    key = (f.group(1), int(f.group(4)))
                    if ev == "block_rq_issue":
                        pending[key] = (at, int(f.group(5)), f.group(2))
                    elif key in pending:
                        i, n, rwbs = pending.pop(key)
                        self.blocks.append((i, at, n, rwbs))
            elif ev == "virtio_transport_recv_pkt":
                f = VSOCK.search(body)
                self.vsock.append((at, f.group(1) if f else "?", cur_pid))
            elif ev == "irq_handler_entry":
                f = IRQ_ENTRY.match(body)
                irq_open[cpu] = (int(f.group(1)), at)
                self.irq_names[int(f.group(1))] = f.group(2)
            elif ev == "irq_handler_exit":
                f = IRQ_EXIT.match(body)
                o = irq_open.pop(cpu, None)
                if o:
                    self.irq_time[self.irq_names.get(o[0], str(o[0]))] += at - o[1]
                    self.irqs.append((o[1], at, self.irq_names.get(o[0], str(o[0]))))
        for cpu, (pid, since) in on.items():
            self.runs.append((cpu, pid, since, last))
            self.segs[pid].append((since, last, "run", cpu))
        for pid in self.segs:
            self.segs[pid].sort()
        self.runs.sort(key=lambda r: r[2])
        self.ncpu = len({r[0] for r in self.runs})

    def label(self, pid):
        """A process's name: the file its unit's top process last exec'd, below PID 1, else its comm."""
        if pid == 0:
            return "idle"
        if pid == 1:
            return "PID 1"
        p, seen = pid, set()
        while p in self.parent and self.parent[p] not in (0, 1, 2) and p not in seen:
            seen.add(p)
            p = self.parent[p]
        if self.parent.get(p) == 2 or p == 2:
            return "kernel threads"
        name = None
        for q in (pid, p):
            if self.execs.get(q):
                name = os.path.basename(self.execs[q][-1][1])
                break
        return name or self.comm.get(pid, str(pid))

    def find(self, pid, t):
        """The index of pid's segment that holds the instant just before t, or None."""
        best = None
        for i, (s, e, _, _) in enumerate(self.segs.get(pid, ())):
            if s > t:
                break
            if s < t <= e + 1e-9 or s == e == t:
                best = i
        return best

    def holders(self, a, b):
        """Who held the vCPUs over [a, b): ms by label, over every CPU, divided by the CPU count."""
        out = collections.Counter()
        for cpu, pid, s, e in self.runs:
            if e <= a:
                continue
            if s >= b:
                break
            out[self.label(pid)] += (min(e, b) - max(s, a)) / self.ncpu
        return out

    def is_kthread(self, pid):
        return pid == 2 or self.parent.get(pid) == 2

    def critical_path(self, t_min=0.0):
        """[(start, end, category, detail)] from t_min to the login's command's exit.

        At a wakeup the path goes on in the waker, unless the wakeup came from interrupt context:
        a disk's completion (the task was in D), a timer or a device. Loopback TCP is the exception:
        it wakes the receiver in softirq inside its sender's syscall, so between the programs of
        the login's pipe (LOOPBACK) a softirq wakeup goes on in the task it ran in. A kernel thread
        woken from interrupt context relays for the task it then woke, whose wait is the device's.
        """
        if not self.true_exit:
            raise ValueError("no exit of /usr/bin/true in the trace")
        pid, t = self.true_exit
        idx = self.find(pid, t)
        path = []
        relay = None  # (task, its sleep's start, its state) when the path went into a kernel thread
        guard = 0
        while t > t_min and guard < 500000:
            guard += 1
            if idx is None or idx < 0:
                if pid in self.forked and self.forked[pid] <= t:
                    t, pid = self.forked[pid], self.parent[pid]
                    idx = self.find(pid, t)
                    continue
                path.append((t_min, t, "untraced", self.label(pid)))
                break
            s, e, kind, info = self.segs[pid][idx]
            s = max(s, t_min)
            if kind == "run":
                path.append((s, t, "run", self.label(pid)))
                t, idx = s, idx - 1
                continue
            if kind != "runnable":
                path.append((s, t, "untraced-" + kind, self.label(pid)))
                t, idx = s, idx - 1
                continue
            path.append((s, t, "cpu-wait", self.label(pid)))
            t = s
            if not info or info[0] not in ("wake", "new"):
                idx -= 1
                continue
            how, waker, irq, irqo = info
            before = self.segs[pid][idx - 1] if how == "wake" and idx > 0 else None
            state = before[2] if before and before[2] in ("S", "D") else None
            loopback = irq and state == "S" and self.label(pid) in LOOPBACK and self.label(waker) in LOOPBACK
            if how == "new" or (not irq and waker != 0) or loopback:
                if self.is_kthread(waker) and state and not self.is_kthread(pid):
                    relay = (pid, before[0], state)
                pid = waker
                idx = self.find(pid, t)
                continue
            src = self.irq_names.get(irqo[0], "") if irqo else ""
            src = f" [{src}]" if src else ""
            if relay and self.is_kthread(pid):
                rpid, rstart, rstate = relay
                relay = None
                cat = "disk" if rstate == "D" else "irq-wait"
                path.append((max(rstart, t_min), t, cat, f"{self.label(rpid)} via {self.comm.get(pid, pid)}{src}"))
                pid, t = rpid, max(rstart, t_min)
                idx = self.find(pid, t)
                continue
            if state:
                cat = "disk" if state == "D" else "irq-wait"
                path.append((max(before[0], t_min), t, cat, self.label(pid) + src))
                t, idx = max(before[0], t_min), idx - 2
            else:
                idx -= 1
        return path


# Milestones on the kernel's clock: the first exec of each file, and the forwarder's first fork,
# which is the host's connection accepted.
MILESTONES = [
    ("systemd", "/sbin/init"),
    ("first generator", "/usr/lib/systemd/system-generators/"),
    ("first unit's executor", "/proc/self/fd/9"),
    ("sshd", "/usr/sbin/sshd"),
    ("dbus-broker-launch", "/usr/bin/dbus-broker-launch"),
    ("socat (the forwarder)", "/usr/bin/socat"),
    ("systemd-journald", "/usr/lib/systemd/systemd-journald"),
    ("connection accepted", "socat's first fork"),
    ("sshd-session", "/usr/lib/openssh/sshd-session"),
    ("systemd-logind", "/usr/lib/systemd/systemd-logind"),
    ("sshd-auth", "/usr/lib/openssh/sshd-auth"),
    ("user-runtime-dir@0", "/usr/lib/systemd/systemd-user-runtime-dir"),
    ("root's user manager", "/usr/lib/systemd/systemd"),
    ("the session's shell", "/bin/bash"),
]

# Spans between milestones whose p50 and interval the report gives: the login's chain, and
# logind's beside it.
SPANS = [
    ("systemd's start, to its first unit", "systemd", "first unit's executor"),
    ("first unit to sshd's exec", "first unit's executor", "sshd"),
    ("sshd's exec to the forwarder's exec", "sshd", "socat (the forwarder)"),
    ("the forwarder's exec to the connection", "socat (the forwarder)", "connection accepted"),
    ("the connection to sshd-session's exec", "connection accepted", "sshd-session"),
    ("sshd-session's exec to the login's end", "sshd-session", "login end"),
    ("first unit to logind's exec", "first unit's executor", "systemd-logind"),
    ("logind's exec to root's user manager", "systemd-logind", "root's user manager"),
    ("root's user manager to the session's shell", "root's user manager", "the session's shell"),
]


def milestones(b):
    out = {}
    for name, f in MILESTONES:
        if f.endswith("/"):
            ts = [t for k, t in b.first_exec.items() if k.startswith(f)]
            if ts:
                out[name] = min(ts)
        elif f in b.first_exec:
            out[name] = b.first_exec[f]
    if b.true_exit:
        out["login end"] = b.true_exit[1]
    return out


def p50_ci(xs, n=2000, seed=1689):
    rnd = random.Random(seed)
    meds = sorted(statistics.median(rnd.choices(xs, k=len(xs))) for _ in range(n))
    return statistics.median(xs), meds[int(0.025 * n)], meds[int(0.975 * n) - 1]


def fmt(xs):
    if not xs:
        return "-"
    m, lo, hi = p50_ci(xs)
    return f"{m:6.1f} [{lo:6.1f}, {hi:6.1f}]  mean {statistics.fmean(xs):6.1f}"


def short(name):
    return {
        "the kernel, to PID 1's exec of init": "kernel to PID 1",
        "spin's handoff, to PID 1's exec of systemd": "handoff (lab stand-in)",
        "PID 1, to the login's sshd-session": "systemd to sshd-session exec",
        "sshd-session, to sshd-auth": "sshd-session to sshd-auth",
        "the key exchange and authentication, in sshd-auth": "kex + auth (sshd-auth)",
        "authenticated: PAM's session, logind, the user's manager": "PAM session, logind",
        "the session's process, to the user's shell": "session to shell",
        "the shell, to the login's command": "shell to command",
        "the login's command, to its exit": "command to exit",
    }.get(name, name)


def analyse(artifact, trees):
    boots = collections.defaultdict(list)
    for path in sorted(glob.glob(os.path.join(artifact, "**", "*.ledger.json"), recursive=True)):
        base = os.path.basename(path)[: -len(".ledger.json")]
        tree, rep = base.rsplit("-", 1)
        if trees and tree not in trees:
            continue
        boots[tree].append((int(rep), path))
    for tree in sorted(boots):
        reps = sorted(boots[tree])
        phases = collections.defaultdict(list)
        crit = collections.defaultdict(lambda: collections.defaultdict(list))
        holders = collections.defaultdict(lambda: collections.defaultdict(list))
        detail = collections.defaultdict(lambda: collections.defaultdict(list))
        irqs = collections.defaultdict(list)
        io = collections.defaultdict(list)
        marks = collections.defaultdict(list)
        spans = collections.defaultdict(list)
        order = ["first_login", "before_qemu", "qemu_start", "guest_to_zero"]
        for rep, path in reps:
            led = json.load(open(path))
            phases["first_login"].append(led["first_login"])
            phases["before_qemu"].append(led["qemu_exec"])
            phases["qemu_start"].append(led["running"] - led["qemu_exec"])
            phases["guest_to_zero"].append(led["kernel_zero"] - led["running"])
            for w in led["windows"]:
                name = short(w["name"])
                if name not in order:
                    order.append(name)
                phases[name].append(w["end"] - w["start"])
            if "residue" not in order:
                pass
            phases["residue"].append(led["residue"])
            trace = path[: -len(".ledger.json")] + ".trace.txt"
            b = Boot(trace, led["monotonic_lag"])
            end = b.true_exit[1] if b.true_exit else float("inf")
            ms = milestones(b)
            for k, v in ms.items():
                marks[k].append(v)
            for name, a, z in SPANS:
                if a in ms and z in ms:
                    spans[name].append(ms[z] - ms[a])
            for name in {x[2] for x in b.irqs}:
                irqs[name].append(sum(e - st for st, e, nm in b.irqs if nm == name and e <= end))
            path_segs = b.critical_path(led["windows"][1]["start"])
            conn = next(w["start"] for w in led["windows"] if w["name"].startswith("sshd-session,"))
            if b.blocks:
                reads = [x for x in b.blocks if "R" in x[3] and x[0] < conn]
                io["reads before the connection"].append(len(reads))
                io["MiB read before the connection"].append(sum(x[2] for x in reads) * 512 / 2**20)
                lat = sorted(x[1] - x[0] for x in reads)
                io["read latency p50 ms"].append(lat[len(lat) // 2] if lat else 0)
                io["read latency p90 ms"].append(lat[int(len(lat) * 0.9)] if lat else 0)
                busy, last = 0.0, 0.0
                for i, c, _, _ in sorted(reads):
                    if c > last:
                        busy += c - max(i, last)
                        last = c
                io["ms with a read in flight, before the connection"].append(busy)
                allr = [x for x in b.blocks if "R" in x[3] and x[0] < b.true_exit[1]]
                io["reads to the login's end"].append(len(allr))
            if b.vsock:
                reqs = [x for x in b.vsock if x[1] == "REQUEST" and x[0] < conn - 3]
                io["vsock connection requests refused before the connection"].append(len(reqs))
                cost = 0.0
                for at, _, kpid in reqs:
                    seg = next((sg for sg in b.segs.get(kpid, ()) if sg[2] == "run" and sg[0] <= at <= sg[1]), None)
                    if seg:
                        cost += seg[1] - seg[0]
                vs = [x for x in b.irqs if x[2] == "virtio0-virtqueues" and x[0] < conn]
                cost += sum(e - st for st, e, _ in vs)
                io["ms of guest CPU on those requests (rx work + IRQ)"].append(cost)
            wins = [(short(w["name"]), w["start"], w["end"]) for w in led["windows"]]
            per = collections.defaultdict(collections.Counter)
            per_detail = collections.defaultdict(collections.Counter)
            per_holder = collections.defaultdict(collections.Counter)
            for s, e, cat, who in path_segs:
                for name, ws, we in wins:
                    a, z = max(s, ws), min(e, we)
                    if z <= a:
                        continue
                    per[name][cat] += z - a
                    per_detail[name][(cat, who)] += z - a
                    if cat == "cpu-wait":
                        for h, ms in b.holders(a, z).items():
                            per_holder[name][h] += ms
            for name, _, _ in wins:
                for cat in ("run", "cpu-wait", "disk", "irq-wait", "untraced"):
                    crit[name][cat].append(sum(v for k, v in per[name].items() if k.startswith(cat)))
                for k, v in per_detail[name].items():
                    detail[name][k].append(v)
                for k, v in per_holder[name].items():
                    holders[name][k].append(v)
        order.append("residue")
        n = len(reps)
        print(f"\n## tree {tree}: {n} boots\n")
        print("phase                              p50 [95% CI of p50]        mean")
        for name in order:
            print(f"  {name:32s} {fmt(phases[name])}")
        parts = [k for k in order if k != "first_login"]
        print(f"  {'sum of the phases\' means':32s} {sum(statistics.fmean(phases[k]) for k in parts):6.1f}")
        print("\nmilestones on the kernel's clock, first exec of each: p50 [95% CI of p50]")
        for name, _ in MILESTONES + [("login end", "")]:
            if len(marks[name]) == n:
                print(f"  {name:32s} {fmt(marks[name])}")
        print("\nspans between them")
        for name, _, _ in SPANS:
            if len(spans[name]) == n:
                print(f"  {name:44s} {fmt(spans[name])}")
        print("\nthe login's critical path by window, mean ms over the boots (run / cpu-wait / disk / irq-wait)")
        for name in order:
            if name not in crit:
                continue
            c = crit[name]
            print(f"  {name:32s} run {statistics.fmean(c['run']):6.1f}  cpu-wait {statistics.fmean(c['cpu-wait']):6.1f}"
                  f"  disk {statistics.fmean(c['disk']):6.1f}  irq-wait {statistics.fmean(c['irq-wait']):6.1f}"
                  f"  untraced {statistics.fmean(c['untraced']):4.1f}")
            top = sorted(detail[name].items(), key=lambda kv: -sum(kv[1]) / n)[:8]
            for (cat, who), v in top:
                v = v + [0.0] * (n - len(v))
                print(f"      {cat:9s} {who:40.40s} mean {statistics.fmean(v):6.1f}  p50 {statistics.median(v):6.1f}")
            hs = sorted(holders[name].items(), key=lambda kv: -sum(kv[1]) / n)[:6]
            if hs:
                print("      the vCPU, while the path waited for it: " + ", ".join(
                    f"{h} {sum(v) / n:.1f}" for h, v in hs))
        if io:
            print("\nthe disk and the vsock, per boot: p50 [95% CI of p50]")
            for k, v in io.items():
                print(f"  {k:58s} {fmt(v)}")
        if irqs:
            print("\ndevice interrupts' handlers to the login's end, ms per boot (mean): " + ", ".join(
                f"{k} {sum(v) / n:.2f}" for k, v in sorted(irqs.items(), key=lambda kv: -sum(kv[1]))))


if __name__ == "__main__":
    if len(sys.argv) < 2:
        sys.exit(__doc__)
    analyse(sys.argv[1], set(sys.argv[2:]))
