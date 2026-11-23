---
date: "2026-11-21"
order: 110
area: image
title: 'PID 1''s quiet logging and the printk sysctl deleted (labs 44818953987, 44822157367, #354)'
runs: [44818953987, 44822157367]
---
optimize-systemd.sh set the manager's `LogLevel=err`, `LogTarget=journal`, `ShowStatus=no` and `StatusUnitFormat=name`, and wrote `kernel.printk = 3 3 3 3`, for a quiet, fast boot, never measured. Taken back together (`lab` edit `carry-logging`) against v20261120.04 in spin's shape, the cold first login read +2.4 [−2.3, +7.8] ms at 20 boots and −1.8 [−7.7, +3.2] at 40; accept to login +1.7 [+0.2, +3.2], the second login +1.8 within noise. The console is quiet through the command line's `quiet loglevel=3` either way. The timeouts in fast-boot.conf stay. The `carry-*` edits stay too, as the tool to run the ablation again (docs/boot-time.md). *Status:* deleted.
