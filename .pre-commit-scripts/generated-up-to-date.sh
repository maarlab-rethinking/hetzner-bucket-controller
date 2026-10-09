#!/usr/bin/env bash
# Fail when the deepcopy code, the CRD or the ClusterRole are stale with respect to
# api/ and the +kubebuilder markers in internal/. Regenerates in place, so a failing
# run leaves the fresh files in the working tree, ready to stage.
set -euo pipefail
make --no-print-directory generate manifests >/dev/null
generated=(
  api/v1alpha1/zz_generated.deepcopy.go
  config/crd
  charts/hetzner-bucket-controller/templates/crd.yaml
  charts/hetzner-bucket-controller/templates/clusterrole.yaml
)
if ! git diff --quiet -- "${generated[@]}"; then
  echo "Generated files were stale and have been regenerated; review and stage them:" >&2
  git diff --stat -- "${generated[@]}" >&2
  exit 1
fi
