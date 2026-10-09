/*
Copyright 2026 Maarlab Rethinking.
Licensed under the Apache License, Version 2.0 (the "License").
*/

package controller

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	objectstoragev1alpha1 "github.com/maarlab-rethinking/hetzner-bucket-controller/api/v1alpha1"
	"github.com/maarlab-rethinking/hetzner-bucket-controller/internal/objectstorage/fakes3"
)

// The envtest suite runs a real kube-apiserver and etcd (no kubelet, no controllers):
// it checks what only an API server can, the CRD's OpenAPI and CEL validation, and
// the controller wired into a manager. `make test` provides KUBEBUILDER_ASSETS; a bare
// `go test` skips these tests.
var (
	envCfg    *rest.Config
	envClient client.Client
)

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		os.Exit(m.Run())
	}
	logf.SetLogger(zap.New(zap.WriteTo(io.Discard)))
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		fmt.Fprintln(os.Stderr, "start envtest:", err)
		os.Exit(1)
	}
	envCfg = cfg
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = objectstoragev1alpha1.AddToScheme(scheme)
	envClient, err = client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		fmt.Fprintln(os.Stderr, "envtest client:", err)
		_ = env.Stop()
		os.Exit(1)
	}
	code := m.Run()
	_ = env.Stop()
	os.Exit(code)
}

func requireEnvtest(t *testing.T) {
	t.Helper()
	if envCfg == nil {
		t.Skip("KUBEBUILDER_ASSETS not set; run `make test`")
	}
}

func createNamespace(t *testing.T) string {
	t.Helper()
	n := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "bucket-test-"}}
	if err := envClient.Create(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	return n.Name
}

func validBucket(namespace, name string) *objectstoragev1alpha1.Bucket {
	b := snapshotsBucket(name)
	b.Namespace = namespace
	b.UID = ""
	b.Generation = 0
	return b
}

func TestCRDValidation(t *testing.T) {
	requireEnvtest(t)
	ctx := context.Background()
	namespace := createNamespace(t)

	t.Run("defaults the secret keys", func(t *testing.T) {
		b := validBucket(namespace, "defaults")
		if err := envClient.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		if b.Spec.Credentials.AccessKeyIDKey != "access-key" || b.Spec.Credentials.SecretAccessKeyKey != "secret-key" {
			t.Fatalf("credentials = %+v", b.Spec.Credentials)
		}
	})

	rejectedOnCreate := map[string]func(*objectstoragev1alpha1.Bucket){
		"name with upper case":   func(b *objectstoragev1alpha1.Bucket) { b.Spec.Name = "Backups" },
		"name with a dot":        func(b *objectstoragev1alpha1.Bucket) { b.Spec.Name = "my.backups" },
		"name ending in a dash":  func(b *objectstoragev1alpha1.Bucket) { b.Spec.Name = "backups-" },
		"name too short":         func(b *objectstoragev1alpha1.Bucket) { b.Spec.Name = "ab" },
		"endpoint with a path":   func(b *objectstoragev1alpha1.Bucket) { b.Spec.Endpoint = "https://nbg1.example.com/bucket" },
		"unknown retention mode": func(b *objectstoragev1alpha1.Bucket) { b.Spec.ObjectLock.DefaultRetention.Mode = "LEGAL" },
		"zero retention days":    func(b *objectstoragev1alpha1.Bucket) { b.Spec.ObjectLock.DefaultRetention.Days = 0 },
		"rule without action": func(b *objectstoragev1alpha1.Bucket) {
			b.Spec.Lifecycle.Rules = []objectstoragev1alpha1.LifecycleRule{{ID: "noop"}}
		},
		"expiration days with delete marker": func(b *objectstoragev1alpha1.Bucket) {
			b.Spec.Lifecycle.Rules = []objectstoragev1alpha1.LifecycleRule{{ID: "x", ExpirationDays: 1, ExpiredObjectDeleteMarker: true}}
		},
		"duplicate rule IDs": func(b *objectstoragev1alpha1.Bucket) {
			b.Spec.Lifecycle.Rules = []objectstoragev1alpha1.LifecycleRule{{ID: "x", ExpirationDays: 1}, {ID: "x", ExpirationDays: 2}}
		},
		"suspended versioning with Object Lock": func(b *objectstoragev1alpha1.Bucket) {
			b.Spec.Versioning = objectstoragev1alpha1.VersioningSuspended
		},
	}
	i := 0
	for name, mutate := range rejectedOnCreate {
		t.Run("rejects "+name, func(t *testing.T) {
			i++
			b := validBucket(namespace, fmt.Sprintf("invalid-%d", i))
			mutate(b)
			err := envClient.Create(ctx, b)
			if err == nil {
				t.Fatal("accepted")
			}
			t.Log(err)
		})
	}

	rejectedOnUpdate := map[string]func(*objectstoragev1alpha1.Bucket){
		"changing the name":      func(b *objectstoragev1alpha1.Bucket) { b.Spec.Name = "another-name" },
		"changing the location":  func(b *objectstoragev1alpha1.Bucket) { b.Spec.Location = "fsn1" },
		"removing Object Lock":   func(b *objectstoragev1alpha1.Bucket) { b.Spec.ObjectLock = nil },
		"adding Object Lock":     nil, // handled below: starts from a bucket without it
		"suspending versioning":  func(b *objectstoragev1alpha1.Bucket) { b.Spec.Versioning = objectstoragev1alpha1.VersioningSuspended },
		"retention mode garbage": func(b *objectstoragev1alpha1.Bucket) { b.Spec.ObjectLock.DefaultRetention.Mode = "x" },
	}
	for name, mutate := range rejectedOnUpdate {
		t.Run("rejects "+name, func(t *testing.T) {
			i++
			b := validBucket(namespace, fmt.Sprintf("update-%d", i))
			if mutate == nil {
				b.Spec.ObjectLock = nil
				mutate = func(b *objectstoragev1alpha1.Bucket) {
					b.Spec.ObjectLock = &objectstoragev1alpha1.ObjectLockSpec{}
				}
			}
			if err := envClient.Create(ctx, b); err != nil {
				t.Fatal(err)
			}
			mutate(b)
			err := envClient.Update(ctx, b)
			if err == nil {
				t.Fatal("accepted")
			}
			t.Log(err)
		})
	}

	t.Run("allows changing retention, lifecycle and endpoint", func(t *testing.T) {
		b := validBucket(namespace, "mutable")
		if err := envClient.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		b.Spec.ObjectLock.DefaultRetention = &objectstoragev1alpha1.DefaultRetention{Mode: objectstoragev1alpha1.RetentionGovernance, Days: 7}
		b.Spec.Lifecycle = nil
		b.Spec.Endpoint = "https://nbg1.your-objectstorage.com"
		if err := envClient.Update(ctx, b); err != nil {
			t.Fatal(err)
		}
	})
}

