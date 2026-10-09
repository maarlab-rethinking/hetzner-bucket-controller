/*
Copyright 2026 Maarlab Rethinking.
Licensed under the Apache License, Version 2.0 (the "License").
*/

package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	objectstoragev1alpha1 "github.com/maarlab-rethinking/hetzner-bucket-controller/api/v1alpha1"
	"github.com/maarlab-rethinking/hetzner-bucket-controller/internal/objectstorage"
	"github.com/maarlab-rethinking/hetzner-bucket-controller/internal/objectstorage/fakes3"
)

const (
	ns        = "backup-buckets"
	accessKey = "AK"
	secretKey = "SK"
)

type harness struct {
	t      *testing.T
	c      client.Client
	s3     *fakes3.Server
	events *events.FakeRecorder
	r      *BucketReconciler
}

func newHarness(t *testing.T, objs ...client.Object) *harness {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := objectstoragev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&objectstoragev1alpha1.Bucket{}).
		WithIndex(&objectstoragev1alpha1.Bucket{}, bucketNameField, indexBucketName).
		Build()
	s3 := fakes3.New(accessKey, secretKey)
	rec := events.NewFakeRecorder(100)
	return &harness{t: t, c: c, s3: s3, events: rec, r: &BucketReconciler{
		Client:           c,
		APIReader:        c,
		Recorder:         rec,
		NewClient:        s3.NewClient,
		ResyncPeriod:     10 * time.Minute,
		WaitPeriod:       time.Minute,
		OperationTimeout: time.Minute,
	}}
}

func credsSecret(name string, data map[string]string) *corev1.Secret {
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}, Data: map[string][]byte{}}
	for k, v := range data {
		s.Data[k] = []byte(v)
	}
	return s
}

func validSecret() *corev1.Secret {
	return credsSecret("creds", map[string]string{"access-key": accessKey, "secret-key": secretKey})
}

// snapshotsBucket mirrors the OpenBao snapshots bucket the ensure-bucket Job created.
func snapshotsBucket(name string) *objectstoragev1alpha1.Bucket {
	return &objectstoragev1alpha1.Bucket{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: types.UID("uid-" + name), Generation: 1},
		Spec: objectstoragev1alpha1.BucketSpec{
			Name:        "maarlab-management-nbg1-backups",
			Location:    "nbg1",
			Credentials: objectstoragev1alpha1.CredentialsSecretRef{Name: "creds"},
			ObjectLock: &objectstoragev1alpha1.ObjectLockSpec{DefaultRetention: &objectstoragev1alpha1.DefaultRetention{
				Mode: objectstoragev1alpha1.RetentionCompliance, Days: 30,
			}},
			Lifecycle: &objectstoragev1alpha1.LifecycleSpec{Rules: []objectstoragev1alpha1.LifecycleRule{
				{ID: "expire-snapshots", ExpirationDays: 35, NoncurrentVersionExpirationDays: 5},
				{ID: "drop-delete-markers", ExpiredObjectDeleteMarker: true},
			}},
		},
	}
}

func (h *harness) reconcile(name string) (ctrl.Result, error) {
	h.t.Helper()
	return h.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}})
}

func (h *harness) get(name string) *objectstoragev1alpha1.Bucket {
	h.t.Helper()
	var b objectstoragev1alpha1.Bucket
	if err := h.c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &b); err != nil {
		h.t.Fatal(err)
	}
	return &b
}

func (h *harness) expectCondition(name, condType string, status metav1.ConditionStatus, reason string) {
	h.t.Helper()
	c := meta.FindStatusCondition(h.get(name).Status.Conditions, condType)
	if c == nil {
		h.t.Fatalf("condition %s missing", condType)
	}
	if c.Status != status || (reason != "" && c.Reason != reason) {
		h.t.Fatalf("condition %s = %s/%s (%s), want %s/%s", condType, c.Status, c.Reason, c.Message, status, reason)
	}
}

// drainEvents returns the events recorded so far.
func (h *harness) drainEvents() []string {
	var out []string
	for {
		select {
		case e := <-h.events.Events:
			out = append(out, e)
		default:
			return out
		}
	}
}

func hasEvent(evs []string, substr string) bool {
	for _, e := range evs {
		if strings.Contains(e, substr) {
			return true
		}
	}
	return false
}

