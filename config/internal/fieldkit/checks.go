package fieldkit

import (
	"cmp"
	"fmt"

	"scrinium.dev/errs"
)

// Validator constructors. Each returns a func(T) error for a Field's
// Check slot. Zero (the Go zero value) always passes: a zero is "field
// omitted / not applicable" (a Plain store leaves crypto fields zero, a
// not-yet-defaulted config leaves everything zero). A caller opts into a
// check by setting a value.

// Enum accepts a value from a fixed set (or zero).
func Enum[T comparable](allowed ...T) func(T) error {
	return func(v T) error {
		var zero T
		if v == zero {
			return nil
		}
		for _, a := range allowed {
			if v == a {
				return nil
			}
		}
		return fmt.Errorf("%w: got %v, want one of %v", errs.ErrInvalidConfig, v, allowed)
	}
}

// MinVal enforces a lower bound (zero = off).
func MinVal[T cmp.Ordered](name string, min T) func(T) error {
	return func(v T) error {
		var zero T
		if v != zero && v < min {
			return fmt.Errorf("%w: %s=%v below minimum %v", errs.ErrInvalidConfig, name, v, min)
		}
		return nil
	}
}

// MaxVal enforces an upper bound (zero = off).
func MaxVal[T cmp.Ordered](name string, max T) func(T) error {
	return func(v T) error {
		var zero T
		if v != zero && v > max {
			return fmt.Errorf("%w: %s=%v above maximum %v", errs.ErrInvalidConfig, name, v, max)
		}
		return nil
	}
}

// RangeVal enforces [min, max] (zero = off) in one check.
func RangeVal[T cmp.Ordered](name string, min, max T) func(T) error {
	return func(v T) error {
		var zero T
		if v != zero && (v < min || v > max) {
			return fmt.Errorf("%w: %s=%v out of range [%v, %v]", errs.ErrInvalidConfig, name, v, min, max)
		}
		return nil
	}
}

// NonNegative refuses negative values (zero passes — e.g. 0 = unlimited).
func NonNegative[T cmp.Ordered](name string) func(T) error {
	return func(v T) error {
		var zero T
		if v < zero {
			return fmt.Errorf("%w: %s=%v is negative", errs.ErrInvalidConfig, name, v)
		}
		return nil
	}
}

// WithSentinel replaces a check's default ErrInvalidConfig with a
// dedicated sentinel.
func WithSentinel[T any](check func(T) error, sentinel error) func(T) error {
	return func(v T) error {
		if check(v) != nil {
			return sentinel
		}
		return nil
	}
}

// And runs several checks in order (first failure wins).
func And[T any](checks ...func(T) error) func(T) error {
	return func(v T) error {
		for _, c := range checks {
			if err := c(v); err != nil {
				return err
			}
		}
		return nil
	}
}
