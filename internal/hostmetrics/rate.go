package hostmetrics

import "sync"

// Derived is what actually gets stored in a host_metrics row - the
// percentages/rates the Monitoring page renders, computed from a Sample's
// raw cumulative counters plus whatever was seen last time for this same
// key. A nil field means "nothing to compare against yet" (first sample
// for this key, or the counter went backwards - a reboot, not a real
// negative rate) - matches Python's _rate/_cpu_percent both returning None
// in exactly those cases, rather than a fabricated number.
type Derived struct {
	CPUPercent  *float64
	MemPercent  *float64
	DiskPercent *float64
	RxRate      *int64 // bytes/sec
	TxRate      *int64 // bytes/sec
}

type previous struct {
	sample Sample
}

// PreviousTracker holds the last raw Sample seen per key (a node's id as a
// string, or a fixed "panel" key for the panel's own self-sample) purely
// in memory - mirrors Python's collect_metrics.py module-level `_previous`
// dict exactly, including its lifetime: it resets on every process
// restart, so the first sample after a restart always yields nil rates,
// never a fabricated spike.
//
// This only produces correct rates when exactly one process ever calls
// Derive for a given key - see the node-report ingestion endpoint's own
// doc comment for why it must be pointed at the backend-singleton
// instance specifically, not a load-balanced pool of API replicas.
type PreviousTracker struct {
	mu   sync.Mutex
	byID map[string]previous
}

func NewPreviousTracker() *PreviousTracker {
	return &PreviousTracker{byID: make(map[string]previous)}
}

func (t *PreviousTracker) Derive(key string, s Sample) Derived {
	t.mu.Lock()
	prev, hadPrev := t.byID[key]
	t.byID[key] = previous{sample: s}
	t.mu.Unlock()

	var d Derived
	if s.MemTotalBytes > 0 {
		pct := round1(100 * float64(s.MemTotalBytes-s.MemAvailableBytes) / float64(s.MemTotalBytes))
		d.MemPercent = &pct
	}
	if s.DiskTotalBytes > 0 {
		pct := round1(100 * float64(s.DiskUsedBytes) / float64(s.DiskTotalBytes))
		d.DiskPercent = &pct
	}
	if !hadPrev {
		return d
	}

	seconds := s.CollectedAt.Sub(prev.sample.CollectedAt).Seconds()
	if seconds <= 0 {
		return d
	}

	if rate, ok := rateOf(s.RxBytes, prev.sample.RxBytes, seconds); ok {
		d.RxRate = &rate
	}
	if rate, ok := rateOf(s.TxBytes, prev.sample.TxBytes, seconds); ok {
		d.TxRate = &rate
	}

	dt := int64(s.CPUTotalJiffies) - int64(prev.sample.CPUTotalJiffies)
	di := int64(s.CPUIdleJiffies) - int64(prev.sample.CPUIdleJiffies)
	if dt > 0 {
		pct := round1(100 * float64(dt-di) / float64(dt))
		if pct < 0 {
			pct = 0
		}
		if pct > 100 {
			pct = 100
		}
		d.CPUPercent = &pct
	}
	return d
}

// rateOf mirrors Python's _rate: nil (via ok=false) when the counter went
// backwards (a reboot resetting cumulative counters to zero), never a
// fabricated negative-turned-huge value.
func rateOf(now, prev int64, seconds float64) (int64, bool) {
	delta := now - prev
	if delta < 0 {
		return 0, false
	}
	return int64(float64(delta) / seconds), true
}

func round1(v float64) float64 {
	return float64(int64(v*10+0.5)) / 10
}