func TestCreatesBucketWithObjectLockRetentionAndLifecycle(t *testing.T) {
	h := newHarness(t, validSecret(), snapshotsBucket("snapshots"))

	res, err := h.reconcile("snapshots")
	if err != nil {
		t.Fatal(err)
	}
	if res.RequeueAfter < 9*time.Minute || res.RequeueAfter > 11*time.Minute {
		t.Fatalf("RequeueAfter = %v, want the resync period (with jitter)", res.RequeueAfter)
	}

	got, ok := h.s3.Get("maarlab-management-nbg1-backups")
	if !ok {
		t.Fatal("bucket not created")
	}
	if !got.ObjectLock || got.Versioning != "Enabled" {
		t.Fatalf("bucket lock=%v versioning=%q, want lock and versioning", got.ObjectLock, got.Versioning)
	}
	if got.Retention == nil || *got.Retention != (objectstorage.Retention{Mode: "COMPLIANCE", Days: 30}) {
		t.Fatalf("retention = %+v", got.Retention)
	}
	if len(got.Lifecycle) != 2 {
		t.Fatalf("lifecycle = %+v", got.Lifecycle)
	}

	h.expectCondition("snapshots", objectstoragev1alpha1.ConditionReady, metav1.ConditionTrue, objectstoragev1alpha1.ReasonAvailable)
	h.expectCondition("snapshots", objectstoragev1alpha1.ConditionCredentialsInvalid, metav1.ConditionFalse, "")
	h.expectCondition("snapshots", objectstoragev1alpha1.ConditionNameTaken, metav1.ConditionFalse, "")
	h.expectCondition("snapshots", objectstoragev1alpha1.ConditionObjectLockMissing, metav1.ConditionFalse, "")

	b := h.get("snapshots")
	if b.Status.ObservedGeneration != 1 {
		t.Fatalf("observedGeneration = %d", b.Status.ObservedGeneration)
	}
	o := b.Status.Observed
	if o == nil || !o.ObjectLockEnabled || o.Versioning != "Enabled" || o.DefaultRetention == nil ||
		o.DefaultRetention.Days != 30 || len(o.LifecycleRules) != 2 ||
		o.Endpoint != "https://nbg1.your-objectstorage.com" {
		t.Fatalf("observed = %+v", o)
	}
	evs := h.drainEvents()
	if !hasEvent(evs, "Created") || !hasEvent(evs, "Configured") {
		t.Fatalf("events = %v", evs)
	}
}

func TestSteadyStateMakesNoWrites(t *testing.T) {
	h := newHarness(t, validSecret(), snapshotsBucket("snapshots"))
	for range 3 {
		if _, err := h.reconcile("snapshots"); err != nil {
			t.Fatal(err)
		}
	}
	if n := h.s3.Calls("CreateBucket"); n != 1 {
		t.Fatalf("CreateBucket called %d times", n)
	}
	if n := h.s3.Calls("PutDefaultRetention"); n != 1 {
		t.Fatalf("PutDefaultRetention called %d times", n)
	}
	if n := h.s3.Calls("PutLifecycle"); n != 1 {
		t.Fatalf("PutLifecycle called %d times", n)
	}
}

func TestCorrectsDrift(t *testing.T) {
	h := newHarness(t, validSecret(), snapshotsBucket("snapshots"))
	if _, err := h.reconcile("snapshots"); err != nil {
		t.Fatal(err)
	}
	h.drainEvents()

	h.s3.Mutate("maarlab-management-nbg1-backups", func(b *fakes3.Bucket) {
		b.Retention = &objectstorage.Retention{Mode: "GOVERNANCE", Days: 1}
		b.Lifecycle = []objectstorage.LifecycleRule{{ID: "rogue", Enabled: true, ExpirationDays: 1}}
	})
	if _, err := h.reconcile("snapshots"); err != nil {
		t.Fatal(err)
	}
	got, _ := h.s3.Get("maarlab-management-nbg1-backups")
	if *got.Retention != (objectstorage.Retention{Mode: "COMPLIANCE", Days: 30}) {
		t.Fatalf("retention not corrected: %+v", got.Retention)
	}
	if len(got.Lifecycle) != 2 || got.Lifecycle[0].ID != "drop-delete-markers" {
		t.Fatalf("lifecycle not corrected: %+v", got.Lifecycle)
	}
	if evs := h.drainEvents(); !hasEvent(evs, EventDriftCorrected) {
		t.Fatalf("events = %v, want DriftCorrected", evs)
	}
}

