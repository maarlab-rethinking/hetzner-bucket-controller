#!/usr/bin/env bash
# Enforce the repo convention: ONE Kubernetes manifest per file. Fails the commit if a staged
# YAML file has a STATIC extra document separator, i.e. two hand-written resources in one file
# (the mistake this guards against). Pure awk/bash: no chart render, fast in pre-commit.
#
# A `---` is NOT a violation when it is:
#   - a leading document start (nothing before it), or
#   - the first line of a Helm `range`/`if`/`with` body — that is one resource TEMPLATE emitting
#     N instances of the SAME kind (the correct way to loop), not two distinct manifests.
# Any other `---` (a separator sitting between two content blocks) means a second static
# manifest and fails.
set -euo pipefail

fail=0
for f in "$@"; do
  [ -f "$f" ] || continue
  static=$(awk '
    /^---[[:space:]]*$/ {
      if (prev == "") next                                 # leading document start
      if (prev ~ /\{\{-?[[:space:]]*(range|if|with|else)/) { prev=""; next }  # loop/conditional body
      static++; prev=""; next                              # static separator -> extra manifest
    }
    /^[[:space:]]*#/ { next }
    /^[[:space:]]*$/ { next }
    { prev = $0 }
    END { print static + 0 }' "$f")
  if [ "${static:-0}" -gt 0 ]; then
    echo "ERROR: $f has $((static + 1)) manifests — one manifest per file; split each into its own file." >&2
    fail=1
  fi
done

exit "$fail"
