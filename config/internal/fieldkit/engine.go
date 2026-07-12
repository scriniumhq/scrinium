package fieldkit

import (
	"fmt"
	"strings"

	"scrinium.dev/domain"
	"scrinium.dev/errs"
)

// The traversal engine. Every per-field operation is one loop over a
// registry ([]Desc) — validation, defaulting, class-filtered
// divergence. The registry itself (the field declarations) lives in
// package config; these functions take it as an argument so this
// package stays rule-free.

// ValidateAll checks every field's value (enum / bounds). First failure
// wins. Fields with a nil Check pass.
func ValidateAll(reg []Desc, cfg domain.StoreConfig) error {
	for _, f := range reg {
		if err := f.Validate(cfg); err != nil {
			return err
		}
	}
	return nil
}

// ApplyDefaults fills zero-valued fields from their declared defaults.
// A single forward pass: conditional defaults key off fields that
// defaulting never mutates, so order is irrelevant.
func ApplyDefaults(reg []Desc, cfg domain.StoreConfig) domain.StoreConfig {
	for _, f := range reg {
		f.ApplyDefault(&cfg)
	}
	return cfg
}

// DivergentByClass lists populated fields of the given class whose req
// value differs from active.
func DivergentByClass(reg []Desc, class FieldClass, req, active domain.StoreConfig) []string {
	var out []string
	for _, f := range reg {
		if f.Class() != class {
			continue
		}
		if msg, ok := f.Diverges(req, active); ok {
			out = append(out, msg)
		}
	}
	return out
}

// MismatchAgainstActive is DivergentByClass(ClassImmutable) wrapped in
// ErrConfigMismatch — the OpenStore/UpdateConfig immutable check.
func MismatchAgainstActive(reg []Desc, req, active domain.StoreConfig) error {
	mismatches := DivergentByClass(reg, ClassImmutable, req, active)
	if len(mismatches) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", errs.ErrConfigMismatch, strings.Join(mismatches, "; "))
}

// Rows projects a registry to (name, class, conn) triples — the shape
// the conformance tests assert against.
func Rows(reg []Desc) []Row {
	out := make([]Row, len(reg))
	for i, f := range reg {
		out[i] = Row{Name: f.Name(), Class: f.Class(), Conn: f.Conn()}
	}
	return out
}

// Row is the projection returned by Rows.
type Row struct {
	Name  string
	Class FieldClass
	Conn  ConnBehavior
}