func TestReplacesUnmodelledLifecycleRule(t *testing.T) {
	h := newHarness(t, validSecret(), snapshotsBucket("snapshots"))
	if _, err := h.reconcile("snapshots"); err != nil {
		t.Fatal(err)
	}
	h.s3.Mutate("maarlab-management-nbg1-backups", func(b *fakes3.Bucket) {
		b.Lifecycle[0].Unmodelled = true
	})
	if _, err := h.reconcile("snapshots"); err != nil {
		t.Fatal(err)
	}
	if n := h.s3.Calls("PutLifecycle"); n != 2 {
		t.Fatalf("PutLifecycle called %d times, want 2", n)
	}
}

func TestRecreatesBucketDeletedOutOfBand(t *testing.T) {
	h := newHarness(t, validSecret(), snapshotsBucket("snapshots"))
	if _, err := h.reconcile("snapshots"); err != nil {
		t.Fatal(err)
	}
	h.s3.DeleteBucket("maarlab-management-nbg1-backups")
	if _, err := h.reconcile("snapshots"); err != nil {
		t.Fatal(err)
	}
	if got, ok := h.s3.Get("maarlab-management-nbg1-backups"); !ok || !got.ObjectLock {
		t.Fatal("bucket not recreated with Object Lock")
	}
}

func TestAdoptsExistingBucket(t *testing.T) {
	h := newHarness(t, validSecret(), snapshotsBucket("snapshots"))
	h.s3.AddBucket("maarlab-management-nbg1-backups", fakes3.Bucket{ObjectLock: true})
	if _, err := h.reconcile("snapshots"); err != nil {
		t.Fatal(err)
	}
	if n := h.s3.Calls("CreateBucket"); n != 0 {
		t.Fatalf("CreateBucket called %d times on an existing bucket", n)
	}
	h.expectCondition("snapshots", objectstoragev1alpha1.ConditionReady, metav1.ConditionTrue, "")
	if evs := h.drainEvents(); !hasEvent(evs, EventAdopted) {
		t.Fatalf("events = %v, want Adopted", evs)
	}
}

func TestAdoptsOnBucketAlreadyOwnedByYou(t *testing.T) {
	h := newHarness(t, validSecret(), snapshotsBucket("snapshots"))
	// The bucket appears between ListBuckets and CreateBucket: the fake answers
	// CreateBucket with BucketAlreadyOwnedByYou.
	h.s3.AddBucket("maarlab-management-nbg1-backups", fakes3.Bucket{ObjectLock: true})
	h.r.NewClient = func(cfg objectstorage.Config) (objectstorage.Client, error) {
		c, _ := h.s3.NewClient(cfg)
		return notExisting{c}, nil
	}
	if _, err := h.reconcile("snapshots"); err != nil {
		t.Fatal(err)
	}
	h.expectCondition("snapshots", objectstoragev1alpha1.ConditionReady, metav1.ConditionTrue, "")
	if evs := h.drainEvents(); !hasEvent(evs, EventAdopted) {
		t.Fatalf("events = %v, want Adopted", evs)
	}
}

// notExisting reports every bucket as missing, to force the CreateBucket path.
type notExisting struct{ objectstorage.Client }

func (notExisting) BucketExists(context.Context, string) (bool, error) { return false, nil }

func TestNameTakenDoesNotRetryWithBackoff(t *testing.T) {
	h := newHarness(t, validSecret(), snapshotsBucket("snapshots"))
	h.s3.AddForeign("maarlab-management-nbg1-backups")

	res, err := h.reconcile("snapshots")
	if err != nil {
		t.Fatalf("NameTaken must not return an error (backoff storm): %v", err)
	}
	if res.RequeueAfter < 9*time.Minute {
		t.Fatalf("RequeueAfter = %v, want the resync period", res.RequeueAfter)
	}
	h.expectCondition("snapshots", objectstoragev1alpha1.ConditionNameTaken, metav1.ConditionTrue, objectstoragev1alpha1.ReasonNameTaken)
	h.expectCondition("snapshots", objectstoragev1alpha1.ConditionReady, metav1.ConditionFalse, objectstoragev1alpha1.ReasonNameTaken)
	h.expectCondition("snapshots", objectstoragev1alpha1.ConditionObjectLockMissing, metav1.ConditionUnknown, "")

	// A second pass must not repeat the warning event.
	if _, err := h.reconcile("snapshots"); err != nil {
		t.Fatal(err)
	}
	warnings := 0
	for _, e := range h.drainEvents() {
		if strings.Contains(e, EventNameTaken) {
			warnings++
		}
	}
	if warnings != 1 {
		t.Fatalf("NameTaken warnings = %d, want 1", warnings)
	}
}

