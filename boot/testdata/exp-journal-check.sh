#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# #514's experiment: docker builds for 10 minutes, and how much of the volatile journal is kept.
set -u
oldest() { journalctl -q -o short-monotonic --no-pager | head -1 | cut -c1-70; }
usage() { journalctl --disk-usage 2>&1 | sed 's/.*take up //'; }
echo "JR MemTotal $(awk '/MemTotal/ {print $2}' /proc/meminfo) kB; /run size,used kB $(df -k --output=size,used /run | tail -1)"
grep -h '^Runtime' /etc/systemd/journald.conf.d/*.conf | sed 's/^/JR conf /'
journalctl -b -u systemd-journald -o cat --no-pager | grep -i 'runtime journal' | sed 's/^/JR journald: /'
echo "JR at start: $(usage); $(journalctl -q --no-pager | wc -l) lines; oldest $(oldest)"
d=$(mktemp -d)
mkdir -p "$d/bin"
cp /bin/dash "$d/bin/sh"
for l in $(ldd /bin/dash | grep -o '/[^ ]*'); do mkdir -p "$d$(dirname "$l")"; cp "$l" "$d$l"; done
tar -C "$d" -c . | docker import - jr-base > /dev/null || { echo "JR import failed"; echo JOURNAL_CHECK_FAILED; exit 0; }
b=$(mktemp -d)
{ echo FROM jr-base; for i in $(seq 20); do echo "RUN echo step $i"; done; } > "$b/Dockerfile"
start=$(date +%s)
end=$((start + 600))
next=$((start + 60))
n=0
while [ "$(date +%s)" -lt "$end" ]; do
    if ! docker build --network none --no-cache -q -t jr-busy "$b" > /dev/null 2>&1; then
        docker build --network none --no-cache "$b" 2>&1 | tail -5 | sed 's/^/JR build: /'
        echo JOURNAL_CHECK_FAILED
        exit 0
    fi
    n=$((n + 1))
    if [ "$(date +%s)" -ge "$next" ]; then
        echo "JR $(( $(date +%s) - start )) s, $n builds: $(usage); $(journalctl -q --no-pager | wc -l) lines; oldest $(oldest)"
        next=$((next + 60))
    fi
done
echo "JR end, $n builds: $(usage); $(journalctl -q --no-pager | wc -l) lines; oldest $(oldest)"
echo "JR bytes exported $(journalctl -q -o export --no-pager | wc -c)"
journalctl -q --no-pager -o json --output-fields=SYSLOG_IDENTIFIER | grep -o '"SYSLOG_IDENTIFIER":"[^"]*"' | sort | uniq -c | sort -rn | head -12 | sed 's/^/JR writer /'
echo JOURNAL_CHECK_OK
