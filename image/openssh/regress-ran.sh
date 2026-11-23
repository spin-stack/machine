#!/bin/sh
# docs/image.md, "The rebuilt packages"
set -eu
log=$1
shift
awk -v want="$*" '
	/^run test / { t = $3; sub(/\.sh$/, "", t); ran[t] = 1 }
	/^SKIPPED: / { skipped[t] = substr($0, 10) }
	END {
		for (t in skipped) printf "skipped %s: %s\n", t, skipped[t]
		n = split(want, w, " ")
		for (i = 1; i <= n; i++) {
			t = w[i]
			if (!(t in ran)) { printf "ERROR: regress test %s did not run\n", t; bad = 1 }
			else if (t in skipped) { printf "ERROR: regress test %s was skipped: %s\n", t, skipped[t]; bad = 1 }
		}
		exit bad
	}
' "$log"
echo "OK: $(grep -c "^run test " "$log") regress tests ran, $(grep -c "^SKIPPED: " "$log") skipped; these ran and passed: $*"
