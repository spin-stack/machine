# Where the cold first login's time goes (spin-stack/spin#1689)

A lab branch, not for main. It measures spin's shape of 2026-11-26 (spin's pin v20261124.01,
the supervisor without default dependencies, 2048 MiB base and nothing plugged) on the bench
fleet, and accounts for every millisecond of the cold first login.

## The runs

```sh
R=v20261124.01
APPEND='trace_event=sched:sched_switch,sched:sched_wakeup,sched:sched_wakeup_new,sched:sched_process_fork,sched:sched_process_exec,sched:sched_process_exit trace_clock=mono trace_options=nooverwrite trace_buf_size=2M'
APPEND_IRQ='trace_event=sched:sched_switch,sched:sched_wakeup,sched:sched_wakeup_new,sched:sched_process_fork,sched:sched_process_exec,sched:sched_process_exit,irq:irq_handler_entry,irq:irq_handler_exit,block:block_rq_issue,block:block_rq_complete,vsock:virtio_transport_recv_pkt trace_clock=mono trace_options=nooverwrite trace_buf_size=4M'
SPIN="--from release:$R --spin-machine release --edit forwarder-early --opt memory=2048 --opt plugged=0"
TRACED="$SPIN --kernel traceable:$R --edit ledger --append '$APPEND'"
TRACED_IRQ="$SPIN --kernel traceable:$R --edit ledger --append '$APPEND_IRQ'"
OLD="--from release:$R --spin-machine release"
REF=lab/1689-cold-login-ledger

# The number, untraced, beside what the instrument costs, 2 vCPUs, and the record's shape.
spin-machine-dev lab dispatch ssh-first --ref $REF --reps 30 --tree "spin=$SPIN" \
  --tree "traced=$TRACED" --tree "tracedirq=$TRACED_IRQ" --tree "cpus2=$SPIN --opt cpus=2" --tree "old=$OLD"

# The ledgers: 30 boots a tree.
spin-machine-dev lab dispatch ledger --ref $REF --reps 30 --tree "spin=$TRACED" \
  --tree "irq=$TRACED_IRQ" --tree "cpus2=$TRACED --opt cpus=2"

# The kernel's initcalls (no forwarder: the probe does not log in).
spin-machine-dev lab dispatch initcalls --ref $REF --reps 20 \
  --tree "spin=--from release:$R --spin-machine release --opt memory=2048 --opt plugged=0"
```

## Reading the ledgers

```sh
spin-machine-dev lab show RUN_ID          # downloads the artifact under _output/lab/runs/RUN_ID
python3 -I lab-1689/ledgerpath.py _output/lab/runs/RUN_ID/result spin
```

`ledgerpath.py` prints the ledger's windows with the p50 and bootstrap interval of each over the
boots, the milestones of the boot on the kernel's clock, the login's critical path by window, and,
on the `irq` tree, the disk's reads and the host's refused vsock connections.
