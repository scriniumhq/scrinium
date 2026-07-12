package declarative

import (
	"fmt"
	"sort"
	"strings"

	"scrinium.dev/domain"
	"scrinium.dev/errs"
)

// The YAML↔domain vocabulary: the ONE dictionary both the mapping
// (StoreConfigFromPolicy) and the file validation (validatePolicy)
// read. A word added here is simultaneously accepted by the parser's
// validator and translated by the mapper — so the two cannot drift.
var (
	deletionPolicyVocab = map[string]domain.DeletionPolicy{
		"free":      domain.DeletionPolicyFree,
		"retention": domain.DeletionPolicyRetention,
		"noDelete":  domain.DeletionPolicyNoDelete,
	}
	encryptionModeVocab = map[string]domain.ManifestCrypto{
		"":         "", // defaulted to sealed by the mapper
		"sealed":   domain.ManifestCryptoSealed,
		"paranoid": domain.ManifestCryptoParanoid,
	}
	dedupVocab = map[string]domain.EncryptedDedup{
		"":           "", // defaulted to disabled by the mapper
		"disabled":   domain.EncryptedDedupDisabled,
		"convergent": domain.EncryptedDedupConvergent,
	}
)

// vocabWords renders a dictionary's accepted words for an error
// message, deterministically ordered, empty word omitted.
func vocabWords[V any](m map[string]V) string {
	words := make([]string, 0, len(m))
	for w := range m {
		if w != "" {
			words = append(words, w)
		}
	}
	sort.Strings(words)
	return "{" + strings.Join(words, ", ") + "}"
}

// GuardUnsupportedPolicy rejects policy features whose components are
// not wired yet, with a precise pointer to the landing chunk.
func GuardUnsupportedPolicy(p *Policy) error {
	if p == nil {
		return nil
	}
	switch {
	case p.Chunking != nil:
		return fmt.Errorf("scrinium: chunking is not wired yet (M5/C3): %w", errs.ErrNotImplemented)
	case p.Bundling != nil:
		return fmt.Errorf("scrinium: bundling is not wired yet (M4/S4): %w", errs.ErrNotImplemented)
	case len(p.Pipeline) > 0 || len(p.PipelineExtra) > 0:
		return fmt.Errorf("scrinium: explicit pipeline assembly is not wired yet: %w", errs.ErrNotImplemented)
	}
	return nil
}

func StoreConfigFromPolicy(p *Policy) (domain.StoreConfig, bool) {
	var cfg domain.StoreConfig
	if p == nil {
		return cfg, false
	}

	encrypted := p.Encryption != nil
	if encrypted {
		if v, ok := encryptionModeVocab[p.Encryption.Mode]; ok && v != "" {
			cfg.ManifestCrypto = v
		} else {
			cfg.ManifestCrypto = domain.ManifestCryptoSealed // "" and "sealed" default here
		}
		if v, ok := dedupVocab[p.Encryption.Dedup]; ok && v != "" {
			cfg.EncryptedDedup = v
		} else {
			cfg.EncryptedDedup = domain.EncryptedDedupDisabled
		}
		if p.Encryption.SegmentSize > 0 {
			cfg.SegmentSize = int(p.Encryption.SegmentSize.Int64())
		}
	}

	if v, ok := deletionPolicyVocab[p.DeletionPolicy]; ok {
		cfg.DeletionPolicy = v
	}
	if p.Retention != 0 {
		cfg.RetentionPeriod = p.Retention.Std()
	}
	if p.MaxArtifactSize > 0 {
		cfg.MaxArtifactSize = int64(p.MaxArtifactSize)
	}

	return cfg, encrypted
}