// TestControllerInManager runs the reconciler in a real manager against the fake S3.
func TestControllerInManager(t *testing.T) {
	requireEnvtest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr, err := ctrl.NewManager(envCfg, ctrl.Options{
		Scheme:  envClient.Scheme(),
		Metrics: metricsserver.Options{BindAddress: "0"},
		// Discarded: the manager can still log after the test returns, and t.Log
		// panics then.
		Logger: zap.New(zap.WriteTo(io.Discard)),
	})
	if err != nil {
		t.Fatal(err)
	}
	s3 := fakes3.New(accessKey, secretKey)
	r := &BucketReconciler{
		Client:       mgr.GetClient(),
		APIReader:    mgr.GetAPIReader(),
		Recorder:     mgr.GetEventRecorder("hetzner-bucket-controller"),
		NewClient:    s3.NewClient,
		ResyncPeriod: 2 * time.Second,
		WaitPeriod:   time.Second,
	}
	if err := r.SetupWithManager(ctx, mgr); err != nil {
		t.Fatal(err)
	}
	go func() { _ = mgr.Start(ctx) }()

	namespace := createNamespace(t)
	b := validBucket(namespace, "snapshots")
	// A name of its own: the validation test's Buckets claim the default one, and the
	// controller would rightly report a Conflict.
	b.Spec.Name = "manager-test-backups"
	if err := envClient.Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	key := types.NamespacedName{Namespace: namespace, Name: "snapshots"}

	// No Secret yet: waits with CredentialsInvalid.
	eventually(t, func() error {
		return expectReady(ctx, key, metav1.ConditionFalse, objectstoragev1alpha1.ReasonSecretNotFound)
	})

	secret := validSecret()
	secret.Namespace = namespace
	if err := envClient.Create(ctx, secret); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() error {
		return expectReady(ctx, key, metav1.ConditionTrue, objectstoragev1alpha1.ReasonAvailable)
	})
	if got, ok := s3.Get(b.Spec.Name); !ok || !got.ObjectLock {
		t.Fatalf("bucket = %+v, %v", got, ok)
	}

	// Drift is corrected by the periodic resync alone, no Kubernetes event involved.
	s3.Mutate(b.Spec.Name, func(fb *fakes3.Bucket) { fb.Lifecycle = nil })
	eventually(t, func() error {
		if got, _ := s3.Get(b.Spec.Name); len(got.Lifecycle) != 2 {
			return fmt.Errorf("lifecycle not restored: %+v", got.Lifecycle)
		}
		return nil
	})

	// Orphan on delete: no finalizer, the object goes at once and the bucket stays.
	var live objectstoragev1alpha1.Bucket
	if err := envClient.Get(ctx, key, &live); err != nil {
		t.Fatal(err)
	}
	if len(live.Finalizers) != 0 {
		t.Fatalf("finalizers = %v, want none", live.Finalizers)
	}
	if err := envClient.Delete(ctx, &live); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() error {
		err := envClient.Get(ctx, key, &live)
		if client.IgnoreNotFound(err) != nil {
			return err
		}
		if err == nil {
			return fmt.Errorf("still present")
		}
		return nil
	})
	if _, ok := s3.Get(b.Spec.Name); !ok {
		t.Fatal("deleting the Bucket object deleted the bucket")
	}
}

func expectReady(ctx context.Context, key types.NamespacedName, status metav1.ConditionStatus, reason string) error {
	var b objectstoragev1alpha1.Bucket
	if err := envClient.Get(ctx, key, &b); err != nil {
		return err
	}
	c := meta.FindStatusCondition(b.Status.Conditions, objectstoragev1alpha1.ConditionReady)
	if c == nil || c.Status != status || c.Reason != reason {
		return fmt.Errorf("Ready = %+v, want %s/%s", c, status, reason)
	}
	if b.Status.ObservedGeneration != b.Generation {
		return fmt.Errorf("observedGeneration %d != generation %d", b.Status.ObservedGeneration, b.Generation)
	}
	return nil
}

func eventually(t *testing.T, fn func() error) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		err := fn()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
