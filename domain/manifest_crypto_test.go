package domain_test

import (
	"encoding/json"
	"testing"

	"scrinium.dev/domain"
)

// --- Current names ---

func TestManifestCrypto_UnmarshalJSON_AcceptsCurrentNames(t *testing.T) {
	cases := []struct {
		in   string
		want domain.ManifestCrypto
	}{
		{`""`, ""},
		{`"Plain"`, domain.ManifestCryptoPlain},
		{`"Sealed"`, domain.ManifestCryptoSealed},
		{`"Paranoid"`, domain.ManifestCryptoParanoid},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			var c domain.ManifestCrypto
			if err := json.Unmarshal([]byte(tc.in), &c); err != nil {
				t.Fatalf("Unmarshal %s: %v", tc.in, err)
			}
			if c != tc.want {
				t.Errorf("got %q, want %q", c, tc.want)
			}
		})
	}
}

// --- Marshal always writes current names ---

func TestManifestCrypto_MarshalJSON_WritesCurrentNames(t *testing.T) {
	cases := []struct {
		in   domain.ManifestCrypto
		want string
	}{
		{domain.ManifestCryptoPlain, `"Plain"`},
		{domain.ManifestCryptoSealed, `"Sealed"`},
		{domain.ManifestCryptoParanoid, `"Paranoid"`},
	}
	for _, tc := range cases {
		t.Run(string(tc.in), func(t *testing.T) {
			out, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if string(out) != tc.want {
				t.Errorf("got %s, want %s", out, tc.want)
			}
		})
	}
}

// --- Round-trip ---

func TestManifestCrypto_RoundTrip_AllNames(t *testing.T) {
	originals := []domain.ManifestCrypto{
		domain.ManifestCryptoPlain,
		domain.ManifestCryptoSealed,
		domain.ManifestCryptoParanoid,
	}
	for _, orig := range originals {
		t.Run(string(orig), func(t *testing.T) {
			raw, err := json.Marshal(orig)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			var got domain.ManifestCrypto
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if got != orig {
				t.Errorf("round-trip: got %q, want %q", got, orig)
			}
		})
	}
}

// --- Rejection of unknown values ---

func TestManifestCrypto_UnmarshalJSON_RejectsUnknown(t *testing.T) {
	cases := []string{
		`"Garbage"`,
		`"sealed"`,    // case-sensitive: lowercase is not accepted
		`"Encrypted"`, // plausible-looking but invalid
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			var c domain.ManifestCrypto
			err := json.Unmarshal([]byte(in), &c)
			if err == nil {
				t.Fatalf("Unmarshal %s: expected error, got %q", in, c)
			}
		})
	}
}
