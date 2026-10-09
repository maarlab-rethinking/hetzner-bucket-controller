//go:build integration

/*
Copyright 2026 Maarlab Rethinking.
Licensed under the Apache License, Version 2.0 (the "License").
*/

// Package integration runs the S3 client and the reconciler against a real S3 server:
// MinIO, which implements Object Lock, default retention, versioning and lifecycle like
// Hetzner does. It needs a Docker-compatible daemon (testcontainers) and, for the
// reconciler test, the envtest binaries: run it with `make test-integration`.
package integration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	objectstoragev1alpha1 "github.com/maarlab-rethinking/hetzner-bucket-controller/api/v1alpha1"
	"github.com/maarlab-rethinking/hetzner-bucket-controller/internal/controller"
	"github.com/maarlab-rethinking/hetzner-bucket-controller/internal/objectstorage"
)

const (
	rootUser     = "integration"
	rootPassword = "integration-secret"
	// Same as the Hetzner location the platform uses; MinIO enforces the region on
	// signing and on CreateBucket like Hetzner enforces the location.
	region = "nbg1"
)

// minioImage defaults to Chainguard's MinIO build: MinIO no longer publishes
// community images to Docker Hub or Quay. Override with MINIO_IMAGE.
func minioImage() string {
	if img := os.Getenv("MINIO_IMAGE"); img != "" {
		return img
	}
	return "cgr.dev/chainguard/minio:latest"
}

func startMinIO(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	ctr, err := testcontainers.Run(ctx, minioImage(),
		testcontainers.WithCmd("server", "/data"),
		testcontainers.WithEnv(map[string]string{
			"MINIO_ROOT_USER":     rootUser,
			"MINIO_ROOT_PASSWORD": rootPassword,
			"MINIO_REGION":        region,
		}),
		testcontainers.WithExposedPorts("9000/tcp"),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/minio/health/ready").WithPort("9000/tcp").WithStartupTimeout(2*time.Minute)),
	)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatalf("start MinIO: %v", err)
	}
	endpoint, err := ctr.PortEndpoint(ctx, "9000/tcp", "http")
	if err != nil {
		t.Fatal(err)
	}
	return endpoint
}

