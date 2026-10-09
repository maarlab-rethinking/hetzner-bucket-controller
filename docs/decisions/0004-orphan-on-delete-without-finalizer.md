# ADR-0004: Deleting a Bucket orphans the bucket, with no finalizer

**Status:** accepted, 2026-10-09

## Context

Kubernetes controllers that manage external resources usually delete them when their object is
deleted, through a finalizer. These buckets hold backups. A backup has to outlive any Kubernetes
object, including the cluster it came from: an accidental `kubectl delete`, a namespace removed by
Argo CD pruning, or a management cluster recreated from scratch must never take the backups with
them. With Object Lock, a deletion would also fail on any object still under retention.

## Decision

Deleting a `Bucket` only stops the reconciliation; the bucket and its data stay in Hetzner. Since
nothing has to happen on deletion, the controller adds **no finalizer**: one would only add a way
for a `Bucket` to get stuck in `Terminating` while the controller is down, with no behaviour behind
it. The ClusterRole has no `update` on `buckets` nor on `buckets/finalizers` for that reason.

Creating the `Bucket` again, in any namespace, adopts the existing bucket
([ADR-0003](0003-existence-from-listbuckets.md)).

## Consequences

- Retiring a bucket is a manual operation after deleting the `Bucket` object: empty it, then delete
  it in the Console or with an S3 client. The procedure is in
  [Operations](../operations.md#retiring-a-bucket).
- A bucket name stays taken by the project until someone deletes it by hand, so a later `Bucket`
  with the same name in the same project adopts it, data included.
