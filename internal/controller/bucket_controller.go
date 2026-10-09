/*
Copyright 2026 Maarlab Rethinking.
Licensed under the Apache License, Version 2.0 (the "License").
*/

// Package controller reconciles Bucket objects against Hetzner Object Storage.
package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	objectstoragev1alpha1 "github.com/maarlab-rethinking/hetzner-bucket-controller/api/v1alpha1"
	"github.com/maarlab-rethinking/hetzner-bucket-controller/internal/objectstorage"
)

// bucketNameField indexes Buckets by spec.name to find every object claiming a name.
const bucketNameField = "spec.name"

// Defaults for the reconciler's timings.
const (
	DefaultResyncPeriod     = 10 * time.Minute
	DefaultWaitPeriod       = time.Minute
	DefaultOperationTimeout = 2 * time.Minute
)

// Event reasons.
const (
	EventCreated           = "Created"
	EventAdopted           = "Adopted"
	EventConfigured        = "Configured"
	EventDriftCorrected    = "DriftCorrected"
	EventNameTaken         = "NameTaken"
	EventObjectLockMissing = "ObjectLockMissing"
	EventCredentials       = "CredentialsInvalid"
	EventConflict          = "Conflict"
)

// BucketReconciler reconciles Bucket objects.
type BucketReconciler struct {
	client.Client
	// APIReader reads the credentials Secret straight from the API server, so the
	// controller needs only `get` on Secrets: no cluster-wide Secret cache, no
	// list/watch permission.
	APIReader client.Reader
	Recorder  events.EventRecorder
	NewClient objectstorage.NewClientFunc

	// ResyncPeriod is how often a healthy Bucket is re-checked against Hetzner to
	// detect drift and out-of-band deletion.
	ResyncPeriod time.Duration
	// WaitPeriod is the retry interval while waiting on something inside the cluster
	// (a credentials Secret that External Secrets has not written yet, a name
	// conflict between Bucket objects).
	WaitPeriod time.Duration
	// OperationTimeout bounds the S3 calls of a single reconcile.
	OperationTimeout time.Duration
}

// RBAC for the generated ClusterRole. These MUST stay a floating comment block,
// separated by a blank line from the declaration below: controller-gen only collects
// kubebuilder:rbac markers from package-level comments. There is deliberately no
// update permission on buckets nor buckets/finalizers: the controller never adds a
// finalizer (deleting a Bucket orphans the bucket, there is nothing to clean up).
//
// +kubebuilder:rbac:groups=objectstorage.maarlab.dev,resources=buckets,verbs=get;list;watch
// +kubebuilder:rbac:groups=objectstorage.maarlab.dev,resources=buckets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// SetupWithManager registers the controller and the spec.name index.
func (r *BucketReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	if r.ResyncPeriod == 0 {
		r.ResyncPeriod = DefaultResyncPeriod
	}
	if r.WaitPeriod == 0 {
		r.WaitPeriod = DefaultWaitPeriod
	}
	if r.OperationTimeout == 0 {
		r.OperationTimeout = DefaultOperationTimeout
	}
	if err := mgr.GetFieldIndexer().IndexField(ctx, &objectstoragev1alpha1.Bucket{}, bucketNameField, indexBucketName); err != nil {
		return fmt.Errorf("index %s: %w", bucketNameField, err)
	}
	// When a Bucket goes away, any other Bucket waiting on a name conflict with it
	// can take over at once instead of at its next retry.
	onDelete := predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return false },
		UpdateFunc:  func(event.UpdateEvent) bool { return false },
		DeleteFunc:  func(event.DeleteEvent) bool { return true },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("bucket").
		// Status writes do not bump the generation, so this keeps the controller from
		// reconciling its own status updates.
		For(&objectstoragev1alpha1.Bucket{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&objectstoragev1alpha1.Bucket{},
			handler.EnqueueRequestsFromMapFunc(r.sameNameBuckets),
			builder.WithPredicates(onDelete)).
		Complete(r)
}

func indexBucketName(o client.Object) []string {
	return []string{o.(*objectstoragev1alpha1.Bucket).Spec.Name}
}

