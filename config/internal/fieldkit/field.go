// Package fieldkit is the hidden machinery behind the StoreConfig field
// registry (config review R-g / S-11). It holds the typed field
// descriptor, the validator constructors, and the traversal engine —
// everything you invoke but never edit when adding a config field.
//
// The rulebook lives one level up, in package config: the registry
// (one declaration per field) and the public API. To add a field you
// touch that declaration and nothing here. This package exists so that
// machinery — how a descriptor validates, how a default applies, how
// the traversal loops — stays out of the way, with its own unit tests.
package fieldkit

import (
	"fmt"

	"scrinium.dev/domain"
)

// FieldClass is a field's ADR-110 class.
type FieldClass int

const (
	// ClassImmutable — class I: fixed at InitStore, changed only by
	// rebuilding the store.
	ClassImmutable FieldClass = iota + 1
	// ClassGovernance — class II: admin-mutable defaults, changed only
	// by an explicit admin act (UpdateConfig), versioned.
	ClassGovernance
	// ClassSession — class III: user-mutable session preferences,
	// self-describing per artifact; a connection may override them.
	ClassSession
)

// ConnBehavior is a populated, DIVERGING client field's fate at
// OpenStore (PlanConnection).
type ConnBehavior int

const (
	// ConnRefusedImmutable — ErrConfigMismatch.
	ConnRefusedImmutable ConnBehavior = iota + 1
	// ConnRefusedGovernance — ErrGovernanceMismatch.
	ConnRefusedGovernance
	// ConnOverlay — accepted as the connection's session overlay
	// (refused like governance under SessionOverrides=Deny).
	ConnOverlay
	// ConnIgnored — not compared at connection at all (KDFParams:
	// input-only, owned by the descriptor).
	ConnIgnored
	// ConnDerived — validated as a derivative of class I (the Pipeline
	// crypto-tail rule) rather than compared verbatim.
	ConnDerived
)

// Desc is the type-erased row the registry stores. Field[T] implements
// it for every concrete field type, so fields of different Go types
// coexist in one slice. A hand-written descriptor (e.g. Pipeline)
// implements Desc directly.
type Desc interface {
	Name() string
	Class() FieldClass
	Conn() ConnBehavior
	// Validate checks this field's value in cfg (enum / bounds).
	Validate(cfg domain.StoreConfig) error
	// Diverges reports whether req's populated value differs from
	// active's, with a human message. Zero (unset) never diverges.
	Diverges(req, active domain.StoreConfig) (string, bool)
	// ApplyDefault writes this field's default into cfg when the row
	// declares one and its condition holds. No-op otherwise.
	ApplyDefault(cfg *domain.StoreConfig)
}

// Field is one typed row. T is the field's Go type (domain.PathTopology,
// time.Duration, int64, …).
//
//   - Get / Set read and write the field on a StoreConfig.
//   - Check validates a value (nil = nothing to validate).
//   - Fmt renders it for a divergence message (nil = %v).
//
// Defaulting has two mutually exclusive slots:
//   - DefaultTo — a plain value applied only when the field is its zero
//     ("if X == zero { X = DefaultTo }"). Unconditional enum defaults
//     use this; each is a non-zero enum value, so a zero DefaultTo
//     unambiguously means "no unconditional default".
//   - DefaultFn — a function of the whole config returning (value,
//     apply?). For conditional defaults (depend on another field) and
//     zero-is-meaningful promotions (PackAlignment None→Auto). Takes
//     precedence over DefaultTo.
//
// A field with neither is never defaulted.
type Field[T comparable] struct {
	FName     string
	FClass    FieldClass
	FConn     ConnBehavior
	Get       func(domain.StoreConfig) T
	Set       func(*domain.StoreConfig, T)
	Check     func(T) error
	Fmt       func(T) string
	DefaultTo T
	DefaultFn func(domain.StoreConfig) (T, bool)
}

func (f Field[T]) Name() string       { return f.FName }
func (f Field[T]) Class() FieldClass  { return f.FClass }
func (f Field[T]) Conn() ConnBehavior { return f.FConn }

func (f Field[T]) Validate(cfg domain.StoreConfig) error {
	if f.Check == nil {
		return nil
	}
	return f.Check(f.Get(cfg))
}

func (f Field[T]) Diverges(req, active domain.StoreConfig) (string, bool) {
	var zero T
	rv := f.Get(req)
	if rv == zero || rv == f.Get(active) {
		return "", false
	}
	return fmt.Sprintf("%s: requested %s, active %s", f.FName, f.render(rv), f.render(f.Get(active))), true
}

func (f Field[T]) render(v T) string {
	if f.Fmt != nil {
		return f.Fmt(v)
	}
	return fmt.Sprintf("%v", v)
}

func (f Field[T]) ApplyDefault(cfg *domain.StoreConfig) {
	if f.DefaultFn != nil {
		if v, ok := f.DefaultFn(*cfg); ok {
			f.Set(cfg, v)
		}
		return
	}
	var zero T
	if f.DefaultTo != zero && f.Get(*cfg) == zero {
		f.Set(cfg, f.DefaultTo)
	}
}
