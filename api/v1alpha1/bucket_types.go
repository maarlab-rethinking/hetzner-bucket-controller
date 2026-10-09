/*
Copyright 2026 Maarlab Rethinking.
Licensed under the Apache License, Version 2.0 (the "License").
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Condition types reported in status.conditions. Every one of them is always present
// once the controller has reconciled the object, so `kubectl wait --for=condition=X=false`
// works as well as `=true`.
const (
	// ConditionReady is True when the bucket exists, is owned by the credential's
	// project and matches the spec.
	ConditionReady = "Ready"
	// ConditionCredentialsInvalid is True when the credentials Secret is missing,
	// unreadable (the controller has no RBAC in that namespace), lacks a key, or
	// Hetzner rejects the key pair.
	ConditionCredentialsInvalid = "CredentialsInvalid"
	// ConditionNameTaken is True when the bucket name belongs to someone else.
	// Bucket names are global across every Hetzner customer and location.
	ConditionNameTaken = "NameTaken"
	// ConditionObjectLockMissing is True when spec.objectLock is set but the bucket
	// exists without Object Lock. It can only be enabled at creation, so the only
	// fix is a new bucket.
	ConditionObjectLockMissing = "ObjectLockMissing"
)

// Reasons used by the Ready condition (and, where they apply, by the others).
const (
	ReasonAvailable          = "Available"
	ReasonSecretNotFound     = "SecretNotFound"
	ReasonSecretForbidden    = "SecretForbidden"
	ReasonSecretKeyMissing   = "SecretKeyMissing"
	ReasonCredentialsInvalid = "CredentialsRejected"
	ReasonCredentialsValid   = "CredentialsAccepted"
	ReasonNameTaken          = "NameTaken"
	ReasonNameAvailable      = "NameOwned"
	ReasonObjectLockMissing  = "ObjectLockMissing"
	ReasonObjectLockOK       = "ObjectLockAsRequested"
	ReasonConflict           = "Conflict"
	ReasonProviderError      = "ProviderError"
	ReasonUnknown            = "NotChecked"
)

// RetentionMode is an S3 Object Lock retention mode.
// +kubebuilder:validation:Enum=COMPLIANCE;GOVERNANCE
type RetentionMode string

const (
	// RetentionCompliance cannot be shortened or lifted by anyone, the Hetzner
	// account owner included, until it lapses.
	RetentionCompliance RetentionMode = "COMPLIANCE"
	// RetentionGovernance can be bypassed by holders of the bypass permission. On
	// Hetzner every S3 credential is project-wide, so it protects against mistakes,
	// not against a compromised credential.
	RetentionGovernance RetentionMode = "GOVERNANCE"
)

// VersioningStatus is the S3 bucket versioning state.
// +kubebuilder:validation:Enum=Enabled;Suspended
type VersioningStatus string

const (
	VersioningEnabled   VersioningStatus = "Enabled"
	VersioningSuspended VersioningStatus = "Suspended"
)

// CredentialsSecretRef points at a Secret in the Bucket's namespace holding an S3
// key pair of the Hetzner project the bucket lives in. Hetzner has no API to mint
// these: they are generated in the Hetzner Console and are full access to the whole
// project, which is why the project a bucket lands in is chosen by the credential.
type CredentialsSecretRef struct {
	// Name of the Secret, in the same namespace as the Bucket.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Key holding the access key ID.
	// +kubebuilder:default=access-key
	// +optional
	AccessKeyIDKey string `json:"accessKeyIDKey,omitempty"`

	// Key holding the secret access key.
	// +kubebuilder:default=secret-key
	// +optional
	SecretAccessKeyKey string `json:"secretAccessKeyKey,omitempty"`
}

// DefaultRetention is the retention applied to every new object version that is
// written without an explicit one.
type DefaultRetention struct {
	// Mode of the default retention.
	Mode RetentionMode `json:"mode"`

	// Days each new object version is locked for.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=36500
	Days int32 `json:"days"`
}

// ObjectLockSpec requests Object Lock on the bucket. Object Lock can only be enabled
// when the bucket is created and forces versioning on, permanently.
type ObjectLockSpec struct {
	// DefaultRetention for new object versions. When omitted the controller keeps
	// the bucket without a default retention (and removes one set out of band);
	// objects are then locked only if the writer asks for it.
	// +optional
	DefaultRetention *DefaultRetention `json:"defaultRetention,omitempty"`
}

// LifecycleRule is one bucket lifecycle rule. Only the elements Hetzner documents as
// supported are modelled; each rule must set at least one action.
//
// +kubebuilder:validation:XValidation:rule="has(self.expirationDays) || (has(self.expiredObjectDeleteMarker) && self.expiredObjectDeleteMarker) || has(self.noncurrentVersionExpirationDays) || has(self.abortIncompleteMultipartUploadDays)",message="a lifecycle rule needs at least one action"
// +kubebuilder:validation:XValidation:rule="!(has(self.expirationDays) && has(self.expiredObjectDeleteMarker) && self.expiredObjectDeleteMarker)",message="expirationDays and expiredObjectDeleteMarker cannot be combined in one rule (S3 restriction); use two rules"
type LifecycleRule struct {
	// ID identifies the rule; unique within the bucket.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=255
	ID string `json:"id"`

	// Prefix restricts the rule to object keys starting with it. Empty means the
	// whole bucket.
	// +optional
	Prefix string `json:"prefix,omitempty"`

	// ExpirationDays expires the current version of an object this many days after
	// it was written. On a versioned bucket this adds a delete marker and turns the
	// object into a noncurrent version; it does not delete data.
	// +kubebuilder:validation:Minimum=1
	// +optional
	ExpirationDays int32 `json:"expirationDays,omitempty"`

	// ExpiredObjectDeleteMarker removes delete markers that no longer have any
	// noncurrent version behind them.
	// +optional
	ExpiredObjectDeleteMarker bool `json:"expiredObjectDeleteMarker,omitempty"`

	// NoncurrentVersionExpirationDays permanently deletes noncurrent versions this
	// many days after they became noncurrent. Versions still under Object Lock
	// retention are kept until it lapses.
	// +kubebuilder:validation:Minimum=1
	// +optional
	NoncurrentVersionExpirationDays int32 `json:"noncurrentVersionExpirationDays,omitempty"`

	// AbortIncompleteMultipartUploadDays deletes the parts of multipart uploads that
	// were not completed this many days after they started.
	// +kubebuilder:validation:Minimum=1
	// +optional
	AbortIncompleteMultipartUploadDays int32 `json:"abortIncompleteMultipartUploadDays,omitempty"`
}

// LifecycleSpec is the bucket's lifecycle configuration, managed as a whole: rules
// present on the bucket but absent here are removed.
type LifecycleSpec struct {
	// Rules of the lifecycle configuration. An empty list removes any lifecycle
	// configuration from the bucket.
	// +listType=map
	// +listMapKey=id
	// +kubebuilder:validation:MaxItems=100
	// +optional
	Rules []LifecycleRule `json:"rules,omitempty"`
}

// BucketSpec is the desired state of a Hetzner Object Storage bucket.
//
// +kubebuilder:validation:XValidation:rule="has(self.objectLock) == has(oldSelf.objectLock)",message="objectLock can only be chosen at creation: Object Lock cannot be added to or removed from an existing bucket"
// +kubebuilder:validation:XValidation:rule="!has(self.objectLock) || !has(self.versioning) || self.versioning == 'Enabled'",message="a bucket with Object Lock always has versioning enabled"
type BucketSpec struct {
	// Name of the bucket. Hetzner bucket names are global across every customer and
	// location: lowercase letters, digits and '-', 3 to 63 characters, starting with
	// a letter or digit and not ending with '-'. Immutable.
	// +kubebuilder:validation:MinLength=3
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="name is immutable"
	Name string `json:"name"`

	// Location of the bucket (for example nbg1, fsn1, hel1). It is also the signing
	// region and the CreateBucket LocationConstraint: Hetzner rejects a constraint
	// that differs from the endpoint's location. Immutable.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9-]+$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="location is immutable"
	Location string `json:"location"`

	// Endpoint of the S3 API. Defaults to https://<location>.your-objectstorage.com.
	// Only needed for a proxy or an S3-compatible test server.
	// +kubebuilder:validation:Pattern=`^https?://[^/?#]+/?$`
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// ForcePathStyle addresses the bucket as <endpoint>/<bucket> instead of
	// <bucket>.<endpoint host>. Hetzner requires the default virtual-hosted style;
	// path style exists for S3-compatible test servers.
	// +optional
	ForcePathStyle bool `json:"forcePathStyle,omitempty"`

	// Credentials used for every call; their Hetzner project is the one the bucket
	// is created in.
	Credentials CredentialsSecretRef `json:"credentials"`

	// ObjectLock creates the bucket with Object Lock enabled. It can only be chosen
	// at creation: a pre-existing bucket without Object Lock is reported with the
	// ObjectLockMissing condition, never fixed in place.
	// +optional
	ObjectLock *ObjectLockSpec `json:"objectLock,omitempty"`

	// Versioning state to enforce. Omitted means not managed. Buckets with Object
	// Lock are always versioned.
	// +optional
	Versioning VersioningStatus `json:"versioning,omitempty"`

	// Lifecycle configuration to enforce. Omitted means not managed: whatever is
	// configured on the bucket is left alone.
	// +optional
	Lifecycle *LifecycleSpec `json:"lifecycle,omitempty"`
}

// ObservedConfiguration is the bucket configuration read from Hetzner on the last
// successful reconcile, after any correction was applied.
type ObservedConfiguration struct {
	// Endpoint the controller talks to.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// ObjectLockEnabled reports whether the bucket has Object Lock.
	// +optional
	ObjectLockEnabled bool `json:"objectLockEnabled,omitempty"`

	// DefaultRetention configured on the bucket, if any.
	// +optional
	DefaultRetention *DefaultRetention `json:"defaultRetention,omitempty"`

	// Versioning status of the bucket; empty if it was never enabled.
	// +optional
	Versioning string `json:"versioning,omitempty"`

	// LifecycleRules configured on the bucket. Only reported when spec.lifecycle is
	// managed.
	// +optional
	LifecycleRules []LifecycleRule `json:"lifecycleRules,omitempty"`
}

// BucketStatus is the observed state of a Bucket.
type BucketStatus struct {
	// ObservedGeneration is the spec generation the status describes.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions: Ready, CredentialsInvalid, NameTaken, ObjectLockMissing.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Observed configuration of the bucket.
	// +optional
	Observed *ObservedConfiguration `json:"observed,omitempty"`
}

// Bucket is a Hetzner Object Storage bucket reconciled by the controller. Deleting
// a Bucket never deletes the bucket in Hetzner (orphan semantics).
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,categories=objectstorage
// +kubebuilder:printcolumn:name="Bucket",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Location",type=string,JSONPath=`.spec.location`
// +kubebuilder:printcolumn:name="Lock",type=string,JSONPath=`.spec.objectLock.defaultRetention.mode`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Bucket struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BucketSpec   `json:"spec"`
	Status BucketStatus `json:"status,omitempty"`
}

// BucketList is a list of Buckets.
//
// +kubebuilder:object:root=true
type BucketList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Bucket `json:"items"`
}