func (r *BucketReconciler) sameNameBuckets(ctx context.Context, o client.Object) []reconcile.Request {
	deleted := o.(*objectstoragev1alpha1.Bucket)
	var list objectstoragev1alpha1.BucketList
	if err := r.List(ctx, &list, client.MatchingFields{bucketNameField: deleted.Spec.Name}); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for _, b := range list.Items {
		if b.UID != deleted.UID {
			reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: b.Namespace, Name: b.Name}})
		}
	}
	return reqs
}

// Reconcile drives a Bucket to its desired state and publishes the outcome in status.
// Deletion is not handled: the bucket is orphaned on purpose (docs/decisions/0004).
func (r *BucketReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var b objectstoragev1alpha1.Bucket
	if err := r.Get(ctx, req.NamespacedName, &b); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !b.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	orig := b.DeepCopy()
	opCtx, cancel := context.WithTimeout(ctx, r.OperationTimeout)
	defer cancel()
	res, err := r.reconcile(opCtx, &b)
	b.Status.ObservedGeneration = b.Generation

	if !equality.Semantic.DeepEqual(orig.Status, b.Status) {
		if perr := r.Status().Patch(ctx, &b, client.MergeFrom(orig)); perr != nil {
			return ctrl.Result{}, errors.Join(err, fmt.Errorf("patch status: %w", perr))
		}
	}
	return res, err
}

