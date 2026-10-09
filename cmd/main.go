/*
Copyright 2026 Maarlab Rethinking.
Licensed under the Apache License, Version 2.0 (the "License").
*/

// hetzner-bucket-controller reconciles Bucket objects into Hetzner Object Storage
// buckets: it creates missing buckets (with Object Lock when requested), keeps their
// default retention, versioning and lifecycle rules as declared, and never deletes a
// bucket.
package main

import (
	"flag"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	objectstoragev1alpha1 "github.com/maarlab-rethinking/hetzner-bucket-controller/api/v1alpha1"
	"github.com/maarlab-rethinking/hetzner-bucket-controller/internal/controller"
	"github.com/maarlab-rethinking/hetzner-bucket-controller/internal/objectstorage"
)

const name = "hetzner-bucket-controller"

func main() {
	var (
		metricsAddr, probeAddr string
		leaderElect            bool
		opts                   = zap.Options{}
		r                      = controller.BucketReconciler{NewClient: objectstorage.NewS3Client}
	)
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "Address the metrics endpoint binds to; \"0\" disables it.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "Address the health probes bind to.")
	flag.BoolVar(&leaderElect, "leader-elect", false, "Enable leader election (required with more than one replica).")
	flag.DurationVar(&r.ResyncPeriod, "resync-period", controller.DefaultResyncPeriod,
		"How often every Bucket is re-checked against Hetzner to correct drift and recreate deleted buckets.")
	flag.DurationVar(&r.WaitPeriod, "wait-period", controller.DefaultWaitPeriod,
		"Retry interval while a Bucket waits on its credentials Secret or on a name conflict.")
	flag.DurationVar(&r.OperationTimeout, "operation-timeout", controller.DefaultOperationTimeout,
		"Upper bound for the S3 calls of one reconcile.")
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	setupLog := ctrl.Log.WithName("setup")

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(objectstoragev1alpha1.AddToScheme(scheme))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         leaderElect,
		LeaderElectionID:       name + ".objectstorage.maarlab.dev",
		// Stop reconciling as soon as leadership is lost.
		LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "create manager")
		os.Exit(1)
	}

	ctx := ctrl.SetupSignalHandler()
	r.Client = mgr.GetClient()
	r.APIReader = mgr.GetAPIReader()
	r.Recorder = mgr.GetEventRecorder(name)
	if err := r.SetupWithManager(ctx, mgr); err != nil {
		setupLog.Error(err, "set up controller")
		os.Exit(1)
	}
	utilruntime.Must(mgr.AddHealthzCheck("healthz", healthz.Ping))
	utilruntime.Must(mgr.AddReadyzCheck("readyz", healthz.Ping))

	setupLog.Info("starting manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "run manager")
		os.Exit(1)
	}
}
