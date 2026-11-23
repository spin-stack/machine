# docs/image.md, "The rebuilt packages"
import json
import sys

log, want = sys.argv[1], sys.argv[2:]
result = {}
with open(log) as f:
    for line in f:
        t = json.loads(line)
        result[t["name"].rsplit(":", 1)[-1]] = t["result"]
bad = [f"{t}: {result.get(t, 'did not run')}" for t in want if result.get(t) != "OK"]
if bad:
    sys.exit("ERROR: systemd's tests did not pass: " + ", ".join(bad))
print(f"OK: {', '.join(want)} passed")
