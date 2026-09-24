// Package tunnelhealth decides whether each WireGuard exit on this host is
// actually carrying traffic. Two consumers depend on that verdict: the node's
// fallback supervisor (switch an exit to a plain direct connection while its
// tunnel is dead) and the monitoring report the panel shows.
//
// A link flag cannot answer the question - a WireGuard device reports
// operstate "unknown" whether its peer is alive or long gone, and an idle
// tunnel without PersistentKeepalive has no recent handshake even when it is
// perfectly healthy. So each tunnel is probed: a real TCP connection is
// opened through that interface, and only a connection that completes counts.
package tunnelhealth

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/legendary1205/rapido-go/internal/hostmetrics"
)

// ErrMissing is the probe result for an interface that does not exist.
var ErrMissing = errors.New("interface not found")

// Probe opens one connection through iface and returns how long it took.
type Probe func(ctx context.Context, iface string) (time.Duration, error)

type Options struct {
	// Probe defaults to DialProbe (Linux only).
	Probe Probe
	// Discover returns the tunnels to always probe and report. Defaults to
	// hostmetrics.TunnelNames: every configured tunnel plus every WireGuard
	// interface that exists.
	Discover func() []string
	// Interval between rounds. Default 5s.
	Interval time.Duration
	// DownAfter is how many consecutive failed rounds turn an up tunnel
	// down. Default 2 - a single dropped probe must not move users. A
	// missing interface skips this and is down at once.
	DownAfter int
	// UpAfter is how many consecutive good rounds turn a down tunnel up.
	// Default 3, so a tunnel that flaps does not bounce users between the
	// tunnel and the fallback every few seconds.
	UpAfter int
	// ProbeTimeout bounds one probe. Default 4s.
	ProbeTimeout time.Duration
	Logger       *slog.Logger
	Now          func() time.Time
}

// Status is the current verdict for one tunnel.
type Status struct {
	Name string
	// Present is whether the network interface exists right now.
	Present bool
	Up      bool
	// Probed is false until the first round has completed for this tunnel.
	Probed    bool
	RTT       time.Duration
	Error     string
	CheckedAt time.Time
	// Since is when Up last changed.
	Since time.Time
}

type entry struct {
	Status
	okStreak   int
	failStreak int
}

type Monitor struct {
	opts Options

	mu      sync.RWMutex
	entries map[string]*entry
	watched map[string]bool
}

func New(o Options) *Monitor {
	if o.Probe == nil {
		o.Probe = DialProbe
	}
	if o.Discover == nil {
		o.Discover = hostmetrics.TunnelNames
	}
	if o.Interval <= 0 {
		o.Interval = 5 * time.Second
	}
	if o.DownAfter <= 0 {
		o.DownAfter = 2
	}
	if o.UpAfter <= 0 {
		o.UpAfter = 3
	}
	if o.ProbeTimeout <= 0 {
		o.ProbeTimeout = 4 * time.Second
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Monitor{opts: o, entries: make(map[string]*entry), watched: make(map[string]bool)}
}

// Watch sets the interfaces to probe in addition to the discovered ones -
// the ones the running config binds outbounds to. It replaces the previous
// set. A watched interface that does not exist is simply reported down.
func (m *Monitor) Watch(names []string) {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		if n != "" {
			set[n] = true
		}
	}
	m.mu.Lock()
	m.watched = set
	m.mu.Unlock()
}

// Run probes every interval until ctx is done. The first round runs
// immediately so a fresh process has a real verdict within seconds.
func (m *Monitor) Run(ctx context.Context) {
	m.Tick(ctx)
	t := time.NewTicker(m.opts.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.Tick(ctx)
		}
	}
}

// Tick runs one probe round over every tracked tunnel and waits for it.
func (m *Monitor) Tick(ctx context.Context) {
	names := m.trackedNames()

	type result struct {
		name string
		rtt  time.Duration
		err  error
	}
	results := make(chan result, len(names))
	for _, name := range names {
		go func(name string) {
			pctx, cancel := context.WithTimeout(ctx, m.opts.ProbeTimeout)
			defer cancel()
			rtt, err := m.opts.Probe(pctx, name)
			results <- result{name, rtt, err}
		}(name)
	}
	for range names {
		r := <-results
		m.record(r.name, r.rtt, r.err)
	}

	keep := make(map[string]bool, len(names))
	for _, n := range names {
		keep[n] = true
	}
	m.mu.Lock()
	for n := range m.entries {
		if !keep[n] {
			delete(m.entries, n)
		}
	}
	m.mu.Unlock()
}

func (m *Monitor) trackedNames() []string {
	set := make(map[string]bool)
	for _, n := range m.opts.Discover() {
		set[n] = true
	}
	m.mu.RLock()
	for n := range m.watched {
		set[n] = true
	}
	m.mu.RUnlock()
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (m *Monitor) record(name string, rtt time.Duration, err error) {
	now := m.opts.Now()
	m.mu.Lock()
	defer m.mu.Unlock()

	e := m.entries[name]
	first := e == nil
	if first {
		e = &entry{Status: Status{Name: name, Since: now}}
		m.entries[name] = e
	}
	wasUp := e.Up
	e.CheckedAt = now

	if err == nil {
		e.Present = true
		e.RTT = rtt
		e.Error = ""
		e.okStreak++
		e.failStreak = 0
		if first || (!e.Up && e.okStreak >= m.opts.UpAfter) {
			e.Up = true
		}
	} else {
		missing := errors.Is(err, ErrMissing)
		e.Present = !missing
		e.RTT = 0
		e.Error = err.Error()
		e.failStreak++
		e.okStreak = 0
		if first || missing || (e.Up && e.failStreak >= m.opts.DownAfter) {
			e.Up = false
		}
	}
	e.Probed = true

	if e.Up != wasUp || first {
		e.Since = now
	}
	if !first && e.Up != wasUp {
		if e.Up {
			m.opts.Logger.Info("wireguard tunnel recovered", "tunnel", name)
		} else {
			m.opts.Logger.Warn("wireguard tunnel is down", "tunnel", name, "error", e.Error)
		}
	}
}

// Up reports whether the tunnel is usable. A tunnel that has never been
// probed counts as up: acting on a guess would make a fresh start send
// everyone through the fallback before the first probe has even returned.
func (m *Monitor) Up(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.entries[name]
	if !ok || !e.Probed {
		return true
	}
	return e.Up
}

func (m *Monitor) Status(name string) (Status, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.entries[name]
	if !ok {
		return Status{}, false
	}
	return e.Status, true
}

// Health returns the verdicts for exactly the tunnels this host is set up
// for (configured or currently existing) in the shape hostmetrics reports.
// Tunnels that are merely watched because a config binds an outbound to
// them - every node is sent every exit in the fleet, most of which live on
// other servers - are not reported: they would show up as permanently down.
func (m *Monitor) Health() map[string]hostmetrics.TunnelHealth {
	own := make(map[string]bool)
	for _, n := range m.opts.Discover() {
		own[n] = true
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]hostmetrics.TunnelHealth, len(own))
	for name, e := range m.entries {
		if !own[name] || !e.Probed {
			continue
		}
		h := hostmetrics.TunnelHealth{Present: e.Present, Up: e.Up, Error: e.Error}
		if e.Up && e.RTT > 0 {
			ms := float64(e.RTT.Microseconds()) / 1000
			h.ProbeMs = &ms
		}
		out[name] = h
	}
	return out
}
