package tunnelhealth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// fakeProbe answers per interface from a table the test edits between rounds.
type fakeProbe struct {
	mu      sync.Mutex
	results map[string]error
	rtt     time.Duration
}

func (f *fakeProbe) set(iface string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results[iface] = err
}

func (f *fakeProbe) probe(ctx context.Context, iface string) (time.Duration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.results[iface]; err != nil {
		return 0, err
	}
	return f.rtt, nil
}

func newTestMonitor(t *testing.T, tunnels ...string) (*Monitor, *fakeProbe) {
	t.Helper()
	fp := &fakeProbe{results: map[string]error{}, rtt: 40 * time.Millisecond}
	m := New(Options{
		Probe:    fp.probe,
		Discover: func() []string { return tunnels },
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return m, fp
}

func TestUnprobedTunnelCountsAsUp(t *testing.T) {
	m, _ := newTestMonitor(t, "uk")
	if !m.Up("uk") {
		t.Error("a tunnel that has never been probed must count as up, or a fresh start sends everyone through the fallback")
	}
	if !m.Up("never-heard-of-it") {
		t.Error("an unknown name is also optimistic")
	}
}

func TestFirstObservationIsTakenAtFaceValue(t *testing.T) {
	m, fp := newTestMonitor(t, "uk", "sweden")
	fp.set("sweden", errors.New("i/o timeout"))
	m.Tick(context.Background())

	if !m.Up("uk") {
		t.Error("uk answered its first probe, want up")
	}
	if m.Up("sweden") {
		t.Error("sweden failed its first probe, want down straight away - no debounce on the first look")
	}
}

func TestUpTunnelNeedsConsecutiveFailuresToGoDown(t *testing.T) {
	m, fp := newTestMonitor(t, "uk")
	ctx := context.Background()
	m.Tick(ctx)

	fp.set("uk", errors.New("i/o timeout"))
	m.Tick(ctx)
	if !m.Up("uk") {
		t.Fatal("one failed round must not take a tunnel down")
	}
	m.Tick(ctx)
	if m.Up("uk") {
		t.Fatal("two consecutive failed rounds must take it down")
	}
}

func TestOneGoodRoundBetweenFailuresResetsTheCount(t *testing.T) {
	m, fp := newTestMonitor(t, "uk")
	ctx := context.Background()
	m.Tick(ctx)

	fp.set("uk", errors.New("i/o timeout"))
	m.Tick(ctx)
	fp.set("uk", nil)
	m.Tick(ctx)
	fp.set("uk", errors.New("i/o timeout"))
	m.Tick(ctx)
	if !m.Up("uk") {
		t.Fatal("failures separated by a good round are not consecutive, tunnel must stay up")
	}
}

func TestMissingInterfaceIsDownImmediately(t *testing.T) {
	m, fp := newTestMonitor(t, "italy")
	ctx := context.Background()
	m.Tick(ctx)
	if !m.Up("italy") {
		t.Fatal("setup: italy should start up")
	}

	fp.set("italy", ErrMissing)
	m.Tick(ctx)
	if m.Up("italy") {
		t.Fatal("wg-quick down removes the interface - that has to be noticed in one round, not two")
	}
	st, _ := m.Status("italy")
	if st.Present {
		t.Errorf("Present = true for a missing interface: %+v", st)
	}
}

func TestDownTunnelNeedsSeveralGoodRoundsToRecover(t *testing.T) {
	m, fp := newTestMonitor(t, "uk")
	ctx := context.Background()
	fp.set("uk", errors.New("down"))
	m.Tick(ctx)
	if m.Up("uk") {
		t.Fatal("setup: uk should start down")
	}

	fp.set("uk", nil)
	m.Tick(ctx)
	m.Tick(ctx)
	if m.Up("uk") {
		t.Fatal("two good rounds are not enough - a flapping tunnel must not bounce users")
	}
	m.Tick(ctx)
	if !m.Up("uk") {
		t.Fatal("three good rounds must bring it back")
	}
}

func TestWatchedInterfaceIsProbedButNotReported(t *testing.T) {
	m, fp := newTestMonitor(t, "uk")
	fp.set("germany", ErrMissing)
	m.Watch([]string{"germany", ""})
	m.Tick(context.Background())

	if m.Up("germany") {
		t.Error("a watched interface that does not exist is down - the fallback supervisor needs to know")
	}
	health := m.Health()
	if _, ok := health["germany"]; ok {
		t.Error("germany lives on another server: reporting it here would show a permanent false outage")
	}
	if h, ok := health["uk"]; !ok || !h.Up || !h.Present {
		t.Errorf("uk is this host's own tunnel and must be reported up: %+v", health)
	}
}

func TestHealthCarriesProbeLatencyOnlyWhileUp(t *testing.T) {
	m, fp := newTestMonitor(t, "uk", "sweden")
	fp.set("sweden", errors.New("i/o timeout"))
	m.Tick(context.Background())

	health := m.Health()
	if h := health["uk"]; h.ProbeMs == nil || *h.ProbeMs != 40 {
		t.Errorf("uk probe = %v, want 40ms", h.ProbeMs)
	}
	if h := health["sweden"]; h.ProbeMs != nil || h.Error != "i/o timeout" {
		t.Errorf("sweden = %+v, want no latency and the probe's error", h)
	}
}

func TestTunnelsThatDisappearFromDiscoveryAreForgotten(t *testing.T) {
	names := []string{"uk", "sweden"}
	fp := &fakeProbe{results: map[string]error{}, rtt: time.Millisecond}
	m := New(Options{
		Probe:    fp.probe,
		Discover: func() []string { return names },
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	m.Tick(context.Background())
	names = []string{"uk"}
	m.Tick(context.Background())

	if _, ok := m.Status("sweden"); ok {
		t.Error("a tunnel nobody watches or configures any more must be dropped, not linger as stale state")
	}
}

func TestRunStopsWhenContextIsCancelled(t *testing.T) {
	m, _ := newTestMonitor(t, "uk")
	m.opts.Interval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()

	time.Sleep(50 * time.Millisecond)
	if _, ok := m.Status("uk"); !ok {
		t.Error("Run must probe immediately and on every tick")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}
