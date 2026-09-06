package hostmetrics

import (
	"testing"
	"time"
)

func TestDeriveFirstSampleHasNoRates(t *testing.T) {
	tr := NewPreviousTracker()
	s := Sample{
		CollectedAt: time.Now(), MemTotalBytes: 1000, MemAvailableBytes: 400,
		DiskTotalBytes: 2000, DiskUsedBytes: 500,
	}
	d := tr.Derive("node:1", s)
	if d.RxRate != nil || d.TxRate != nil || d.CPUPercent != nil {
		t.Errorf("first-ever sample must have nil rates, got %+v", d)
	}
	if d.MemPercent == nil || *d.MemPercent != 60 {
		t.Errorf("MemPercent = %v, want 60 (mem/disk percent don't need a previous sample)", d.MemPercent)
	}
	if d.DiskPercent == nil || *d.DiskPercent != 25 {
		t.Errorf("DiskPercent = %v, want 25", d.DiskPercent)
	}
}

func TestDeriveComputesRateFromPreviousSample(t *testing.T) {
	tr := NewPreviousTracker()
	t0 := time.Now()
	tr.Derive("node:1", Sample{CollectedAt: t0, RxBytes: 1000, TxBytes: 2000})

	d := tr.Derive("node:1", Sample{CollectedAt: t0.Add(10 * time.Second), RxBytes: 6000, TxBytes: 2500})
	if d.RxRate == nil || *d.RxRate != 500 {
		t.Errorf("RxRate = %v, want 500 B/s ((6000-1000)/10s)", d.RxRate)
	}
	if d.TxRate == nil || *d.TxRate != 50 {
		t.Errorf("TxRate = %v, want 50 B/s ((2500-2000)/10s)", d.TxRate)
	}
}

func TestDeriveCounterGoingBackwardsMeansReboot(t *testing.T) {
	tr := NewPreviousTracker()
	t0 := time.Now()
	tr.Derive("node:1", Sample{CollectedAt: t0, RxBytes: 900000})

	// A rebooted host's cumulative /proc counters reset to a small number -
	// must yield nil, not a nonsensical negative-turned-huge rate.
	d := tr.Derive("node:1", Sample{CollectedAt: t0.Add(10 * time.Second), RxBytes: 100})
	if d.RxRate != nil {
		t.Errorf("RxRate after an apparent counter reset = %v, want nil", d.RxRate)
	}
}

func TestDeriveCPUPercentFromJiffies(t *testing.T) {
	tr := NewPreviousTracker()
	t0 := time.Now()
	tr.Derive("node:1", Sample{CollectedAt: t0, CPUTotalJiffies: 1000, CPUIdleJiffies: 800})

	// 500 total jiffies elapsed, 100 of them idle -> 80% busy.
	d := tr.Derive("node:1", Sample{CollectedAt: t0.Add(time.Second), CPUTotalJiffies: 1500, CPUIdleJiffies: 900})
	if d.CPUPercent == nil || *d.CPUPercent != 80 {
		t.Errorf("CPUPercent = %v, want 80", d.CPUPercent)
	}
}

func TestDeriveKeysAreIndependent(t *testing.T) {
	tr := NewPreviousTracker()
	t0 := time.Now()
	tr.Derive("node:1", Sample{CollectedAt: t0, RxBytes: 1000})
	// A totally different key's first sample must also have nil rates,
	// not accidentally pick up node:1's previous value.
	d := tr.Derive("node:2", Sample{CollectedAt: t0.Add(time.Second), RxBytes: 5})
	if d.RxRate != nil {
		t.Errorf("a different key's first sample got a rate: %+v", d)
	}
}

func TestDeriveZeroElapsedSecondsSkipsRates(t *testing.T) {
	tr := NewPreviousTracker()
	t0 := time.Now()
	tr.Derive("node:1", Sample{CollectedAt: t0, RxBytes: 1000})
	d := tr.Derive("node:1", Sample{CollectedAt: t0, RxBytes: 2000}) // same timestamp, zero elapsed
	if d.RxRate != nil {
		t.Errorf("RxRate with zero elapsed seconds = %v, want nil (division by zero guarded)", d.RxRate)
	}
}
