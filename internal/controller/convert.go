/*
Copyright 2026 Maarlab Rethinking.
Licensed under the Apache License, Version 2.0 (the "License").
*/

package controller

import (
	"fmt"
	"slices"
	"strings"

	objectstoragev1alpha1 "github.com/maarlab-rethinking/hetzner-bucket-controller/api/v1alpha1"
	"github.com/maarlab-rethinking/hetzner-bucket-controller/internal/objectstorage"
)

// DefaultEndpoint returns the Hetzner S3 endpoint of a location.
func DefaultEndpoint(location string) string {
	return "https://" + location + ".your-objectstorage.com"
}

func endpointFor(spec *objectstoragev1alpha1.BucketSpec) string {
	if spec.Endpoint != "" {
		return strings.TrimSuffix(spec.Endpoint, "/")
	}
	return DefaultEndpoint(spec.Location)
}

func desiredRetention(spec *objectstoragev1alpha1.BucketSpec) *objectstorage.Retention {
	if spec.ObjectLock == nil || spec.ObjectLock.DefaultRetention == nil {
		return nil
	}
	dr := spec.ObjectLock.DefaultRetention
	return &objectstorage.Retention{Mode: string(dr.Mode), Days: dr.Days}
}

func retentionEqual(a, b *objectstorage.Retention) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func describeRetention(r *objectstorage.Retention) string {
	switch {
	case r == nil:
		return "none"
	case r.Years > 0:
		return fmt.Sprintf("%s %dy", r.Mode, r.Years)
	default:
		return fmt.Sprintf("%s %dd", r.Mode, r.Days)
	}
}

// observedRetention converts a retention read from the bucket for status. A retention
// expressed in years is reported in days (365 per year) for display only.
func observedRetention(r *objectstorage.Retention) *objectstoragev1alpha1.DefaultRetention {
	if r == nil {
		return nil
	}
	days := r.Days
	if r.Years > 0 {
		days = r.Years * 365
	}
	return &objectstoragev1alpha1.DefaultRetention{Mode: objectstoragev1alpha1.RetentionMode(r.Mode), Days: days}
}

// desiredRules returns the spec's lifecycle rules sorted by ID; every rule the
// controller writes is enabled.
func desiredRules(spec *objectstoragev1alpha1.BucketSpec) []objectstorage.LifecycleRule {
	out := make([]objectstorage.LifecycleRule, 0, len(spec.Lifecycle.Rules))
	for _, r := range spec.Lifecycle.Rules {
		out = append(out, objectstorage.LifecycleRule{
			ID:                        r.ID,
			Prefix:                    r.Prefix,
			Enabled:                   true,
			ExpirationDays:            r.ExpirationDays,
			ExpiredObjectDeleteMarker: r.ExpiredObjectDeleteMarker,
			NoncurrentDays:            r.NoncurrentVersionExpirationDays,
			AbortIncompleteDays:       r.AbortIncompleteMultipartUploadDays,
		})
	}
	sortRules(out)
	return out
}

func sortRules(rules []objectstorage.LifecycleRule) {
	slices.SortFunc(rules, func(a, b objectstorage.LifecycleRule) int { return strings.Compare(a.ID, b.ID) })
}

// rulesEqual compares two rule sets regardless of order. A rule the controller cannot
// represent never matches.
func rulesEqual(desired, actual []objectstorage.LifecycleRule) bool {
	if len(desired) != len(actual) {
		return false
	}
	a := slices.Clone(actual)
	sortRules(a)
	for i := range desired {
		if a[i].Unmodelled || desired[i] != a[i] {
			return false
		}
	}
	return true
}

func observedRules(rules []objectstorage.LifecycleRule) []objectstoragev1alpha1.LifecycleRule {
	if len(rules) == 0 {
		return nil
	}
	out := make([]objectstoragev1alpha1.LifecycleRule, 0, len(rules))
	for _, r := range rules {
		out = append(out, objectstoragev1alpha1.LifecycleRule{
			ID:                                 r.ID,
			Prefix:                             r.Prefix,
			ExpirationDays:                     r.ExpirationDays,
			ExpiredObjectDeleteMarker:          r.ExpiredObjectDeleteMarker,
			NoncurrentVersionExpirationDays:    r.NoncurrentDays,
			AbortIncompleteMultipartUploadDays: r.AbortIncompleteDays,
		})
	}
	return out
}
