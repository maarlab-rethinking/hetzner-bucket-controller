# ADR-0003: Decide existence with ListBuckets, not HeadBucket

**Status:** accepted, 2026-10-09

## Context

Every reconcile has to answer three questions before it touches a bucket: does the key pair work,
does the bucket exist, and if it exists, is it ours. `HeadBucket` is the usual existence check and
the one the `ensure-bucket` Jobs used, but a `HEAD` response has no body: a 403 carries no error
code, so it cannot tell "bad credentials" from "the name belongs to another customer". Bucket names
are global across every Hetzner customer ([ADR-0002](0002-model-only-what-hetzner-documents.md)), so
the second case is not hypothetical.

## Decision

Existence is decided with `ListBuckets` on the credential's project. One call answers all three
questions:

- It fails with `InvalidAccessKeyId`, `SignatureDoesNotMatch` (or `AccessDenied`, which a valid
  Hetzner key never gets, since credentials are project-wide) when the key pair is wrong. That is
  `CredentialsInvalid` with reason `CredentialsRejected`.
- The bucket is in the list: it exists and the project owns it.
- The bucket is not in the list: `CreateBucket` decides the rest. `BucketAlreadyOwnedByYou` (a race)
  is adoption; `BucketAlreadyExists` means someone else owns the name, which is `NameTaken`.

`NameTaken` is not retried with backoff, since retrying cannot help; it is checked again at the
next resync, in case the owner deletes it.

## Consequences

- The cost per reconcile grows with the number of buckets in the project, one page per thousand.
  Backup projects hold a handful.
- Adoption of an existing bucket is explicit: an `Adopted` event the first time the controller finds
  it, whether through the list or through `BucketAlreadyOwnedByYou`.
