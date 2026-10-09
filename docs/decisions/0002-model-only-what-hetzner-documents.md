# ADR-0002: Model only what Hetzner documents

**Status:** accepted, 2026-10-09

## Context

Hetzner Object Storage speaks the S3 API, but a reduced one. What it supports was checked against
the official documentation (docs.hetzner.com, Object Storage section: "List of supported actions",
FAQ "Buckets & objects", how-tos for Object Lock retention, versioning and lifecycle, "Using
libraries", troubleshooting "HTTP 400"):

| Need | Hetzner | Notes |
|---|---|---|
| `CreateBucket` with Object Lock | Supported | Only the `x-amz-bucket-object-lock-enabled` and `x-amz-acl` headers are accepted; the body accepts only `LocationConstraint`. |
| `LocationConstraint` = location | Required | Signing region and constraint must be the endpoint's location (`nbg1` for `nbg1.your-objectstorage.com`), otherwise `LocationConstraintConflict`. |
| Enable Object Lock later | **Not possible** | "It is not possible to enable Object Lock on Buckets that were created without Object Lock." |
| Default retention (`Get`/`PutObjectLockConfiguration`) | Supported | `GOVERNANCE` or `COMPLIANCE`, `Days` or `Years`. Clearing it is a PUT with only `ObjectLockEnabled=Enabled`. |
| `COMPLIANCE` | Immutable | "No one can end the retention period earlier", including the account owner. |
| Versioning | Supported | Forced on (cannot be suspended) for Object Lock buckets. |
| Lifecycle (`Get`/`Put`/`DeleteBucketLifecycle`) | Supported, reduced | Documented elements: `Expiration.Days`, `Expiration.ExpiredObjectDeleteMarker`, `NoncurrentVersionExpiration` (**only** `NoncurrentDays`), `AbortIncompleteMultipartUpload.DaysAfterInitiation`, prefix filter. Transitions, tag filters and `NewerNoncurrentVersions` are not documented. |
| Bucket tagging, replication, logging, metrics, inventory, analytics, notifications, request payment, website, accelerate, ownership controls, intelligent tiering | **Not supported** | Explicitly listed as unsupported bucket features. |
| Bucket names | Global | Unique across all Hetzner customers and locations; lowercase letters, digits and `-`, 3–63 characters, no dots, immutable. A deleted name is free again for the same owner immediately, for everyone else after 14 days. |
| Addressing | Virtual-hosted | "If the host header is any domain other than the Bucket domain, the request will fail"; the SDK examples use virtual-hosted addressing. |

Nothing the controller needs is missing. An API that exposed the full S3 lifecycle model, or any of
the unsupported features, would accept specs Hetzner then rejects or silently ignores.

## Decision

The CRD models exactly the documented subset: Object Lock chosen at creation, a default retention in
days, versioning, and lifecycle rules with prefix, expiration days, expired-delete-marker cleanup,
noncurrent version expiration and abort of incomplete multipart uploads. The Hetzner constraints
become validation the API server enforces with CEL:

- `spec.name` and `spec.location` are immutable: changing either means a different bucket.
- The presence of `spec.objectLock` is immutable: it cannot be added to or removed from an existing
  bucket.
- A bucket with Object Lock cannot ask for `versioning: Suspended`.
- `spec.endpoint` and `spec.forcePathStyle` only describe how to reach the location and stay
  mutable.

Object Lock is never faked. A bucket that already exists without it is reported with
`ObjectLockMissing` and `Ready=False`, never "fixed": the only fix is a new bucket, and choosing its
name is the user's decision.

A lifecycle rule found on the bucket that uses an element outside the model (a transition, a tag or
size filter, a date) is replaced, because the controller cannot compare it and would otherwise
report it as matching.

## Consequences

- New lifecycle features have to be added to the CRD explicitly; that is intended, since each needs
  checking against Hetzner first.
- The AWS SDK for Go v2 sends CRC32 integrity checksums by default since 2025, which several
  S3-compatible services reject. The client sets `RequestChecksumCalculation` and
  `ResponseChecksumValidation` to `WhenRequired`, the setting recommended for third-party endpoints.
  Calls S3 marks as "checksum required" (`PutBucketLifecycleConfiguration`,
  `PutObjectLockConfiguration`) still carry one. MinIO accepts them; **Hetzner has not been tested
  yet**, and the first run against it must confirm it. See [Compatibility](../operations.md#compatibility).