func s3Client(t *testing.T, endpoint, reg, sk string) objectstorage.Client {
	t.Helper()
	c, err := objectstorage.NewS3Client(objectstorage.Config{
		Endpoint: endpoint, Region: reg, AccessKeyID: rootUser, SecretAccessKey: sk, ForcePathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// snapshotRules are the rules of the OpenBao snapshots bucket.
var snapshotRules = []objectstorage.LifecycleRule{
	{ID: "drop-delete-markers", Enabled: true, ExpiredObjectDeleteMarker: true},
	{ID: "expire-snapshots", Enabled: true, ExpirationDays: 35, NoncurrentDays: 5},
}

// TestS3Client checks every call of the aws-sdk-go-v2 client against a real server,
// including the checksum settings and the error-code classification.
func TestS3Client(t *testing.T) {
	endpoint := startMinIO(t)
	ctx := context.Background()
	c := s3Client(t, endpoint, region, rootPassword)

	t.Run("rejected credentials", func(t *testing.T) {
		_, err := s3Client(t, endpoint, region, "wrong").BucketExists(ctx, "x")
		if !objectstorage.IsCredentialsError(err) {
			t.Fatalf("err = %v, want a CredentialsError", err)
		}
	})

	t.Run("bucket with Object Lock", func(t *testing.T) {
		const name = "locked-backups"
		if ok, err := c.BucketExists(ctx, name); err != nil || ok {
			t.Fatalf("exists before creation: %v %v", ok, err)
		}
		if err := c.CreateBucket(ctx, name, region, true); err != nil {
			t.Fatal(err)
		}
		if ok, err := c.BucketExists(ctx, name); err != nil || !ok {
			t.Fatalf("exists after creation: %v %v", ok, err)
		}
		if err := c.CreateBucket(ctx, name, region, true); !errors.Is(err, objectstorage.ErrBucketAlreadyOwnedByYou) {
			t.Fatalf("second CreateBucket: %v, want ErrBucketAlreadyOwnedByYou", err)
		}

		lock, err := c.GetObjectLockConfiguration(ctx, name)
		if err != nil || !lock.Enabled || lock.DefaultRetention != nil {
			t.Fatalf("lock = %+v, %v", lock, err)
		}
		want := &objectstorage.Retention{Mode: "COMPLIANCE", Days: 30}
		if err := c.PutDefaultRetention(ctx, name, want); err != nil {
			t.Fatal(err)
		}
		lock, err = c.GetObjectLockConfiguration(ctx, name)
		if err != nil || lock.DefaultRetention == nil || *lock.DefaultRetention != *want {
			t.Fatalf("lock = %+v, %v", lock, err)
		}
		if err := c.PutDefaultRetention(ctx, name, nil); err != nil {
			t.Fatal(err)
		}
		if lock, _ = c.GetObjectLockConfiguration(ctx, name); lock.DefaultRetention != nil {
			t.Fatalf("retention not cleared: %+v", lock.DefaultRetention)
		}

		if v, err := c.GetVersioning(ctx, name); err != nil || v != "Enabled" {
			t.Fatalf("versioning = %q, %v; Object Lock must force it on", v, err)
		}

		if rules, err := c.GetLifecycle(ctx, name); err != nil || len(rules) != 0 {
			t.Fatalf("lifecycle before put = %+v, %v", rules, err)
		}
		if err := c.PutLifecycle(ctx, name, snapshotRules); err != nil {
			t.Fatal(err)
		}
		rules, err := c.GetLifecycle(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		if len(rules) != len(snapshotRules) {
			t.Fatalf("lifecycle = %+v", rules)
		}
		byID := map[string]objectstorage.LifecycleRule{}
		for _, r := range rules {
			byID[r.ID] = r
		}
		for _, w := range snapshotRules {
			if byID[w.ID] != w {
				t.Errorf("rule %s = %+v, want %+v", w.ID, byID[w.ID], w)
			}
		}
		if err := c.DeleteLifecycle(ctx, name); err != nil {
			t.Fatal(err)
		}
		if rules, _ := c.GetLifecycle(ctx, name); len(rules) != 0 {
			t.Fatalf("lifecycle after delete = %+v", rules)
		}
	})

	t.Run("bucket without Object Lock", func(t *testing.T) {
		const name = "plain-bucket"
		if err := c.CreateBucket(ctx, name, region, false); err != nil {
			t.Fatal(err)
		}
		lock, err := c.GetObjectLockConfiguration(ctx, name)
		if err != nil || lock.Enabled {
			t.Fatalf("lock = %+v, %v", lock, err)
		}
		if v, err := c.GetVersioning(ctx, name); err != nil || v != "" {
			t.Fatalf("versioning = %q, %v", v, err)
		}
		if err := c.PutVersioning(ctx, name, "Enabled"); err != nil {
			t.Fatal(err)
		}
		if v, _ := c.GetVersioning(ctx, name); v != "Enabled" {
			t.Fatalf("versioning = %q", v)
		}
	})

	t.Run("location mismatch is refused", func(t *testing.T) {
		if err := s3Client(t, endpoint, "fsn1", rootPassword).CreateBucket(ctx, "wrong-location", "fsn1", false); err == nil {
			t.Fatal("CreateBucket in a region the server is not in succeeded")
		}
	})
}

// TestReconcilerAgainstMinIO runs the controller, with the real S3 client, in a
// manager against envtest and MinIO.
func TestReconcilerAgainstMinIO(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS not set; run `make test-integration`")
	}
	endpoint := startMinIO(t)
	logf.SetLogger(zap.New(zap.WriteTo(io.Discard)))

	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = env.Stop() })

	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = objectstoragev1alpha1.AddToScheme(scheme)
	k8s, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme, Metrics: metricsserver.Options{BindAddress: "0"}})
	if err != nil {
		t.Fatal(err)
	}
	r := &controller.BucketReconciler{
		Client:       mgr.GetClient(),
		APIReader:    mgr.GetAPIReader(),
		Recorder:     mgr.GetEventRecorder("hetzner-bucket-controller"),
		NewClient:    objectstorage.NewS3Client,
		ResyncPeriod: 2 * time.Second,
		WaitPeriod:   time.Second,
	}
	if err := r.SetupWithManager(ctx, mgr); err != nil {
		t.Fatal(err)
	}
	go func() { _ = mgr.Start(ctx) }()

	ns := "backup-buckets"
	if err := k8s.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
		t.Fatal(err)
	}
	// What External Secrets materializes from OpenBao.
	if err := k8s.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "object-storage-openbao-snapshots"},
		StringData: map[string]string{"access-key": rootUser, "secret-key": rootPassword},
	}); err != nil {
		t.Fatal(err)
	}
	const bucket = "maarlab-management-nbg1-backups"
	b := &objectstoragev1alpha1.Bucket{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "openbao-snapshots"},
		Spec: objectstoragev1alpha1.BucketSpec{
			Name:           bucket,
			Location:       region,
			Endpoint:       endpoint,
			ForcePathStyle: true,
			Credentials:    objectstoragev1alpha1.CredentialsSecretRef{Name: "object-storage-openbao-snapshots"},
			ObjectLock: &objectstoragev1alpha1.ObjectLockSpec{DefaultRetention: &objectstoragev1alpha1.DefaultRetention{
				Mode: objectstoragev1alpha1.RetentionCompliance, Days: 30,
			}},
			Lifecycle: &objectstoragev1alpha1.LifecycleSpec{Rules: []objectstoragev1alpha1.LifecycleRule{
				{ID: "expire-snapshots", ExpirationDays: 35, NoncurrentVersionExpirationDays: 5},
				{ID: "drop-delete-markers", ExpiredObjectDeleteMarker: true},
			}},
		},
	}
	if err := k8s.Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	key := types.NamespacedName{Namespace: ns, Name: b.Name}
	eventually(t, func() error { return ready(ctx, k8s, key) })

	s3 := s3Client(t, endpoint, region, rootPassword)
	lock, err := s3.GetObjectLockConfiguration(ctx, bucket)
	if err != nil || !lock.Enabled || lock.DefaultRetention == nil || lock.DefaultRetention.Days != 30 {
		t.Fatalf("lock = %+v, %v", lock, err)
	}

	// Out-of-band drift: retention weakened and lifecycle removed. The resync restores both.
	if err := s3.PutDefaultRetention(ctx, bucket, &objectstorage.Retention{Mode: "GOVERNANCE", Days: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s3.DeleteLifecycle(ctx, bucket); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() error {
		lock, err := s3.GetObjectLockConfiguration(ctx, bucket)
		if err != nil {
			return err
		}
		if lock.DefaultRetention == nil || lock.DefaultRetention.Mode != "COMPLIANCE" || lock.DefaultRetention.Days != 30 {
			return fmt.Errorf("retention not restored: %+v", lock.DefaultRetention)
		}
		rules, err := s3.GetLifecycle(ctx, bucket)
		if err != nil {
			return err
		}
		if len(rules) != 2 {
			return fmt.Errorf("lifecycle not restored: %+v", rules)
		}
		return nil
	})

	// A pre-existing bucket without Object Lock is reported, not fixed.
	if err := s3.CreateBucket(ctx, "unlocked-backups", region, false); err != nil {
		t.Fatal(err)
	}
	unlocked := b.DeepCopy()
	unlocked.ObjectMeta = metav1.ObjectMeta{Namespace: ns, Name: "unlocked"}
	unlocked.Spec.Name = "unlocked-backups"
	if err := k8s.Create(ctx, unlocked); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() error {
		var got objectstoragev1alpha1.Bucket
		if err := k8s.Get(ctx, types.NamespacedName{Namespace: ns, Name: "unlocked"}, &got); err != nil {
			return err
		}
		if !meta.IsStatusConditionTrue(got.Status.Conditions, objectstoragev1alpha1.ConditionObjectLockMissing) {
			return fmt.Errorf("conditions = %+v", got.Status.Conditions)
		}
		return nil
	})

	// Deleting the Bucket object leaves the bucket.
	if err := k8s.Delete(ctx, b); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() error {
		var got objectstoragev1alpha1.Bucket
		if err := k8s.Get(ctx, key, &got); client.IgnoreNotFound(err) != nil {
			return err
		} else if err == nil {
			return errors.New("still present")
		}
		return nil
	})
	if ok, err := s3.BucketExists(ctx, bucket); err != nil || !ok {
		t.Fatalf("bucket gone after deleting the Bucket object: %v %v", ok, err)
	}
}

func ready(ctx context.Context, k8s client.Client, key types.NamespacedName) error {
	var b objectstoragev1alpha1.Bucket
	if err := k8s.Get(ctx, key, &b); err != nil {
		return err
	}
	if !meta.IsStatusConditionTrue(b.Status.Conditions, objectstoragev1alpha1.ConditionReady) {
		return fmt.Errorf("not ready: %+v", b.Status.Conditions)
	}
	return nil
}

func eventually(t *testing.T, fn func() error) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		err := fn()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(300 * time.Millisecond)
	}
}