// reconcile does the work and records conditions on b; Reconcile persists them.
func (r *BucketReconciler) reconcile(ctx context.Context, b *objectstoragev1alpha1.Bucket) (ctrl.Result, error) {
	l := log.FromContext(ctx).WithValues("bucket", b.Spec.Name, "location", b.Spec.Location)
	spec := &b.Spec
	wasReady := meta.IsStatusConditionTrue(b.Status.Conditions, objectstoragev1alpha1.ConditionReady)

	// 1. One Bucket object per bucket name. Names are global, so two objects for the
	//    same name would point at the same bucket and fight over its configuration.
	owner, err := r.nameOwner(ctx, b)
	if err != nil {
		return ctrl.Result{}, err
	}
	if owner != nil {
		msg := fmt.Sprintf("bucket %q is already managed by Bucket %s/%s", spec.Name, owner.Namespace, owner.Name)
		r.warnOnTransition(b, objectstoragev1alpha1.ReasonConflict, EventConflict, "Reconcile", msg)
		r.setUnchecked(b, objectstoragev1alpha1.ConditionCredentialsInvalid, objectstoragev1alpha1.ConditionNameTaken, objectstoragev1alpha1.ConditionObjectLockMissing)
		setReady(b, false, objectstoragev1alpha1.ReasonConflict, msg)
		return ctrl.Result{RequeueAfter: r.WaitPeriod}, nil
	}

	// 2. Credentials from the Secret, read uncached on every pass so a rotation is
	//    picked up without a restart.
	cfg, reason, msg := r.credentials(ctx, b)
	if reason != "" {
		r.warnOnTransition(b, reason, EventCredentials, "ReadCredentials", msg)
		setCondition(b, objectstoragev1alpha1.ConditionCredentialsInvalid, true, reason, msg)
		r.setUnchecked(b, objectstoragev1alpha1.ConditionNameTaken, objectstoragev1alpha1.ConditionObjectLockMissing)
		setReady(b, false, reason, msg)
		return ctrl.Result{RequeueAfter: r.WaitPeriod}, nil
	}
	s3, err := r.NewClient(cfg)
	if err != nil {
		return r.providerError(b, fmt.Errorf("build S3 client: %w", err))
	}

	// 3. Existence. ListBuckets also proves the credential works.
	exists, err := s3.BucketExists(ctx, spec.Name)
	if objectstorage.IsCredentialsError(err) {
		msg := fmt.Sprintf("Hetzner rejected the key pair in Secret %q: %v", spec.Credentials.Name, err)
		r.warnOnTransition(b, objectstoragev1alpha1.ReasonCredentialsInvalid, EventCredentials, "ListBuckets", msg)
		setCondition(b, objectstoragev1alpha1.ConditionCredentialsInvalid, true, objectstoragev1alpha1.ReasonCredentialsInvalid, msg)
		r.setUnchecked(b, objectstoragev1alpha1.ConditionNameTaken, objectstoragev1alpha1.ConditionObjectLockMissing)
		setReady(b, false, objectstoragev1alpha1.ReasonCredentialsInvalid, msg)
		// A new key arrives through External Secrets; no point in a fast retry.
		return ctrl.Result{RequeueAfter: r.jitter(r.ResyncPeriod)}, nil
	}
	if err != nil {
		return r.providerError(b, err)
	}
	setCondition(b, objectstoragev1alpha1.ConditionCredentialsInvalid, false, objectstoragev1alpha1.ReasonCredentialsValid,
		fmt.Sprintf("Secret %q holds a key pair Hetzner accepts", spec.Credentials.Name))

	// 4. Create when missing (again, if it was deleted out of band).
	if !exists {
		err := s3.CreateBucket(ctx, spec.Name, spec.Location, spec.ObjectLock != nil)
		switch {
		case err == nil:
			lock := "without Object Lock"
			if spec.ObjectLock != nil {
				lock = "with Object Lock"
			}
			l.Info("created bucket", "objectLock", spec.ObjectLock != nil)
			r.event(b, corev1.EventTypeNormal, EventCreated, "CreateBucket", fmt.Sprintf("Created bucket %q in %s %s", spec.Name, spec.Location, lock))
		case errors.Is(err, objectstorage.ErrBucketAlreadyOwnedByYou):
			r.event(b, corev1.EventTypeNormal, EventAdopted, "CreateBucket", fmt.Sprintf("Adopted existing bucket %q", spec.Name))
		case errors.Is(err, objectstorage.ErrBucketAlreadyExists):
			msg := fmt.Sprintf("bucket name %q belongs to another Hetzner project or customer; bucket names are global, choose another", spec.Name)
			r.warnOnTransition(b, objectstoragev1alpha1.ReasonNameTaken, EventNameTaken, "CreateBucket", msg)
			setCondition(b, objectstoragev1alpha1.ConditionNameTaken, true, objectstoragev1alpha1.ReasonNameTaken, msg)
			r.setUnchecked(b, objectstoragev1alpha1.ConditionObjectLockMissing)
			setReady(b, false, objectstoragev1alpha1.ReasonNameTaken, msg)
			// Not an error: retrying with backoff cannot help. Check again at the
			// resync in case the owner deletes it.
			return ctrl.Result{RequeueAfter: r.jitter(r.ResyncPeriod)}, nil
		default:
			return r.providerError(b, err)
		}
	} else if b.Status.Observed == nil {
		r.event(b, corev1.EventTypeNormal, EventAdopted, "ListBuckets", fmt.Sprintf("Adopted existing bucket %q", spec.Name))
	}
	setCondition(b, objectstoragev1alpha1.ConditionNameTaken, false, objectstoragev1alpha1.ReasonNameAvailable,
		fmt.Sprintf("bucket %q is owned by the credential's project", spec.Name))

	observed := &objectstoragev1alpha1.ObservedConfiguration{Endpoint: cfg.Endpoint}
	var changes []string

	// 5. Object Lock. It cannot be added to an existing bucket: report, never "fix".
	lock, err := s3.GetObjectLockConfiguration(ctx, spec.Name)
	if err != nil {
		return r.providerError(b, err)
	}
	observed.ObjectLockEnabled = lock.Enabled
	lockMissing := spec.ObjectLock != nil && !lock.Enabled
	if lockMissing {
		msg := fmt.Sprintf("bucket %q exists without Object Lock, which can only be enabled at creation; "+
			"create a new bucket (another name) to get immutability", spec.Name)
		r.warnOnTransition(b, objectstoragev1alpha1.ReasonObjectLockMissing, EventObjectLockMissing, "GetObjectLockConfiguration", msg)
		setCondition(b, objectstoragev1alpha1.ConditionObjectLockMissing, true, objectstoragev1alpha1.ReasonObjectLockMissing, msg)
	} else {
		setCondition(b, objectstoragev1alpha1.ConditionObjectLockMissing, false, objectstoragev1alpha1.ReasonObjectLockOK,
			fmt.Sprintf("Object Lock enabled: %t", lock.Enabled))
	}
	observed.DefaultRetention = observedRetention(lock.DefaultRetention)
	if spec.ObjectLock != nil && lock.Enabled {
		want := desiredRetention(spec)
		if !retentionEqual(want, lock.DefaultRetention) {
			if err := s3.PutDefaultRetention(ctx, spec.Name, want); err != nil {
				return r.providerError(b, err)
			}
			changes = append(changes, fmt.Sprintf("default retention %s -> %s", describeRetention(lock.DefaultRetention), describeRetention(want)))
			observed.DefaultRetention = observedRetention(want)
		}
	}

	// 6. Versioning (forced on, permanently, by Object Lock).
	versioning, err := s3.GetVersioning(ctx, spec.Name)
	if err != nil {
		return r.providerError(b, err)
	}
	// Suspending versioning on a bucket with Object Lock fails; the error surfaces as
	// ProviderError (the CRD only rejects it when the spec also requests the lock).
	if want := string(spec.Versioning); want != "" && want != versioning {
		if err := s3.PutVersioning(ctx, spec.Name, want); err != nil {
			return r.providerError(b, err)
		}
		changes = append(changes, fmt.Sprintf("versioning %q -> %q", versioning, want))
		versioning = want
	}
	observed.Versioning = versioning

	// 7. Lifecycle, managed as a whole when the spec asks for it.
	if spec.Lifecycle != nil {
		current, err := s3.GetLifecycle(ctx, spec.Name)
		if err != nil {
			return r.providerError(b, err)
		}
		want := desiredRules(spec)
		if !rulesEqual(want, current) {
			if len(want) == 0 {
				err = s3.DeleteLifecycle(ctx, spec.Name)
			} else {
				err = s3.PutLifecycle(ctx, spec.Name, want)
			}
			if err != nil {
				return r.providerError(b, err)
			}
			changes = append(changes, fmt.Sprintf("lifecycle rules [%s] -> [%s]", ruleIDs(current), ruleIDs(want)))
			current = want
		}
		observed.LifecycleRules = observedRules(current)
	}
	b.Status.Observed = observed

	if len(changes) > 0 {
		reason := EventConfigured
		// Same generation and already Ready means nobody changed the spec: the bucket
		// was changed outside the cluster.
		if wasReady && b.Status.ObservedGeneration == b.Generation {
			reason = EventDriftCorrected
		}
		l.Info("applied bucket configuration", "reason", reason, "changes", changes)
		r.event(b, corev1.EventTypeNormal, reason, "Update", strings.Join(changes, "; "))
	}

	if lockMissing {
		setReady(b, false, objectstoragev1alpha1.ReasonObjectLockMissing,
			fmt.Sprintf("bucket %q exists without the Object Lock the spec requests", spec.Name))
	} else {
		setReady(b, true, objectstoragev1alpha1.ReasonAvailable, fmt.Sprintf("bucket %q exists and matches the spec", spec.Name))
	}
	return ctrl.Result{RequeueAfter: r.jitter(r.ResyncPeriod)}, nil
}

