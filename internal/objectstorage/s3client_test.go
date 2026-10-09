/*
Copyright 2026 Maarlab Rethinking.
Licensed under the Apache License, Version 2.0 (the "License").
*/

package objectstorage

import (
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

func TestRuleRoundTrip(t *testing.T) {
	rules := []LifecycleRule{
		{ID: "expire", Enabled: true, ExpirationDays: 35, NoncurrentDays: 5},
		{ID: "markers", Enabled: true, Prefix: "openbao/", ExpiredObjectDeleteMarker: true},
		{ID: "multipart", Enabled: true, AbortIncompleteDays: 7},
	}
	for _, r := range rules {
		if got := fromS3Rule(toS3Rule(r)); got != r {
			t.Errorf("round trip %+v -> %+v", r, got)
		}
	}
}

func TestToS3RuleUsesFilterPrefix(t *testing.T) {
	r := toS3Rule(LifecycleRule{ID: "x", Enabled: true, ExpirationDays: 1})
	if r.Filter == nil || r.Filter.Prefix == nil || *r.Filter.Prefix != "" {
		t.Fatalf("filter = %+v, want an empty prefix filter", r.Filter)
	}
	if r.Status != s3types.ExpirationStatusEnabled {
		t.Fatalf("status = %v", r.Status)
	}
}

func TestFromS3RuleReadsLegacyPrefix(t *testing.T) {
	r := fromS3Rule(s3types.LifecycleRule{
		ID: aws.String("x"), Status: s3types.ExpirationStatusEnabled,
		Prefix:     aws.String("logs/"), //nolint:staticcheck // the deprecated field is what we test
		Expiration: &s3types.LifecycleExpiration{Days: aws.Int32(3)},
	})
	if r.Prefix != "logs/" || r.Unmodelled {
		t.Fatalf("rule = %+v", r)
	}
}

func TestFromS3RuleFlagsUnmodelledElements(t *testing.T) {
	cases := map[string]s3types.LifecycleRule{
		"transition": {Transitions: []s3types.Transition{{Days: aws.Int32(1)}}},
		"tag filter": {Filter: &s3types.LifecycleRuleFilter{Tag: &s3types.Tag{Key: aws.String("k"), Value: aws.String("v")}}},
		"newer noncurrent": {NoncurrentVersionExpiration: &s3types.NoncurrentVersionExpiration{
			NoncurrentDays: aws.Int32(1), NewerNoncurrentVersions: aws.Int32(3),
		}},
		"size filter": {Filter: &s3types.LifecycleRuleFilter{ObjectSizeGreaterThan: aws.Int64(1)}},
	}
	for name, in := range cases {
		if !fromS3Rule(in).Unmodelled {
			t.Errorf("%s: not flagged as unmodelled", name)
		}
	}
}

func TestClassify(t *testing.T) {
	for _, code := range []string{"InvalidAccessKeyId", "SignatureDoesNotMatch", "AccessDenied"} {
		err := classify("op", &smithy.GenericAPIError{Code: code, Message: "m"})
		if !IsCredentialsError(err) {
			t.Errorf("%s: not a credentials error", code)
		}
	}
	err := classify("op", &smithy.GenericAPIError{Code: "InternalError"})
	if IsCredentialsError(err) {
		t.Error("InternalError classified as credentials error")
	}
	var ae smithy.APIError
	if !errors.As(err, &ae) {
		t.Error("the API error is no longer reachable with errors.As")
	}
}

func TestNewS3ClientValidatesConfig(t *testing.T) {
	if _, err := NewS3Client(Config{Region: "nbg1", AccessKeyID: "a", SecretAccessKey: "b"}); err == nil {
		t.Error("accepted a config without endpoint")
	}
	if _, err := NewS3Client(Config{Endpoint: "https://nbg1.your-objectstorage.com", Region: "nbg1"}); err == nil {
		t.Error("accepted a config without credentials")
	}
}