func TestObjectLockMissingIsReportedNotFixed(t *testing.T) {
	h := newHarness(t, validSecret(), snapshotsBucket("snapshots"))
	h.s3.AddBucket("maarlab-management-nbg1-backups", fakes3.Bucket{})

	if _, err := h.reconcile("snapshots"); err != nil {
		t.Fatal(err)
	}
	h.expectCondition("snapshots", objectstoragev1alpha1.ConditionObjectLockMissing, metav1.ConditionTrue, objectstoragev1alpha1.ReasonObjectLockMissing)
	h.expectCondition("snapshots", objectstoragev1alpha1.ConditionReady, metav1.ConditionFalse, objectstoragev1alpha1.ReasonObjectLockMissing)
	if n := h.s3.Calls("PutDefaultRetention"); n != 0 {
		t.Fatalf("PutDefaultRetention called %d times on a bucket without Object Lock", n)
	}
	if n := h.s3.Calls("CreateBucket"); n != 0 {
		t.Fatalf("CreateBucket called %d times", n)
	}
	// Lifecycle is still enforced: it does not depend on the lock.
	if got, _ := h.s3.Get("maarlab-management-nbg1-backups"); len(got.Lifecycle) != 2 {
		t.Fatalf("lifecycle = %+v", got.Lifecycle)
	}
	if evs := h.drainEvents(); !hasEvent(evs, "Warning "+EventObjectLockMissing) {
		t.Fatalf("events = %v, want a Warning ObjectLockMissing", evs)
	}
}