// nameOwner returns the Bucket that owns spec.name when it is not b: the oldest one,
// ties broken by namespace/name. It returns nil when b is the owner.
func (r *BucketReconciler) nameOwner(ctx context.Context, b *objectstoragev1alpha1.Bucket) (*objectstoragev1alpha1.Bucket, error) {
	var list objectstoragev1alpha1.BucketList
	if err := r.List(ctx, &list, client.MatchingFields{bucketNameField: b.Spec.Name}); err != nil {
		return nil, fmt.Errorf("list Buckets named %q: %w", b.Spec.Name, err)
	}
	owner := b
	for i := range list.Items {
		o := &list.Items[i]
		if o.UID == b.UID || !o.DeletionTimestamp.IsZero() {
			continue
		}
		if older(o, owner) {
			owner = o
		}
	}
	if owner.UID == b.UID {
		return nil, nil
	}
	return owner, nil
}

func older(a, b *objectstoragev1alpha1.Bucket) bool {
	if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
		return a.CreationTimestamp.Before(&b.CreationTimestamp)
	}
	return a.Namespace+"/"+a.Name < b.Namespace+"/"+b.Name
}

// credentials reads the key pair. On failure it returns the condition reason and a
// message; on success an empty reason.
func (r *BucketReconciler) credentials(ctx context.Context, b *objectstoragev1alpha1.Bucket) (objectstorage.Config, string, string) {
	ref := b.Spec.Credentials
	var s corev1.Secret
	if err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: b.Namespace, Name: ref.Name}, &s); err != nil {
		if apierrors.IsNotFound(err) {
			return objectstorage.Config{}, objectstoragev1alpha1.ReasonSecretNotFound,
				fmt.Sprintf("credentials Secret %q not found in namespace %q", ref.Name, b.Namespace)
		}
		if apierrors.IsForbidden(err) {
			return objectstorage.Config{}, objectstoragev1alpha1.ReasonSecretForbidden,
				fmt.Sprintf("controller may not read Secrets in namespace %q; add it to the chart's rbac.secretNamespaces", b.Namespace)
		}
		return objectstorage.Config{}, objectstoragev1alpha1.ReasonSecretNotFound,
			fmt.Sprintf("read credentials Secret %q: %v", ref.Name, err)
	}
	akKey, skKey := keyOrDefault(ref.AccessKeyIDKey, "access-key"), keyOrDefault(ref.SecretAccessKeyKey, "secret-key")
	ak, sk := strings.TrimSpace(string(s.Data[akKey])), strings.TrimSpace(string(s.Data[skKey]))
	var missing []string
	if ak == "" {
		missing = append(missing, akKey)
	}
	if sk == "" {
		missing = append(missing, skKey)
	}
	if len(missing) > 0 {
		return objectstorage.Config{}, objectstoragev1alpha1.ReasonSecretKeyMissing,
			fmt.Sprintf("credentials Secret %q has no (or an empty) %s", ref.Name, strings.Join(missing, " and "))
	}
	return objectstorage.Config{
		Endpoint:        endpointFor(&b.Spec),
		Region:          b.Spec.Location,
		AccessKeyID:     ak,
		SecretAccessKey: sk,
		ForcePathStyle:  b.Spec.ForcePathStyle,
	}, "", ""
}

