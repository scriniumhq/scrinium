package metrics

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
	"math"
)

// Results aggregates computed stream metrics.
// Zero-value fields indicate unrequested metrics.
type Results struct {
	Hash    string
	Entropy float64
	Size    int64
}

// Pipeline routes a byte stream through active metric calculators.
type Pipeline struct {
	writers     []io.Writer
	hashCalc    hash.Hash
	entropyCalc *entropyCalc
	size        int64
}

type Option func(*Pipeline)

func WithSHA256() Option {
	return func(p *Pipeline) {
		p.hashCalc = sha256.New()
		p.writers = append(p.writers, p.hashCalc)
	}
}

func WithEntropy() Option {
	return func(p *Pipeline) {
		p.entropyCalc = &entropyCalc{}
		p.writers = append(p.writers, p.entropyCalc)
	}
}

func NewPipeline(opts ...Option) *Pipeline {
	p := &Pipeline{}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

func (p *Pipeline) Write(b []byte) (int, error) {
	p.size += int64(len(b))
	for _, w := range p.writers {
		if _, err := w.Write(b); err != nil {
			return 0, err
		}
	}
	return len(b), nil
}

// Results finalizes the state and returns the aggregated metrics.
func (p *Pipeline) Results() *Results {
	res := &Results{Size: p.size}
	if p.hashCalc != nil {
		res.Hash = hex.EncodeToString(p.hashCalc.Sum(nil))
	}
	if p.entropyCalc != nil {
		rawEntropy := p.entropyCalc.calculate()
		// Round to one decimal place (tenths) - enough for decisions.
		res.Entropy = math.Round(rawEntropy*10) / 10
	}
	return res
}

type entropyCalc struct {
	counts [256]uint64
	total  int64
}

func (c *entropyCalc) Write(p []byte) (int, error) {
	n := len(p)
	c.total += int64(n)

	for len(p) >= 4 {
		_ = p[3] // BCE hint
		c.counts[p[0]]++
		c.counts[p[1]]++
		c.counts[p[2]]++
		c.counts[p[3]]++
		p = p[4:]
	}

	for _, b := range p {
		c.counts[b]++
	}

	return n, nil
}

func (c *entropyCalc) calculate() float64 {
	if c.total == 0 {
		return 0
	}

	var ent float64
	invTotal := 1.0 / float64(c.total)

	for _, count := range c.counts {
		if count > 0 {
			pr := float64(count) * invTotal
			ent -= pr * math.Log2(pr)
		}
	}
	return ent
}
