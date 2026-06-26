package domain_test

import (
	"testing"

	"scrinium.dev/domain"
)

func TestClassifyStoreOwnership(t *testing.T) {
	cases := []struct {
		name                    string
		recorded, authoritative string
		want                    domain.StoreOwnership
	}{
		{"matching", "store-X", "store-X", domain.StoreOwnershipOwn},
		{"different", "store-Y", "store-X", domain.StoreOwnershipForeign},
		{"recorded empty", "", "store-X", domain.StoreOwnershipUnknown},
		{"authoritative empty", "store-Y", "", domain.StoreOwnershipUnknown},
		{"both empty", "", "", domain.StoreOwnershipUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.ClassifyStoreOwnership(tc.recorded, tc.authoritative); got != tc.want {
				t.Errorf("ClassifyStoreOwnership(%q, %q) = %v, want %v",
					tc.recorded, tc.authoritative, got, tc.want)
			}
		})
	}
}
