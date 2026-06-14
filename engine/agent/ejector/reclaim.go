package ejector

import (
	"os"
	"path/filepath"
	"time"
)

// reclaim removes idle (zero-holder) and over-lifetime entries; returns
// eviction counts per reason. File removal happens outside the lock.
func (a *ejectorAgent) reclaim(now time.Time) map[string]int {
	type victim struct{ ch, path, reason string }
	a.mu.Lock()
	var vs []victim
	for ch, e := range a.byHash {
		switch {
		case a.cfg.MaxLifetime > 0 && now.Sub(e.extractedAt) >= a.cfg.MaxLifetime:
			vs = append(vs, victim{ch, e.path, "lifetime"})
		case e.holders == 0 && a.cfg.KeepAliveIdle > 0 && now.Sub(e.lastAccess) >= a.cfg.KeepAliveIdle:
			vs = append(vs, victim{ch, e.path, "idle"})
		}
	}
	for _, v := range vs {
		delete(a.byHash, v.ch)
		a.dropReqLocked(v.ch)
	}
	a.mu.Unlock()
	counts := map[string]int{}
	for _, v := range vs {
		if err := os.Remove(v.path); err != nil {
			a.Logger().Debug("ejector: scratch remove on reclaim failed", "path", v.path, "err", err)
		}
		counts[v.reason]++
	}
	return counts
}

// sizeCapEvict enforces MaxScratchBytes by evicting zero-holder entries
// oldest-first. Held entries are never evicted (backpressure).
func (a *ejectorAgent) sizeCapEvict() {
	if a.cfg.MaxScratchBytes <= 0 {
		return
	}
	a.mu.Lock()
	var total int64
	for _, e := range a.byHash {
		total += e.size
	}
	var removed []string
	for total > a.cfg.MaxScratchBytes {
		var victimCh string
		var victim *entry
		for ch, e := range a.byHash {
			if e.holders != 0 {
				continue
			}
			if victim == nil || e.lastAccess.Before(victim.lastAccess) {
				victim, victimCh = e, ch
			}
		}
		if victim == nil {
			break // everything held
		}
		total -= victim.size
		removed = append(removed, victim.path)
		delete(a.byHash, victimCh)
		a.dropReqLocked(victimCh)
	}
	a.mu.Unlock()
	for _, p := range removed {
		if err := os.Remove(p); err != nil {
			a.Logger().Debug("ejector: scratch remove under size pressure failed", "path", p, "err", err)
		}
	}
	if n := len(removed); n > 0 {
		a.emitCleanup(map[string]int{"pressure": n})
	}
}

// dropReqLocked removes byReq memo entries pointing at ch. Caller holds mu.
func (a *ejectorAgent) dropReqLocked(ch string) {
	for rk, v := range a.byReq {
		if v == ch {
			delete(a.byReq, rk)
		}
	}
}

// sweepOnOpen removes any files left in TempDir by a previous instance
// (safe: scratch is reproducible).
func (a *ejectorAgent) sweepOnOpen() {
	ents, err := os.ReadDir(a.cfg.TempDir)
	if err != nil {
		return
	}
	for _, de := range ents {
		p := filepath.Join(a.cfg.TempDir, de.Name())
		if err := os.Remove(p); err != nil {
			a.Logger().Debug("ejector: open-sweep remove failed", "path", p, "err", err)
		}
	}
}
