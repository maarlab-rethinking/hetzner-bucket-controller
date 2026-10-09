/*
Copyright 2026 Maarlab Rethinking.
Licensed under the Apache License, Version 2.0 (the "License").
*/

package controller

import (
	"testing"

	objectstoragev1alpha1 "github.com/maarlab-rethinking/hetzner-bucket-controller/api/v1alpha1"
	"github.com/maarlab-rethinking/hetzner-bucket-controller/internal/objectstorage"
)

func TestRulesEqualIgnoresOrder(t *testing.T) {
	spec := &objectstoragev1alpha1.BucketSpec{Lifecycle: &objectstoragev1alpha1.LifecycleSpec{Rules: []objectstoragev1alpha1.LifecycleRule{
		{ID: "b", ExpirationDays: 2}, {ID: "a", ExpiredObjectDeleteMarker: true},
	}}}
	want := desiredRules(spec)
	actual := []objectstorage.LifecycleRule{
		{ID: "b", Enabled: true, ExpirationDays: 2},
		{ID: "a", Enabled: true, ExpiredObjectDeleteMarker: true},
	}
	if !rulesEqual(want, actual) {
		t.Fatal("same rules in another order reported as different")
	}
	actual[0].Enabled = false
	if rulesEqual(want, actual) {
		t.Fatal("a disabled rule must count as drift")
	}
}

func TestRetentionInYearsIsDrift(t *testing.T) {
	want := &objectstorage.Retention{Mode: "COMPLIANCE", Days: 365}
	if retentionEqual(want, &objectstorage.Retention{Mode: "COMPLIANCE", Years: 1}) {
		t.Fatal("years treated as equal to days")
	}
	if got := observedRetention(&objectstorage.Retention{Mode: "COMPLIANCE", Years: 1}); got.Days != 365 {
		t.Fatalf("observed = %+v", got)
	}
}

func TestEndpoint(t *testing.T) {
	if got := endpointFor(&objectstoragev1alpha1.BucketSpec{Location: "fsn1"}); got != "https://fsn1.your-objectstorage.com" {
		t.Fatal(got)
	}
	if got := endpointFor(&objectstoragev1alpha1.BucketSpec{Location: "fsn1", Endpoint: "http://x:9000/"}); got != "http://x:9000" {
		t.Fatal(got)
	}
}
