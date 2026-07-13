package config

import (
	"cmp"

	"scrinium.dev/config/internal/fieldkit"
)

// Thin surface over package fieldkit so the registry (registry.go) reads
// as a plain table. The machinery — how a descriptor validates, defaults
// and diverges, and how the traversal loops — lives in fieldkit and is
// never edited when adding a field. Here we only re-expose the names the
// declarations use.

// field is the typed registry row (generic type alias over
// fieldkit.Field). A declaration writes field[T]{ FName: …, … }.
type field[T comparable] = fieldkit.Field[T]

// fieldDesc is a registry row after type erasure.
type fieldDesc = fieldkit.Desc

// Field classes / connection behaviours — the vocabulary a declaration
// labels a field with.
type (
	FieldClass   = fieldkit.FieldClass
	ConnBehavior = fieldkit.ConnBehavior
)

const (
	classImmutable  = fieldkit.ClassImmutable
	classGovernance = fieldkit.ClassGovernance
	classSession    = fieldkit.ClassSession

	connRefusedImmutable  = fieldkit.ConnRefusedImmutable
	connRefusedGovernance = fieldkit.ConnRefusedGovernance
	connOverlay           = fieldkit.ConnOverlay
	connIgnored           = fieldkit.ConnIgnored
	connDerived           = fieldkit.ConnDerived
)

// Validator constructors a declaration picks from. Thin generic
// forwarders — generic funcs can't be aliased by value, so each is a
// one-line pass-through preserving fieldkit's constraints.
func enum[T comparable](allowed ...T) func(T) error          { return fieldkit.Enum(allowed...) }
func minVal[T cmp.Ordered](name string, min T) func(T) error { return fieldkit.MinVal(name, min) }
func maxVal[T cmp.Ordered](name string, max T) func(T) error { return fieldkit.MaxVal(name, max) }
func rangeVal[T cmp.Ordered](name string, min, max T) func(T) error {
	return fieldkit.RangeVal(name, min, max)
}
func nonNegative[T cmp.Ordered](name string) func(T) error { return fieldkit.NonNegative[T](name) }
func withSentinel[T any](check func(T) error, sentinel error) func(T) error {
	return fieldkit.WithSentinel(check, sentinel)
}
