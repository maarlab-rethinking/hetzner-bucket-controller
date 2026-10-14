# Architecture

How a `Bucket` becomes a configured bucket in Hetzner Object Storage, in the order the controller
does it and with the reasons for that order. The [decision records](decisions/) hold the
alternatives that were considered and why they lost.

## The shape

```
Bucket/management-backups            namespace backup-buckets
  ├─ spec.credentials ─► Secret/object-storage-management-backups   (written by External Secrets)
  └─ reconciles ─► bucket maarlab-management-nbg1-backups           (Hetzner, nbg1)
```

There is nothing else. The controller creates no Kubernetes objects, sets no owner references and
adds no finalizer: the only things it writes are the bucket in Hetzner and the `Bucket`'s status and
Events. Deleting a `Bucket` leaves the bucket where it is
([ADR-0004](decisions/0004-orphan-on-delete-without-finalizer.md)).

The controller talks to two APIs. Kubernetes, through a cached client for `Bucket` objects and an
uncached reader for Secrets ([ADR-0005](decisions/0005-credentials-read-per-namespace.md)). Hetzner,
through `aws-sdk-go-v2` against `https://<location>.your-objectstorage.com`, with virtual-hosted
addressing, the location as signing region and checksums only where S3 requires them
([ADR-0002](decisions/0002-model-only-what-hetzner-documents.md)).

## One reconcile, in order

A pass runs when the spec changes (status writes do not bump the generation, so the controller does
not wake itself up), when a `Bucket` with the same `spec.name` is deleted, and every
`--resync-period` (10 minutes by default, with 10% jitter) even when nothing changed in Kubernetes.
The resync is what catches changes made in Hetzner. All S3 calls of one pass share a
`--operation-timeout` (2 minutes), and each request has its own 30-second limit.

1. **Deletion**: nothing to do. A `Bucket` with a deletion timestamp is skipped and goes away at
   once; the bucket stays.
2. **One owner per name.** Bucket names are global, so two `Bucket` objects with the same
   `spec.name` would fight over one bucket's configuration. An index on `spec.name` finds every
   claimant; the oldest by creation time owns the name (ties broken by namespace/name) and the
   others stop with `Ready=False`, reason `Conflict`, touching nothing. They retry every
   `--wait-period` and are woken at once when the owner is deleted.
3. **Credentials.** The Secret named in `spec.credentials` is read from the API server, uncached, in
   the `Bucket`'s namespace. Missing, forbidden by RBAC or lacking a key is
   `CredentialsInvalid=True` and a retry every `--wait-period`: the usual cause is External Secrets
   not having written it yet.
4. **Existence**, with `ListBuckets` on the credential's project
   ([ADR-0003](decisions/0003-existence-from-listbuckets.md)). A rejected key pair is
   `CredentialsRejected`, retried at the resync rather than quickly: only a new key fixes it. Success
   sets `CredentialsInvalid=False`.
5. **Creation** when the bucket is not in the list — the first time, or again after someone deleted
   it out of band. `CreateBucket` carries `LocationConstraint = spec.location` and, when
   `spec.objectLock` is set, the Object Lock header, since that is the only moment it can be enabled.
   `BucketAlreadyOwnedByYou` is adoption; `BucketAlreadyExists` is `NameTaken=True`, checked again
   at the resync and never retried with backoff.
6. **Object Lock.** Read, never added: a bucket that exists without the lock the spec requests is
   `ObjectLockMissing=True` and `Ready=False`, but the steps below still run, so the lifecycle keeps
   expiring data. With the lock present, the default retention is compared with
   `spec.objectLock.defaultRetention` and rewritten only if it differs; omitting it clears the
   bucket's default. A retention Hetzner reports in years is drift against a spec in days.
7. **Versioning**, only when `spec.versioning` is set: rewritten if it differs.
8. **Lifecycle**, only when `spec.lifecycle` is set, and then as a whole: the rules on the bucket are
   compared with the spec ignoring order, and on any difference the whole configuration is replaced,
   or deleted when the spec lists no rules. A rule using an element outside the model is a
   difference by construction.
9. **Status.** `status.observed` gets what was read, after corrections; the conditions get their
   final values; `Ready=True` with reason `Available` unless Object Lock is missing. The status is
   patched only if it changed.

Every write in steps 6 to 8 is reported in one Event listing the changes. Its reason is `Configured`
when the spec moved, and `DriftCorrected` when the `Bucket` was already `Ready` at the same
generation, which means someone changed the bucket outside the cluster.

A pass that changes nothing makes only reads: `ListBuckets`, `GetObjectLockConfiguration`,
`GetBucketVersioning` and, when managed, `GetBucketLifecycleConfiguration`.

## Conditions and requeues

Every condition is present after the first pass, so `kubectl wait --for=condition=X=false` works as
well as `=true`. A condition that could not be evaluated because an earlier step stopped the pass is
`Unknown` with reason `NotChecked`.

| Outcome | Requeue | Why |
|---|---|---|
| `Available`, `ObjectLockMissing` | resync | look for drift and out-of-band deletion |
| `Conflict`, `SecretNotFound`, `SecretForbidden`, `SecretKeyMissing` | `--wait-period` | waiting on something inside the cluster |
| `CredentialsRejected`, `NameTaken` | resync | waiting on someone outside the cluster |
| `ProviderError` | exponential backoff | unexpected; the error is returned to controller-runtime |

Warning Events are emitted once per transition into a failure reason, not on every retry.

## Failure philosophy

The controller never guesses on the user's behalf. It does not pick another name when one is taken,
does not create a replacement bucket when Object Lock is missing, and does not delete anything. Each
of those is a condition with a message that says what to do. What it does without asking is make the
bucket's configuration match the spec again, every time it drifts, and recreate a bucket that has
disappeared.

## What the tests cover, and cannot

- **Unit tests** run the reconciler against a fake S3 server (`internal/objectstorage/fakes3`) and a
  fake Kubernetes client: every path above, including drift, out-of-band deletion, adoption, name
  conflicts and every credential failure.
- **envtest** runs a real kube-apiserver and etcd: the CEL validation of the CRD and the controller
  inside a manager.
- **Integration** (`make test-integration`) runs the S3 client and the whole reconciler against
  MinIO with Object Lock, so the real SDK, signing, checksums and XML go over the wire.
- **None of them is Hetzner.** MinIO is more permissive than Hetzner in places (it implements the
  sub-resources Hetzner lacks and accepts the checksums Hetzner has not been tested with). The first
  run against Hetzner is the test the suite cannot replace; see
  [Compatibility](operations.md#compatibility).
