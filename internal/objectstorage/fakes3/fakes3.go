/*
Copyright 2026 Maarlab Rethinking.
Licensed under the Apache License, Version 2.0 (the "License").
*/

// Package fakes3 is an in-memory objectstorage.Client that mimics the Hetzner
// semantics the controller relies on: global bucket names, Object Lock only at
// creation, versioning forced on by Object Lock, and rejected credentials.
package fakes3

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/maarlab-rethinking/hetzner-bucket-controller/internal/objectstorage"
)

// Bucket is the state of one fake bucket.
type Bucket struct {
	ObjectLock bool
	Retention  *objectstorage.Retention
	Versioning string
	Lifecycle  []objectstorage.LifecycleRule
}

// Server is a fake Object Storage shared by every Client it hands out. The zero value
// is not usable; call New.
type Server struct {
	mu sync.Mutex
	// Buckets owned by the (single) project of ValidKeys.
	buckets map[string]*Bucket
	// Foreign holds names that belong to someone else.
	foreign map[string]bool
	// ValidKeys maps access key -> secret key.
	validKeys map[string]string
	// Calls counts calls per operation name.
	calls map[string]int
	// failures injects an error into the next call of an operation.
	failures map[string]error
}

// New returns a Server accepting the given key pair.
func New(accessKey, secretKey string) *Server {
	return &Server{
		buckets:   map[string]*Bucket{},
		foreign:   map[string]bool{},
		validKeys: map[string]string{accessKey: secretKey},
		calls:     map[string]int{},
		failures:  map[string]error{},
	}
}

// NewClient is an objectstorage.NewClientFunc bound to the server.
func (s *Server) NewClient(cfg objectstorage.Config) (objectstorage.Client, error) {
	return &client{s: s, cfg: cfg}, nil
}

// AddBucket pre-creates an owned bucket.
func (s *Server) AddBucket(name string, b Bucket) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b.ObjectLock {
		b.Versioning = "Enabled"
	}
	s.buckets[name] = &b
}

// AddForeign marks a name as taken by another customer.
func (s *Server) AddForeign(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.foreign[name] = true
}

// DeleteBucket removes a bucket, as an out-of-band deletion would.
func (s *Server) DeleteBucket(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.buckets, name)
}

// Mutate changes a bucket's state in place, as an out-of-band edit would.
func (s *Server) Mutate(name string, fn func(*Bucket)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s.buckets[name])
}

// Get returns a copy of a bucket's state.
func (s *Server) Get(name string) (Bucket, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.buckets[name]
	if !ok {
		return Bucket{}, false
	}
	out := *b
	if b.Retention != nil {
		r := *b.Retention
		out.Retention = &r
	}
	out.Lifecycle = slices.Clone(b.Lifecycle)
	return out, true
}

// Calls returns how many times op was called.
func (s *Server) Calls(op string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[op]
}

// FailNext makes the next call to op return err.
func (s *Server) FailNext(op string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures[op] = err
}

type client struct {
	s   *Server
	cfg objectstorage.Config
}

// enter counts the call and checks injected failures and credentials. The caller
// holds the lock.
func (c *client) enter(op string) error {
	c.s.calls[op]++
	if err, ok := c.s.failures[op]; ok {
		delete(c.s.failures, op)
		return err
	}
	if sk, ok := c.s.validKeys[c.cfg.AccessKeyID]; !ok {
		return &objectstorage.CredentialsError{Code: "InvalidAccessKeyId", Message: "unknown key"}
	} else if sk != c.cfg.SecretAccessKey {
		return &objectstorage.CredentialsError{Code: "SignatureDoesNotMatch", Message: "bad secret"}
	}
	return nil
}

func (c *client) bucket(name string) (*Bucket, error) {
	b, ok := c.s.buckets[name]
	if !ok {
		return nil, fmt.Errorf("NoSuchBucket: %s", name)
	}
	return b, nil
}

func (c *client) BucketExists(_ context.Context, name string) (bool, error) {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	if err := c.enter("BucketExists"); err != nil {
		return false, err
	}
	_, ok := c.s.buckets[name]
	return ok, nil
}

func (c *client) CreateBucket(_ context.Context, name, region string, objectLock bool) error {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	if err := c.enter("CreateBucket"); err != nil {
		return err
	}
	if region != c.cfg.Region {
		return errors.New("LocationConstraintConflict")
	}
	if _, ok := c.s.buckets[name]; ok {
		return objectstorage.ErrBucketAlreadyOwnedByYou
	}
	if c.s.foreign[name] {
		return objectstorage.ErrBucketAlreadyExists
	}
	b := &Bucket{ObjectLock: objectLock}
	if objectLock {
		b.Versioning = "Enabled"
	}
	c.s.buckets[name] = b
	return nil
}

func (c *client) GetObjectLockConfiguration(_ context.Context, name string) (objectstorage.LockConfiguration, error) {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	if err := c.enter("GetObjectLockConfiguration"); err != nil {
		return objectstorage.LockConfiguration{}, err
	}
	b, err := c.bucket(name)
	if err != nil {
		return objectstorage.LockConfiguration{}, err
	}
	if !b.ObjectLock {
		return objectstorage.LockConfiguration{}, nil
	}
	lc := objectstorage.LockConfiguration{Enabled: true}
	if b.Retention != nil {
		r := *b.Retention
		lc.DefaultRetention = &r
	}
	return lc, nil
}

func (c *client) PutDefaultRetention(_ context.Context, name string, r *objectstorage.Retention) error {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	if err := c.enter("PutDefaultRetention"); err != nil {
		return err
	}
	b, err := c.bucket(name)
	if err != nil {
		return err
	}
	if !b.ObjectLock {
		return errors.New("InvalidBucketState: Object Lock is not enabled")
	}
	if r == nil {
		b.Retention = nil
	} else {
		cp := *r
		b.Retention = &cp
	}
	return nil
}

func (c *client) GetVersioning(_ context.Context, name string) (string, error) {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	if err := c.enter("GetVersioning"); err != nil {
		return "", err
	}
	b, err := c.bucket(name)
	if err != nil {
		return "", err
	}
	return b.Versioning, nil
}

func (c *client) PutVersioning(_ context.Context, name, status string) error {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	if err := c.enter("PutVersioning"); err != nil {
		return err
	}
	b, err := c.bucket(name)
	if err != nil {
		return err
	}
	if b.ObjectLock && status != "Enabled" {
		return errors.New("InvalidBucketState: versioning cannot be suspended with Object Lock")
	}
	b.Versioning = status
	return nil
}

func (c *client) GetLifecycle(_ context.Context, name string) ([]objectstorage.LifecycleRule, error) {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	if err := c.enter("GetLifecycle"); err != nil {
		return nil, err
	}
	b, err := c.bucket(name)
	if err != nil {
		return nil, err
	}
	return slices.Clone(b.Lifecycle), nil
}

func (c *client) PutLifecycle(_ context.Context, name string, rules []objectstorage.LifecycleRule) error {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	if err := c.enter("PutLifecycle"); err != nil {
		return err
	}
	b, err := c.bucket(name)
	if err != nil {
		return err
	}
	b.Lifecycle = slices.Clone(rules)
	return nil
}

func (c *client) DeleteLifecycle(_ context.Context, name string) error {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	if err := c.enter("DeleteLifecycle"); err != nil {
		return err
	}
	b, err := c.bucket(name)
	if err != nil {
		return err
	}
	b.Lifecycle = nil
	return nil
}
