# API Reference

## Packages
- [objectstorage.maarlab.dev/v1alpha1](#objectstoragemaarlabdevv1alpha1)


## objectstorage.maarlab.dev/v1alpha1

Package v1alpha1 contains the API types for the objectstorage.maarlab.dev group.

### Resource Types
- [Bucket](#bucket)
- [BucketList](#bucketlist)



#### Bucket



Bucket is a Hetzner Object Storage bucket reconciled by the controller. Deleting
a Bucket never deletes the bucket in Hetzner (orphan semantics).



_Appears in:_
- [BucketList](#bucketlist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `objectstorage.maarlab.dev/v1alpha1` | | |
| `kind` _string_ | `Bucket` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.37/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[BucketSpec](#bucketspec)_ |  |  |  |


#### BucketList



BucketList is a list of Buckets.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `objectstorage.maarlab.dev/v1alpha1` | | |
| `kind` _string_ | `BucketList` | | |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.37/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[Bucket](#bucket) array_ |  |  |  |


#### BucketSpec



BucketSpec is the desired state of a Hetzner Object Storage bucket.



_Appears in:_
- [Bucket](#bucket)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name of the bucket. Hetzner bucket names are global across every customer and<br />location: lowercase letters, digits and '-', 3 to 63 characters, starting with<br />a letter or digit and not ending with '-'. Immutable. |  | MaxLength: 63 <br />MinLength: 3 <br />Pattern: `^[a-z0-9]([a-z0-9-]*[a-z0-9])?$` <br /> |
| `location` _string_ | Location of the bucket (for example nbg1, fsn1, hel1). It is also the signing<br />region and the CreateBucket LocationConstraint: Hetzner rejects a constraint<br />that differs from the endpoint's location. Immutable. |  | MaxLength: 63 <br />MinLength: 1 <br />Pattern: `^[a-z0-9-]+$` <br /> |
| `endpoint` _string_ | Endpoint of the S3 API. Defaults to https://<location>.your-objectstorage.com.<br />Only needed for a proxy or an S3-compatible test server. |  | Pattern: `^https?://[^/?#]+/?$` <br />Optional: \{\} <br /> |
| `forcePathStyle` _boolean_ | ForcePathStyle addresses the bucket as <endpoint>/<bucket> instead of<br /><bucket>.<endpoint host>. Hetzner requires the default virtual-hosted style;<br />path style exists for S3-compatible test servers. |  | Optional: \{\} <br /> |
| `credentials` _[CredentialsSecretRef](#credentialssecretref)_ | Credentials used for every call; their Hetzner project is the one the bucket<br />is created in. |  |  |
| `objectLock` _[ObjectLockSpec](#objectlockspec)_ | ObjectLock creates the bucket with Object Lock enabled. It can only be chosen<br />at creation: a pre-existing bucket without Object Lock is reported with the<br />ObjectLockMissing condition, never fixed in place. |  | Optional: \{\} <br /> |
| `versioning` _[VersioningStatus](#versioningstatus)_ | Versioning state to enforce. Omitted means not managed. Buckets with Object<br />Lock are always versioned. |  | Enum: [Enabled Suspended] <br />Optional: \{\} <br /> |
| `lifecycle` _[LifecycleSpec](#lifecyclespec)_ | Lifecycle configuration to enforce. Omitted means not managed: whatever is<br />configured on the bucket is left alone. |  | Optional: \{\} <br /> |




#### CredentialsSecretRef



CredentialsSecretRef points at a Secret in the Bucket's namespace holding an S3
key pair of the Hetzner project the bucket lives in. Hetzner has no API to mint
these: they are generated in the Hetzner Console and are full access to the whole
project, which is why the project a bucket lands in is chosen by the credential.



_Appears in:_
- [BucketSpec](#bucketspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name of the Secret, in the same namespace as the Bucket. |  | MinLength: 1 <br /> |
| `accessKeyIDKey` _string_ | Key holding the access key ID. | access-key | Optional: \{\} <br /> |
| `secretAccessKeyKey` _string_ | Key holding the secret access key. | secret-key | Optional: \{\} <br /> |


#### DefaultRetention



DefaultRetention is the retention applied to every new object version that is
written without an explicit one.



_Appears in:_
- [ObjectLockSpec](#objectlockspec)
- [ObservedConfiguration](#observedconfiguration)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `mode` _[RetentionMode](#retentionmode)_ | Mode of the default retention. |  | Enum: [COMPLIANCE GOVERNANCE] <br /> |
| `days` _integer_ | Days each new object version is locked for. |  | Maximum: 36500 <br />Minimum: 1 <br /> |


#### LifecycleRule



LifecycleRule is one bucket lifecycle rule. Only the elements Hetzner documents as
supported are modelled; each rule must set at least one action.



_Appears in:_
- [LifecycleSpec](#lifecyclespec)
- [ObservedConfiguration](#observedconfiguration)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `id` _string_ | ID identifies the rule; unique within the bucket. |  | MaxLength: 255 <br />MinLength: 1 <br /> |
| `prefix` _string_ | Prefix restricts the rule to object keys starting with it. Empty means the<br />whole bucket. |  | Optional: \{\} <br /> |
| `expirationDays` _integer_ | ExpirationDays expires the current version of an object this many days after<br />it was written. On a versioned bucket this adds a delete marker and turns the<br />object into a noncurrent version; it does not delete data. |  | Minimum: 1 <br />Optional: \{\} <br /> |
| `expiredObjectDeleteMarker` _boolean_ | ExpiredObjectDeleteMarker removes delete markers that no longer have any<br />noncurrent version behind them. |  | Optional: \{\} <br /> |
| `noncurrentVersionExpirationDays` _integer_ | NoncurrentVersionExpirationDays permanently deletes noncurrent versions this<br />many days after they became noncurrent. Versions still under Object Lock<br />retention are kept until it lapses. |  | Minimum: 1 <br />Optional: \{\} <br /> |
| `abortIncompleteMultipartUploadDays` _integer_ | AbortIncompleteMultipartUploadDays deletes the parts of multipart uploads that<br />were not completed this many days after they started. |  | Minimum: 1 <br />Optional: \{\} <br /> |


#### LifecycleSpec



LifecycleSpec is the bucket's lifecycle configuration, managed as a whole: rules
present on the bucket but absent here are removed.



_Appears in:_
- [BucketSpec](#bucketspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `rules` _[LifecycleRule](#lifecyclerule) array_ | Rules of the lifecycle configuration. An empty list removes any lifecycle<br />configuration from the bucket. |  | MaxItems: 100 <br />Optional: \{\} <br /> |


#### ObjectLockSpec



ObjectLockSpec requests Object Lock on the bucket. Object Lock can only be enabled
when the bucket is created and forces versioning on, permanently.



_Appears in:_
- [BucketSpec](#bucketspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `defaultRetention` _[DefaultRetention](#defaultretention)_ | DefaultRetention for new object versions. When omitted the controller keeps<br />the bucket without a default retention (and removes one set out of band);<br />objects are then locked only if the writer asks for it. |  | Optional: \{\} <br /> |


#### ObservedConfiguration



ObservedConfiguration is the bucket configuration read from Hetzner on the last
successful reconcile, after any correction was applied.



_Appears in:_
- [BucketStatus](#bucketstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `endpoint` _string_ | Endpoint the controller talks to. |  | Optional: \{\} <br /> |
| `objectLockEnabled` _boolean_ | ObjectLockEnabled reports whether the bucket has Object Lock. |  | Optional: \{\} <br /> |
| `defaultRetention` _[DefaultRetention](#defaultretention)_ | DefaultRetention configured on the bucket, if any. |  | Optional: \{\} <br /> |
| `versioning` _string_ | Versioning status of the bucket; empty if it was never enabled. |  | Optional: \{\} <br /> |
| `lifecycleRules` _[LifecycleRule](#lifecyclerule) array_ | LifecycleRules configured on the bucket. Only reported when spec.lifecycle is<br />managed. |  | Optional: \{\} <br /> |


#### RetentionMode

_Underlying type:_ _string_

RetentionMode is an S3 Object Lock retention mode.

_Validation:_
- Enum: [COMPLIANCE GOVERNANCE]

_Appears in:_
- [DefaultRetention](#defaultretention)

| Field | Description |
| --- | --- |
| `COMPLIANCE` | RetentionCompliance cannot be shortened or lifted by anyone, the Hetzner<br />account owner included, until it lapses.<br /> |
| `GOVERNANCE` | RetentionGovernance can be bypassed by holders of the bypass permission. On<br />Hetzner every S3 credential is project-wide, so it protects against mistakes,<br />not against a compromised credential.<br /> |


#### VersioningStatus

_Underlying type:_ _string_

VersioningStatus is the S3 bucket versioning state.

_Validation:_
- Enum: [Enabled Suspended]

_Appears in:_
- [BucketSpec](#bucketspec)

| Field | Description |
| --- | --- |
| `Enabled` |  |
| `Suspended` |  |