func keyOrDefault(k, def string) string {
	if k == "" {
		return def
	}
	return k
}

// providerError records an unexpected S3 failure and returns it, so the request is
// retried with backoff.
func (r *BucketReconciler) providerError(b *objectstoragev1alpha1.Bucket, err error) (ctrl.Result, error) {
	setReady(b, false, objectstoragev1alpha1.ReasonProviderError, err.Error())
	return ctrl.Result{}, err
}

// setUnchecked marks conditions that could not be evaluated on this pass.
func (r *BucketReconciler) setUnchecked(b *objectstoragev1alpha1.Bucket, condTypes ...string) {
	for _, t := range condTypes {
		meta.SetStatusCondition(&b.Status.Conditions, metav1.Condition{
			Type:               t,
			Status:             metav1.ConditionUnknown,
			Reason:             objectstoragev1alpha1.ReasonUnknown,
			Message:            "not evaluated: an earlier check failed",
			ObservedGeneration: b.Generation,
		})
	}
}

// warnOnTransition emits a Warning event the first time Ready turns False with reason,
// not on every retry.
func (r *BucketReconciler) warnOnTransition(b *objectstoragev1alpha1.Bucket, reason, eventReason, action, msg string) {
	c := meta.FindStatusCondition(b.Status.Conditions, objectstoragev1alpha1.ConditionReady)
	if c != nil && c.Status == metav1.ConditionFalse && c.Reason == reason {
		return
	}
	r.event(b, corev1.EventTypeWarning, eventReason, action, msg)
}

func (r *BucketReconciler) event(b *objectstoragev1alpha1.Bucket, eventType, reason, action, msg string) {
	if r.Recorder != nil {
		r.Recorder.Eventf(b, nil, eventType, reason, action, "%s", msg)
	}
}

func (r *BucketReconciler) jitter(d time.Duration) time.Duration {
	return wait.Jitter(d, 0.1)
}

func setCondition(b *objectstoragev1alpha1.Bucket, condType string, status bool, reason, msg string) {
	s := metav1.ConditionFalse
	if status {
		s = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&b.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             s,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: b.Generation,
	})
}

func setReady(b *objectstoragev1alpha1.Bucket, ready bool, reason, msg string) {
	setCondition(b, objectstoragev1alpha1.ConditionReady, ready, reason, msg)
}

func ruleIDs(rules []objectstorage.LifecycleRule) string {
	ids := make([]string, 0, len(rules))
	for _, r := range rules {
		ids = append(ids, r.ID)
	}
	return strings.Join(ids, ",")
}
