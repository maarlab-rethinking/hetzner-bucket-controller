# Changelog

All notable changes are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.1.0] - 2026-10-18

### Added
- `Bucket`: a Hetzner Object Storage bucket reconciled by the controller, with Object Lock chosen
  at creation and its default retention, versioning and lifecycle rules enforced. Deleting a
  `Bucket` never deletes the bucket in Hetzner.

[Unreleased]: https://github.com/maarlab-rethinking/hetzner-bucket-controller/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/maarlab-rethinking/hetzner-bucket-controller/releases/tag/v0.1.0
