/*
Copyright 2026 Maarlab Rethinking.
Licensed under the Apache License, Version 2.0 (the "License").
*/

package objectstorage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// httpTimeout bounds every single S3 request; the reconcile context bounds the sum.
const httpTimeout = 30 * time.Second

// Error codes that mean the key pair itself is wrong. AccessDenied is included because
// Hetzner credentials are project-wide: a valid key is never denied ListBuckets.
var credentialErrorCodes = map[string]bool{
	"InvalidAccessKeyId":    true,
	"SignatureDoesNotMatch": true,
	"InvalidToken":          true,
	"ExpiredToken":          true,
	"AccessDenied":          true,
}

type s3Client struct {
	api *s3.Client
}

// NewS3Client returns a Client backed by aws-sdk-go-v2.
func NewS3Client(cfg Config) (Client, error) {
	if cfg.Endpoint == "" || cfg.Region == "" {
		return nil, errors.New("endpoint and region are required")
	}
	if cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
		return nil, errors.New("access key ID and secret access key are required")
	}
	api := s3.New(s3.Options{
		BaseEndpoint: aws.String(cfg.Endpoint),
		// The signing region doubles as the CreateBucket LocationConstraint, which
		// Hetzner requires to equal the endpoint's location.
		Region:       cfg.Region,
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		UsePathStyle: cfg.ForcePathStyle,
		HTTPClient:   awshttp.NewBuildableClient().WithTimeout(httpTimeout),
		// Since 2025 the SDK adds CRC32 checksums to every request that supports them,
		// which many S3-compatible services reject. Only send them where the S3 API
		// requires a checksum.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
		AppID:                      "hetzner-bucket-controller",
	})
	return &s3Client{api: api}, nil
}

func apiErrorCode(err error) string {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		return ae.ErrorCode()
	}
	return ""
}

// classify turns credential failures into a CredentialsError and keeps the rest.
func classify(op string, err error) error {
	var ae smithy.APIError
	if errors.As(err, &ae) && credentialErrorCodes[ae.ErrorCode()] {
		return &CredentialsError{Code: ae.ErrorCode(), Message: ae.ErrorMessage()}
	}
	return fmt.Errorf("%s: %w", op, err)
}

func (c *s3Client) BucketExists(ctx context.Context, bucket string) (bool, error) {
	in := &s3.ListBucketsInput{}
	for {
		out, err := c.api.ListBuckets(ctx, in)
		if err != nil {
			return false, classify("ListBuckets", err)
		}
		for _, b := range out.Buckets {
			if aws.ToString(b.Name) == bucket {
				return true, nil
			}
		}
		if aws.ToString(out.ContinuationToken) == "" {
			return false, nil
		}
		in.ContinuationToken = out.ContinuationToken
	}
}

func (c *s3Client) CreateBucket(ctx context.Context, bucket, region string, objectLock bool) error {
	in := &s3.CreateBucketInput{
		Bucket: aws.String(bucket),
		CreateBucketConfiguration: &s3types.CreateBucketConfiguration{
			LocationConstraint: s3types.BucketLocationConstraint(region),
		},
	}
	if objectLock {
		in.ObjectLockEnabledForBucket = aws.Bool(true)
	}
	_, err := c.api.CreateBucket(ctx, in)
	switch apiErrorCode(err) {
	case "":
		if err != nil {
			return fmt.Errorf("CreateBucket: %w", err)
		}
		return nil
	case "BucketAlreadyOwnedByYou":
		return ErrBucketAlreadyOwnedByYou
	case "BucketAlreadyExists":
		return ErrBucketAlreadyExists
	default:
		return classify("CreateBucket", err)
	}
}

func (c *s3Client) GetObjectLockConfiguration(ctx context.Context, bucket string) (LockConfiguration, error) {
	out, err := c.api.GetObjectLockConfiguration(ctx, &s3.GetObjectLockConfigurationInput{Bucket: aws.String(bucket)})
	if err != nil {
		if apiErrorCode(err) == "ObjectLockConfigurationNotFoundError" {
			return LockConfiguration{}, nil
		}
		return LockConfiguration{}, classify("GetObjectLockConfiguration", err)
	}
	cfg := out.ObjectLockConfiguration
	if cfg == nil || cfg.ObjectLockEnabled != s3types.ObjectLockEnabledEnabled {
		return LockConfiguration{}, nil
	}
	lc := LockConfiguration{Enabled: true}
	if cfg.Rule != nil && cfg.Rule.DefaultRetention != nil {
		dr := cfg.Rule.DefaultRetention
		lc.DefaultRetention = &Retention{
			Mode:  string(dr.Mode),
			Days:  aws.ToInt32(dr.Days),
			Years: aws.ToInt32(dr.Years),
		}
	}
	return lc, nil
}

func (c *s3Client) PutDefaultRetention(ctx context.Context, bucket string, r *Retention) error {
	cfg := &s3types.ObjectLockConfiguration{ObjectLockEnabled: s3types.ObjectLockEnabledEnabled}
	if r != nil {
		cfg.Rule = &s3types.ObjectLockRule{DefaultRetention: &s3types.DefaultRetention{
			Mode: s3types.ObjectLockRetentionMode(r.Mode),
			Days: aws.Int32(r.Days),
		}}
	}
	_, err := c.api.PutObjectLockConfiguration(ctx, &s3.PutObjectLockConfigurationInput{
		Bucket:                  aws.String(bucket),
		ObjectLockConfiguration: cfg,
	})
	if err != nil {
		return classify("PutObjectLockConfiguration", err)
	}
	return nil
}

