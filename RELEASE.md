# Releasing

A release is a signed tag on `main`. Everything else follows from it.

1. **Green `main`.** CI on the last commit passed, including the integration job.
2. **Changelog.** Move `[Unreleased]` in `CHANGELOG.md` under the new version with the date, and
   start a new empty `[Unreleased]`. Anything removed from the API is listed with its replacement.
3. **Chart versions.** In `charts/hetzner-bucket-controller/Chart.yaml` set `version` and
   `appVersion` to the release. The image tag defaults to `appVersion`.
4. **Release commit.** One commit, `chore(release): vX.Y.Z`, merged through a pull request like
   any other change.
5. **Tag.** On the merge commit that brings it to `main`, whose tree is what CI tested there, a
   signed annotated tag: `git tag -s vX.Y.Z -m "vX.Y.Z"`, then push the tag.
6. **The workflow does the rest.** `release.yml` refuses a tag the chart is not at, builds the
   multi-arch image with an SBOM and SLSA provenance, signs it with Sigstore keyless signing, pushes
   it to `ghcr.io/maarlab-rethinking/hetzner-bucket-controller` and creates the GitHub Release with
   the verification command in its notes. The chart is consumed from the tag in this repository.
7. **Verify** what it published, with Cosign 3 or later (Cosign 2 cannot read its bundle format):

   ```console
   cosign verify ghcr.io/maarlab-rethinking/hetzner-bucket-controller:X.Y.Z \
     --certificate-identity-regexp 'https://github.com/maarlab-rethinking/hetzner-bucket-controller/' \
     --certificate-oidc-issuer https://token.actions.githubusercontent.com
   ```

## Versioning

Semantic versioning on the controller; the chart's `version` tracks it. While the API is
`v1alpha1`, a minor release may change it, and the changelog is the compatibility contract. Patch
releases are fixes only.
