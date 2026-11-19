# SPDX-License-Identifier: Apache-2.0
"""Fail unless meson configured the build with every option a variant asked for.

Reads `meson introspect --buildoptions` on stdin and the variant's -Dname=value arguments from
argv: an option misspelt, or one the packaging set after the variant's, would otherwise build the
stage1 packages again under the variant's name.
"""
import json
import sys

opts = {o["name"]: o["value"] for o in json.load(sys.stdin)}
for arg in sys.argv[1:]:
    name, want = arg.removeprefix("-D").split("=", 1)
    got = opts.get(name)
    if str(got).lower() != want.lower():
        sys.exit(f"ERROR: meson has {name}={got}, the variant asked for {want}")
    print(f"meson took {name}={got}")