func (c *s3Client) GetVersioning(ctx context.Context, bucket string) (string, error) {
	out, err := c.api.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: aws.String(bucket)})
	if err != nil {
		return "", classify("GetBucketVersioning", err)
	}
	return string(out.Status), nil
}

func (c *s3Client) PutVersioning(ctx context.Context, bucket, status string) error {
	_, err := c.api.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{
		Bucket:                  aws.String(bucket),
		VersioningConfiguration: &s3types.VersioningConfiguration{Status: s3types.BucketVersioningStatus(status)},
	})
	if err != nil {
		return classify("PutBucketVersioning", err)
	}
	return nil
}

func (c *s3Client) GetLifecycle(ctx context.Context, bucket string) ([]LifecycleRule, error) {
	out, err := c.api.GetBucketLifecycleConfiguration(ctx, &s3.GetBucketLifecycleConfigurationInput{Bucket: aws.String(bucket)})
	if err != nil {
		if apiErrorCode(err) == "NoSuchLifecycleConfiguration" {
			return nil, nil
		}
		return nil, classify("GetBucketLifecycleConfiguration", err)
	}
	rules := make([]LifecycleRule, 0, len(out.Rules))
	for _, r := range out.Rules {
		rules = append(rules, fromS3Rule(r))
	}
	return rules, nil
}

func (c *s3Client) PutLifecycle(ctx context.Context, bucket string, rules []LifecycleRule) error {
	if len(rules) == 0 {
		return errors.New("PutLifecycle needs at least one rule; use DeleteLifecycle")
	}
	s3rules := make([]s3types.LifecycleRule, 0, len(rules))
	for _, r := range rules {
		s3rules = append(s3rules, toS3Rule(r))
	}
	_, err := c.api.PutBucketLifecycleConfiguration(ctx, &s3.PutBucketLifecycleConfigurationInput{
		Bucket:                 aws.String(bucket),
		LifecycleConfiguration: &s3types.BucketLifecycleConfiguration{Rules: s3rules},
	})
	if err != nil {
		return classify("PutBucketLifecycleConfiguration", err)
	}
	return nil
}

func (c *s3Client) DeleteLifecycle(ctx context.Context, bucket string) error {
	_, err := c.api.DeleteBucketLifecycle(ctx, &s3.DeleteBucketLifecycleInput{Bucket: aws.String(bucket)})
	if err != nil {
		return classify("DeleteBucketLifecycle", err)
	}
	return nil
}

func toS3Rule(r LifecycleRule) s3types.LifecycleRule {
	status := s3types.ExpirationStatusDisabled
	if r.Enabled {
		status = s3types.ExpirationStatusEnabled
	}
	out := s3types.LifecycleRule{
		ID:     aws.String(r.ID),
		Status: status,
		Filter: &s3types.LifecycleRuleFilter{Prefix: aws.String(r.Prefix)},
	}
	switch {
	case r.ExpirationDays > 0:
		out.Expiration = &s3types.LifecycleExpiration{Days: aws.Int32(r.ExpirationDays)}
	case r.ExpiredObjectDeleteMarker:
		out.Expiration = &s3types.LifecycleExpiration{ExpiredObjectDeleteMarker: aws.Bool(true)}
	}
	if r.NoncurrentDays > 0 {
		out.NoncurrentVersionExpiration = &s3types.NoncurrentVersionExpiration{NoncurrentDays: aws.Int32(r.NoncurrentDays)}
	}
	if r.AbortIncompleteDays > 0 {
		out.AbortIncompleteMultipartUpload = &s3types.AbortIncompleteMultipartUpload{DaysAfterInitiation: aws.Int32(r.AbortIncompleteDays)}
	}
	return out
}

func fromS3Rule(r s3types.LifecycleRule) LifecycleRule {
	out := LifecycleRule{
		ID:      aws.ToString(r.ID),
		Enabled: r.Status == s3types.ExpirationStatusEnabled,
		// The deprecated top-level Prefix is what Hetzner's own examples use.
		Prefix: aws.ToString(r.Prefix), //nolint:staticcheck // read for compatibility
	}
	if f := r.Filter; f != nil {
		if f.Prefix != nil {
			out.Prefix = aws.ToString(f.Prefix)
		}
		if f.Tag != nil || f.And != nil || f.ObjectSizeGreaterThan != nil || f.ObjectSizeLessThan != nil {
			out.Unmodelled = true
		}
	}
	if e := r.Expiration; e != nil {
		out.ExpirationDays = aws.ToInt32(e.Days)
		out.ExpiredObjectDeleteMarker = aws.ToBool(e.ExpiredObjectDeleteMarker)
		if e.Date != nil {
			out.Unmodelled = true
		}
	}
	if n := r.NoncurrentVersionExpiration; n != nil {
		out.NoncurrentDays = aws.ToInt32(n.NoncurrentDays)
		if aws.ToInt32(n.NewerNoncurrentVersions) != 0 {
			out.Unmodelled = true
		}
	}
	if a := r.AbortIncompleteMultipartUpload; a != nil {
		out.AbortIncompleteDays = aws.ToInt32(a.DaysAfterInitiation)
	}
	if len(r.Transitions) > 0 || len(r.NoncurrentVersionTransitions) > 0 {
		out.Unmodelled = true
	}
	return out
}
