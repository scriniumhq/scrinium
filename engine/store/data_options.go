package store

import (
	"time"

	"scrinium.dev/domain"
)

// PutOption configures a Put. Options are applied left to right; the
// zero set (no options) means: default namespace, no session, regular
// blob, no retention, no routing hints.
type PutOption func(*putConfig)

// putConfig is the assembled Put configuration. It never leaves the
// store package — the public surface is the PutOption functions; the
// internal layers (artifactio, manifest assembly) receive this struct.
type putConfig struct {
	namespace      string
	sessionID      domain.SessionID
	externalURI    string
	blobType       domain.BlobType
	retentionUntil time.Time
	routing        domain.RoutingHints
}

// WithNamespace places the artifact in a namespace. Empty (the default)
// is the store's default namespace.
func WithNamespace(ns string) PutOption {
	return func(c *putConfig) { c.namespace = ns }
}

// WithSession ties the Put to a session for RollbackSession.
func WithSession(id domain.SessionID) PutOption {
	return func(c *putConfig) { c.sessionID = id }
}

// WithExternalURI records an external location for the payload.
func WithExternalURI(uri string) PutOption {
	return func(c *putConfig) { c.externalURI = uri }
}

// WithBlobType sets the blob type (default: regular).
func WithBlobType(bt domain.BlobType) PutOption {
	return func(c *putConfig) { c.blobType = bt }
}

// WithRetention sets a retention deadline for the artifact.
func WithRetention(t time.Time) PutOption {
	return func(c *putConfig) { c.retentionUntil = t }
}

// WithRouting attaches routing hints (e.g. for a multistore router).
func WithRouting(h domain.RoutingHints) PutOption {
	return func(c *putConfig) { c.routing = h }
}

// applyPut folds the options into a putConfig.
func applyPut(opts []PutOption) putConfig {
	var c putConfig
	for _, o := range opts {
		o(&c)
	}
	return c
}

// toDomain converts the assembled config into the internal DTO that the
// store's lower layers (artifactio, manifest assembly) consume. The DTO
// stays inside the store; callers never see it.
func (c putConfig) toDomain() domain.PutOptions {
	return domain.PutOptions{
		SessionID:      c.sessionID,
		Namespace:      c.namespace,
		ExternalURI:    c.externalURI,
		BlobType:       c.blobType,
		RetentionUntil: c.retentionUntil,
		Routing:        c.routing,
	}
}

// GetOption configures a Get. The zero set means a normal read.
type GetOption func(*getConfig)

// getConfig is the assembled Get configuration. Never leaves the store
// package.
type getConfig struct {
	allowColdRead bool
}

// WithColdRead permits fetching from cold/expensive backing storage
// (e.g. a multistore tier that is normally skipped on reads).
func WithColdRead() GetOption {
	return func(c *getConfig) { c.allowColdRead = true }
}

// applyGet folds the options into a getConfig.
func applyGet(opts []GetOption) getConfig {
	var c getConfig
	for _, o := range opts {
		o(&c)
	}
	return c
}

// ResolvePutOptions folds PutOptions into the internal DTO. Exported
// for decorators and test doubles that wrap a Store and need to read
// the resolved values (namespace, session, …) — the option functions
// themselves are opaque.
func ResolvePutOptions(opts ...PutOption) domain.PutOptions {
	return applyPut(opts).toDomain()
}

// ResolveGetOptions reports whether a cold read was requested. Exported
// for decorators (e.g. multistore) that wrap a Store and branch on it.
func ResolveGetOptions(opts ...GetOption) (allowColdRead bool) {
	return applyGet(opts).allowColdRead
}