func TestMissingSecretWaits(t *testing.T) {
	h := newHarness(t, snapshotsBucket("snapshots"))
	res, err := h.reconcile("snapshots")
	if err != nil {
		t.Fatal(err)
	}
	if res.RequeueAfter != time.Minute {
		t.Fatalf("RequeueAfter = %v, want the wait period", res.RequeueAfter)
	}
	h.expectCondition("snapshots", objectstoragev1alpha1.ConditionCredentialsInvalid, metav1.ConditionTrue, objectstoragev1alpha1.ReasonSecretNotFound)
	h.expectCondition("snapshots", objectstoragev1alpha1.ConditionReady, metav1.ConditionFalse, objectstoragev1alpha1.ReasonSecretNotFound)
	if n := h.s3.Calls("BucketExists"); n != 0 {
		t.Fatal("S3 called without credentials")
	}

	// External Secrets writes the Secret: the next pass succeeds.
	if err := h.c.Create(context.Background(), validSecret()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.reconcile("snapshots"); err != nil {
		t.Fatal(err)
	}
	h.expectCondition("snapshots", objectstoragev1alpha1.ConditionReady, metav1.ConditionTrue, "")
	h.expectCondition("snapshots", objectstoragev1alpha1.ConditionCredentialsInvalid, metav1.ConditionFalse, "")
}

// forbiddenSecrets is the API reader of a controller without a Role in the Bucket's
// namespace: every Secret read is denied.
type forbiddenSecrets struct{ client.Reader }

func (forbiddenSecrets) Get(_ context.Context, key client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
	return apierrors.NewForbidden(corev1.Resource("secrets"), key.Name, errors.New("RBAC: access denied"))
}

func TestSecretForbiddenWaits(t *testing.T) {
	h := newHarness(t, snapshotsBucket("snapshots"), validSecret())
	h.r.APIReader = forbiddenSecrets{h.c}
	res, err := h.reconcile("snapshots")
	if err != nil {
		t.Fatal(err)
	}
	if res.RequeueAfter != time.Minute {
		t.Fatalf("RequeueAfter = %v, want the wait period", res.RequeueAfter)
	}
	h.expectCondition("snapshots", objectstoragev1alpha1.ConditionCredentialsInvalid, metav1.ConditionTrue, objectstoragev1alpha1.ReasonSecretForbidden)
	h.expectCondition("snapshots", objectstoragev1alpha1.ConditionReady, metav1.ConditionFalse, objectstoragev1alpha1.ReasonSecretForbidden)
	if n := h.s3.Calls("BucketExists"); n != 0 {
		t.Fatal("S3 called without credentials")
	}
}

func TestCustomSecretKeys(t *testing.T) {
	b := snapshotsBucket("snapshots")
	b.Spec.Credentials = objectstoragev1alpha1.CredentialsSecretRef{Name: "legacy", AccessKeyIDKey: "ACCESS_KEY_ID", SecretAccessKeyKey: "ACCESS_SECRET_KEY"}
	h := newHarness(t, b, credsSecret("legacy", map[string]string{"ACCESS_KEY_ID": accessKey, "ACCESS_SECRET_KEY": secretKey}))
	if _, err := h.reconcile("snapshots"); err != nil {
		t.Fatal(err)
	}
	h.expectCondition("snapshots", objectstoragev1alpha1.ConditionReady, metav1.ConditionTrue, "")
}

func TestSecretKeyMissing(t *testing.T) {
	h := newHarness(t, snapshotsBucket("snapshots"), credsSecret("creds", map[string]string{"access-key": accessKey}))
	if _, err := h.reconcile("snapshots"); err != nil {
		t.Fatal(err)
	}
	h.expectCondition("snapshots", objectstoragev1alpha1.ConditionCredentialsInvalid, metav1.ConditionTrue, objectstoragev1alpha1.ReasonSecretKeyMissing)
}

func TestRejectedCredentials(t *testing.T) {
	h := newHarness(t, snapshotsBucket("snapshots"), credsSecret("creds", map[string]string{"access-key": accessKey, "secret-key": "wrong"}))
	res, err := h.reconcile("snapshots")
	if err != nil {
		t.Fatalf("rejected credentials must not return an error: %v", err)
	}
	if res.RequeueAfter < 9*time.Minute {
		t.Fatalf("RequeueAfter = %v", res.RequeueAfter)
	}
	h.expectCondition("snapshots", objectstoragev1alpha1.ConditionCredentialsInvalid, metav1.ConditionTrue, objectstoragev1alpha1.ReasonCredentialsInvalid)
	h.expectCondition("snapshots", objectstoragev1alpha1.ConditionReady, metav1.ConditionFalse, objectstoragev1alpha1.ReasonCredentialsInvalid)
}

func TestProviderErrorIsReturnedForBackoff(t *testing.T) {
	h := newHarness(t, validSecret(), snapshotsBucket("snapshots"))
	h.s3.FailNext("BucketExists", errors.New("connection reset"))
	if _, err := h.reconcile("snapshots"); err == nil {
		t.Fatal("want the error returned so the request is retried with backoff")
	}
	h.expectCondition("snapshots", objectstoragev1alpha1.ConditionReady, metav1.ConditionFalse, objectstoragev1alpha1.ReasonProviderError)
}

func TestClearsDefaultRetentionWhenOmitted(t *testing.T) {
	b := snapshotsBucket("snapshots")
	b.Spec.ObjectLock.DefaultRetention = nil
	h := newHarness(t, validSecret(), b)
	h.s3.AddBucket("maarlab-management-nbg1-backups", fakes3.Bucket{
		ObjectLock: true, Retention: &objectstorage.Retention{Mode: "GOVERNANCE", Days: 7},
	})
	if _, err := h.reconcile("snapshots"); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.s3.Get("maarlab-management-nbg1-backups"); got.Retention != nil {
		t.Fatalf("retention = %+v, want cleared", got.Retention)
	}
}

func TestEmptyLifecycleDeletesConfiguration(t *testing.T) {
	b := snapshotsBucket("snapshots")
	b.Spec.Lifecycle = &objectstoragev1alpha1.LifecycleSpec{}
	h := newHarness(t, validSecret(), b)
	h.s3.AddBucket("maarlab-management-nbg1-backups", fakes3.Bucket{
		ObjectLock: true, Lifecycle: []objectstorage.LifecycleRule{{ID: "x", Enabled: true, ExpirationDays: 1}},
	})
	if _, err := h.reconcile("snapshots"); err != nil {
		t.Fatal(err)
	}
	if n := h.s3.Calls("DeleteLifecycle"); n != 1 {
		t.Fatalf("DeleteLifecycle called %d times", n)
	}
}

func TestUnmanagedLifecycleAndVersioningAreLeftAlone(t *testing.T) {
	b := snapshotsBucket("snapshots")
	b.Spec.ObjectLock = nil
	b.Spec.Lifecycle = nil
	h := newHarness(t, validSecret(), b)
	h.s3.AddBucket("maarlab-management-nbg1-backups", fakes3.Bucket{
		Versioning: "Suspended", Lifecycle: []objectstorage.LifecycleRule{{ID: "x", Enabled: true, ExpirationDays: 1}},
	})
	if _, err := h.reconcile("snapshots"); err != nil {
		t.Fatal(err)
	}
	if h.s3.Calls("GetLifecycle")+h.s3.Calls("PutLifecycle")+h.s3.Calls("PutVersioning") != 0 {
		t.Fatal("touched unmanaged configuration")
	}
	if got := h.get("snapshots").Status.Observed; got.Versioning != "Suspended" || got.ObjectLockEnabled {
		t.Fatalf("observed = %+v", got)
	}
}

func TestEnforcesVersioning(t *testing.T) {
	b := snapshotsBucket("snapshots")
	b.Spec.ObjectLock = nil
	b.Spec.Versioning = objectstoragev1alpha1.VersioningEnabled
	h := newHarness(t, validSecret(), b)
	if _, err := h.reconcile("snapshots"); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.s3.Get("maarlab-management-nbg1-backups"); got.ObjectLock || got.Versioning != "Enabled" {
		t.Fatalf("bucket = %+v", got)
	}
}

func TestNameConflictBetweenBucketObjects(t *testing.T) {
	first := snapshotsBucket("first")
	first.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Hour))
	second := snapshotsBucket("second")
	second.CreationTimestamp = metav1.NewTime(time.Now())
	h := newHarness(t, validSecret(), first, second)

	res, err := h.reconcile("second")
	if err != nil {
		t.Fatal(err)
	}
	if res.RequeueAfter != time.Minute {
		t.Fatalf("RequeueAfter = %v, want the wait period", res.RequeueAfter)
	}
	h.expectCondition("second", objectstoragev1alpha1.ConditionReady, metav1.ConditionFalse, objectstoragev1alpha1.ReasonConflict)
	if h.s3.Calls("BucketExists") != 0 {
		t.Fatal("the conflicting Bucket touched S3")
	}
	if _, err := h.reconcile("first"); err != nil {
		t.Fatal(err)
	}
	h.expectCondition("first", objectstoragev1alpha1.ConditionReady, metav1.ConditionTrue, "")

	// The owner goes away: the other one takes over.
	if err := h.c.Delete(context.Background(), h.get("first")); err != nil {
		t.Fatal(err)
	}
	if reqs := h.r.sameNameBuckets(context.Background(), first); len(reqs) != 1 || reqs[0].Name != "second" {
		t.Fatalf("sameNameBuckets = %v", reqs)
	}
	if _, err := h.reconcile("second"); err != nil {
		t.Fatal(err)
	}
	h.expectCondition("second", objectstoragev1alpha1.ConditionReady, metav1.ConditionTrue, "")
}

func TestDeletedBucketObjectIsIgnored(t *testing.T) {
	h := newHarness(t, validSecret())
	if _, err := h.reconcile("gone"); err != nil {
		t.Fatal(err)
	}
}

func TestCustomEndpointAndPathStyleReachClient(t *testing.T) {
	b := snapshotsBucket("snapshots")
	b.Spec.Endpoint = "http://minio.test:9000/"
	b.Spec.ForcePathStyle = true
	h := newHarness(t, validSecret(), b)
	var got objectstorage.Config
	h.r.NewClient = func(cfg objectstorage.Config) (objectstorage.Client, error) {
		got = cfg
		return h.s3.NewClient(cfg)
	}
	if _, err := h.reconcile("snapshots"); err != nil {
		t.Fatal(err)
	}
	want := objectstorage.Config{Endpoint: "http://minio.test:9000", Region: "nbg1", AccessKeyID: accessKey, SecretAccessKey: secretKey, ForcePathStyle: true}
	if got != want {
		t.Fatalf("config = %+v, want %+v", got, want)
	}
}
