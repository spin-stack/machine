# SPDX-License-Identifier: Apache-2.0
"""Fail unless meson configured the build with every option it was handed.

Reads `meson introspect --buildoptions` on stdin and the -Dname=value arguments from
argv: an option misspelt, or one the packaging set after ours, would otherwise build the
stage1 packages without it and say nothing.
"""
import json
import sys

opts = {o["name"]: o["value"] for o in json.load(sys.stdin)}
for arg in sys.argv[1:]:
    name, want = arg.removeprefix("-D").split("=", 1)
    got = opts.get(name)
    if str(got).lower() != want.lower():
        sys.exit(f"ERROR: meson has {name}={got}, it was asked for {want}")
    print(f"meson took {name}={got}")
