#!/usr/bin/env bash
# Lint every chart in the repo, found by its Chart.yaml (so a nested chart layout works
# too; this repo currently ships a single chart under charts/).
set -euo pipefail
for chart in $(find charts -name Chart.yaml | sort); do
  helm lint "$(dirname "$chart")"
done
