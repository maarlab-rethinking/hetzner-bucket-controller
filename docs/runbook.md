# Runbook

Symptom first, then what the controller knows, then what to do. `kubectl describe bucket <name>`
shows the conditions and Events this refers to; the controller's log is in the namespace it was
installed in.

## Conditions and reasons

The `Ready` reason is the one `kubectl get buckets` shows. The other conditions carry the same
reason when they are the cause.

| Condition | Reason | Meaning | Action |
|---|---|---|---|
| `Ready=True` | `Available` | the bucket exists and matches the spec | none |
| `Ready=False` | `Conflict` | another `Bucket` object, older, manages this `spec.name`; this one touches nothing | delete one of the two. If this one should win, delete the other; it takes over at once |
| | `SecretNotFound` | the credentials Secret does not exist in the `Bucket`'s namespace | check the `ExternalSecret` that should write it (`kubectl get externalsecret`), and the name in `spec.credentials` |
| | `SecretForbidden` | the controller has no Role in this namespace | add the namespace to `rbac.secretNamespaces` and upgrade the release |
| | `SecretKeyMissing` | the Secret lacks `access-key` or `secret-key` (or the keys named in `spec.credentials`), or one is empty | fix the `ExternalSecret`'s `data`, or the key names in `spec.credentials` |
| | `CredentialsRejected` | Hetzner refused the key pair: `InvalidAccessKeyId`, `SignatureDoesNotMatch` or `AccessDenied` | the key was deleted or mistyped. Create one in the Hetzner Console for the right project and write it to OpenBao. Retried only at the resync; force a pass after fixing it |
| | `NameTaken` | the name belongs to another Hetzner project or customer | pick another `spec.name` (a new `Bucket`, since the name is immutable). If it is yours in another project, the credential points at the wrong project |
| | `ObjectLockMissing` | the bucket exists without Object Lock and the spec requests it; lifecycle and versioning were still applied | Object Lock cannot be added. Create a new `Bucket` with another name, move the producers to it, then retire this one ([operations](operations.md#retiring-a-bucket)) |
| | `ProviderError` | an S3 call failed unexpectedly; the message carries the operation and Hetzner's error | retried with backoff. Transient errors clear on their own; one that persists is in the table below |
| any | `NotChecked` | the condition was not evaluated because an earlier step stopped the pass | see `Ready` |

## Events

| Type | Reason | Meaning |
|---|---|---|
| Normal | `Created` | the bucket was created, with or without Object Lock as the message says |
| Normal | `Adopted` | the bucket already existed in the credential's project and is now managed |
| Normal | `Configured` | retention, versioning or lifecycle were written after a spec change; the message lists each change |
| Normal | `DriftCorrected` | the same, with no spec change: someone changed the bucket outside the cluster. Find out who |
| Warning | `CredentialsInvalid` | entered `SecretNotFound`, `SecretForbidden`, `SecretKeyMissing` or `CredentialsRejected` |
| Warning | `NameTaken` | entered `NameTaken` |
| Warning | `ObjectLockMissing` | entered `ObjectLockMissing` |
| Warning | `Conflict` | entered `Conflict` |

Warnings are emitted once per transition, not on every retry. A `Created` Event on a `Bucket` that
was already `Ready` means the bucket was deleted outside the cluster and recreated empty: whatever it
held is gone, and whoever deleted it needed the project's credential.

## Provider errors worth recognising

| Message contains | Cause and action |
|---|---|
| `LocationConstraintConflict` | the endpoint's location and `spec.location` differ. Fix `spec.endpoint`, or recreate the `Bucket` with the right location |
| `InvalidBucketState` on `PutBucketVersioning` | `versioning: Suspended` on a bucket that has Object Lock. Remove `spec.versioning` or set it to `Enabled` |
| a 400 on `PutBucketLifecycleConfiguration` or `PutObjectLockConfiguration` mentioning a checksum | Hetzner rejecting the CRC32 checksum the SDK sends on these calls; see [Compatibility](operations.md#compatibility). The fix belongs in the controller's S3 client |
| `context deadline exceeded` | Hetzner did not answer within `--operation-timeout` (2 minutes) or a request within 30 seconds. Check Hetzner's status page; persists only if the endpoint is unreachable from the cluster |
| `no such host` | the endpoint does not resolve: a typo in `spec.location` or `spec.endpoint`, or no egress DNS from the controller's pod |
| `Server rejected event` (controller log) | RBAC for `events.k8s.io` missing; the chart grants it |

## Forcing a reconcile

The controller reconciles a `Bucket` on spec changes and every `--resync-period`; annotations and
labels do not wake it. To check every `Bucket` now, restart the controller, which reconciles them all
on start:

```console
kubectl -n object-storage rollout restart deployment hetzner-bucket-controller
```
