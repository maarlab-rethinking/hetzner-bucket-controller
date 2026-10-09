/*
Copyright 2026 Maarlab Rethinking.
Licensed under the Apache License, Version 2.0 (the "License").
*/

// Package objectstorage is the narrow S3 surface the controller needs, in terms of
// what Hetzner Object Storage supports. The controller only talks to the Client
// interface, so its logic is tested against an in-memory fake; the aws-sdk-go-v2
// implementation is exercised against a real S3 server in the integration tests.
package objectstorage

import (
	"context"
	"errors"
	"fmt"
)

// Retention is an Object Lock default retention.
type Retention struct {
	// Mode is COMPLIANCE or GOVERNANCE.
	Mode string
	// Days of retention; zero when the retention is expressed in Years.
	Days int32
	// Years of retention; zero when expressed in Days. The controller only writes
	// Days, a Years value set out of band is reported as drift.
	Years int32
}

// LockConfiguration is a bucket's Object Lock state.
type LockConfiguration struct {
	// Enabled is true when the bucket was created with Object Lock.
	Enabled bool
	// DefaultRetention is nil when the bucket has no default retention.
	DefaultRetention *Retention
}

// LifecycleRule is a lifecycle rule restricted to the elements Hetzner supports.
// Zero-valued numbers mean "not set" (S3 requires them to be at least 1).
type LifecycleRule struct {
	ID                        string
	Prefix                    string
	Enabled                   bool
	ExpirationDays            int32
	ExpiredObjectDeleteMarker bool
	NoncurrentDays            int32
	AbortIncompleteDays       int32
	// Unmodelled is set for a rule read from the bucket that uses an element this
	// type cannot represent (transitions, tag or size filters, expiration dates,
	// NewerNoncurrentVersions). Such a rule never equals a desired one, so the
	// controller replaces it.
	Unmodelled bool
}

// Config is what a Client needs to reach one Hetzner location with one credential.
type Config struct {
	Endpoint        string
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	ForcePathStyle  bool
}

// Client is the subset of the S3 API the controller uses.
type Client interface {
	// BucketExists reports whether the credential's project owns the bucket. It
	// lists the project's buckets rather than issuing a HEAD, because a HEAD answer
	// has no body and cannot tell rejected credentials from a foreign bucket.
	BucketExists(ctx context.Context, bucket string) (bool, error)
	// CreateBucket creates the bucket in region, with Object Lock when objectLock is
	// true. It returns ErrBucketAlreadyOwnedByYou or ErrBucketAlreadyExists when the
	// name is in use.
	CreateBucket(ctx context.Context, bucket, region string, objectLock bool) error
	// GetObjectLockConfiguration returns Enabled=false for a bucket without Object Lock.
	GetObjectLockConfiguration(ctx context.Context, bucket string) (LockConfiguration, error)
	// PutDefaultRetention sets the default retention, or clears it when r is nil.
	// The bucket must have Object Lock.
	PutDefaultRetention(ctx context.Context, bucket string, r *Retention) error
	// GetVersioning returns "Enabled", "Suspended" or "" (never enabled).
	GetVersioning(ctx context.Context, bucket string) (string, error)
	// PutVersioning sets the versioning status.
	PutVersioning(ctx context.Context, bucket, status string) error
	// GetLifecycle returns the lifecycle rules, empty if there is no configuration.
	GetLifecycle(ctx context.Context, bucket string) ([]LifecycleRule, error)
	// PutLifecycle replaces the lifecycle configuration with rules (at least one).
	PutLifecycle(ctx context.Context, bucket string, rules []LifecycleRule) error
	// DeleteLifecycle removes the lifecycle configuration.
	DeleteLifecycle(ctx context.Context, bucket string) error
}

// NewClientFunc builds a Client for a configuration.
type NewClientFunc func(Config) (Client, error)

var (
	// ErrBucketAlreadyOwnedByYou: the bucket exists in the credential's project.
	ErrBucketAlreadyOwnedByYou = errors.New("bucket already owned by you")
	// ErrBucketAlreadyExists: the name belongs to another project or customer.
	ErrBucketAlreadyExists = errors.New("bucket name already taken")
)

// CredentialsError is returned when the service rejects the key pair.
type CredentialsError struct {
	Code    string
	Message string
}

func (e *CredentialsError) Error() string {
	return fmt.Sprintf("credentials rejected: %s: %s", e.Code, e.Message)
}

// IsCredentialsError reports whether err (or something it wraps) is a CredentialsError.
func IsCredentialsError(err error) bool {
	var ce *CredentialsError
	return errors.As(err, &ce)
}
