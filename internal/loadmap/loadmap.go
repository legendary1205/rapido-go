// Package loadmap turns the live per-port connection counts the nodes report
// (Redis keys presence:node:<id>:ports, written by the node-live endpoint) into
// a per-host "how crowded is this config" number: connections on the host's
// port on the node(s) that serve it, as a percent of a configured capacity.
//
// Everything here is read-only and best-effort. The subscription endpoint is
// the hottest path of the panel, so a lookup is a map read on an immutable
// snapshot that is rebuilt at most once every few seconds and never blocks a
// request on Redis, Postgres or DNS after the first build. Any failure
// (Redis down, no node reporting, DNS not resolved yet) shows up as "unknown"
// for the affected hosts and callers simply render nothing.
package loadmap

import (
	"context"
	"log/slog"
	"math"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Level is the coarse crowdedness bucket of a config.
type Level string

const (
	LevelUnknown Level = "unknown"
	LevelFree    Level = "free"
	LevelNormal  Level = "normal"
	LevelBusy    Level = "busy"
	LevelFull    Level = "full"
)

const (
	// DefaultCapacity is the number of concurrent connections that counts as
	// 100% when CONFIG_LOAD_CAPACITY is not set.
	DefaultCapacity = 1000

	// DefaultTTL is how long one snapshot is served before the next request
	// triggers a rebuild.
	DefaultTTL = 3 * time.Second

	// maxStale bounds how old a snapshot may be while it is still handed out
	// (and refreshed in the background). Beyond it - a panel that saw no
	// subscription traffic for a while - the caller waits for a fresh one
	// instead of showing minutes-old numbers; node presence itself expires
	// after 20 s.
	maxStale = 10 * time.Second

	// buildTimeout caps one rebuild (two small Postgres reads plus one Redis
	// pipeline). It runs detached from any request context.
	buildTimeout = 2 * time.Second
)

// LevelFor buckets a percent: free < 40, normal < 70, busy < 90, full >= 90.
func LevelFor(percent int) Level {
	switch {
	case percent < 40:
		return LevelFree
	case percent < 70:
		return LevelNormal
	case percent < 90:
		return LevelBusy
	default:
		return LevelFull
	}
}

// Emoji is the traffic-light shown next to a config name; empty for unknown.
func (l Level) Emoji() string {
	switch l {
	case LevelFree:
		return "🟢"
	case LevelNormal:
		return "🟡"
	case LevelBusy:
		return "🟠"
	case LevelFull:
		return "🔴"
	default:
		return ""
	}
}

// Percent is clamp(round(100 * conns / capacity), 0, 100). A capacity below 1
// falls back to DefaultCapacity so a bad setting can never divide by zero.
func Percent(conns float64, capacity int) int {
	if capacity < 1 {
		capacity = DefaultCapacity
	}
	p := int(math.Round(100 * conns / float64(capacity)))
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}

// Node is the part of a nodes row the mapping needs.
type Node struct {
	ID      int32
	Address string // an IP, or a name (resolved like a host address)
	Status  string
	// InboundTags / ListenPorts restrict what the node serves (see the node
	// profile columns); empty means everything.
	InboundTags []string
	ListenPorts []int32
}

// Host is the part of a hosts row the mapping needs.
type Host struct {
	ID         int32
	Remark     string // the raw template, not formatted
	Address    string
	Port       int // 0 when the host has no port
	InboundTag string
}

// Inventory is where nodes and hosts come from (Postgres in production).
// Hosts must already exclude disabled ones, in display (priority, id) order.
type Inventory interface {
	Nodes(ctx context.Context) ([]Node, error)
	Hosts(ctx context.Context) ([]Host, error)
}

// NodePresence is one node's last node-live report as it sits in Redis.
type NodePresence struct {
	// Reporting is false when the node's presence keys are gone (old node
	// binary, node down, key expired): its connection counts are unknown.
	Reporting bool
	Ports     map[int]int // open connections per listening port, only ports > 0
}

// PresenceData is everything read from Redis for one snapshot.
type PresenceData struct {
	// Live mirrors presence:live: at least one node sent a report recently.
	Live  bool
	Nodes map[int32]NodePresence
}

// Presence reads the nodes' live connection counts.
type Presence interface {
	Read(ctx context.Context, nodeIDs []int32) (PresenceData, error)
}

// HostLoad is the computed load of one real (non-info) config.
type HostLoad struct {
	HostID  int32
	Remark  string // raw template
	Address string
	Port    int

	// Known is false when there is no usable data for this host.
	Known   bool
	Conns   int // rounded; the mean over NodeIDs when several serve the host
	Percent int
	Level   Level
	NodeIDs []int32 // nodes the number was computed from (do not modify)

	// UsesLoad reports whether the remark template already contains a
	// {LOAD...} variable and is therefore used as written.
	UsesLoad bool
}

// Snapshot is an immutable view of the fleet's load at one instant. A nil
// *Snapshot is valid and knows nothing.
type Snapshot struct {
	At       time.Time
	Live     bool
	Capacity int
	// Hosts holds every enabled, non-info host in display order, known or not.
	Hosts []HostLoad

	byID  map[int32]int
	known bool
}

// For returns the entry of a host, or nil when the host is not part of the
// snapshot (an info host, disabled, or created after it was built).
func (s *Snapshot) For(hostID int32) *HostLoad {
	if s == nil {
		return nil
	}
	i, ok := s.byID[hostID]
	if !ok {
		return nil
	}
	return &s.Hosts[i]
}

// HasData reports whether at least one host has a known load.
func (s *Snapshot) HasData() bool { return s != nil && s.known }

// IsInfo reports whether a remark template is an info pseudo-host: it holds a
// {VARIABLE} but none of the load ones. Such hosts (e.g. "🛜 {DATA_LEFT} 🛜") are
// not configs a client connects through, so they never get a load.
func IsInfo(remark string) bool {
	return strings.IndexByte(remark, '{') >= 0 && !UsesLoadVariable(remark)
}

// UsesLoadVariable reports whether a template mentions {LOAD}, {LOAD_EMOJI},
// {LOAD_PERCENT} or {LOAD_LEVEL}.
func UsesLoadVariable(s string) bool { return strings.Contains(s, "{LOAD") }

// Resolver is the subset of *net.Resolver the address mapping needs, so tests
// can inject a fake.
type Resolver interface {
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// Config tunes a Map. Zero values pick the production defaults.
type Config struct {
	Capacity int
	TTL      time.Duration
	Resolver Resolver
	// ExpandAddress rewrites a host address before it is resolved (for the
	// panel's own {SERVER_IP} variable); an address that still holds a "{"
	// afterwards is not resolved at all. Nil leaves addresses as they are.
	ExpandAddress func(string) string
	Logger        *slog.Logger
	Now           func() time.Time // for tests
}

// Map builds and caches Snapshots.
type Map struct {
	cfg  Config
	inv  Inventory
	pres Presence
	dns  *dnsCache

	snap atomic.Pointer[Snapshot]

	mu     sync.Mutex
	flight *flight
}

type flight struct {
	done chan struct{}
}

// New returns a Map reading nodes/hosts from inv and connection counts from
// pres. It starts no goroutines until a snapshot is requested.
func New(cfg Config, inv Inventory, pres Presence) *Map {
	if cfg.Capacity < 1 {
		cfg.Capacity = DefaultCapacity
	}
	if cfg.TTL <= 0 {
		cfg.TTL = DefaultTTL
	}
	if cfg.Resolver == nil {
		cfg.Resolver = net.DefaultResolver
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	return &Map{cfg: cfg, inv: inv, pres: pres, dns: newDNSCache(cfg.Resolver, cfg.Now)}
}

// Capacity is the connection count that reads as 100%.
func (m *Map) Capacity() int { return m.cfg.Capacity }

// Snapshot returns the current load view. It is a single atomic load while the
// cached one is fresh; a stale one is still returned immediately while exactly
// one background rebuild runs. Only when nothing usable exists yet (first call,
// or no traffic for a while) does the caller wait for that one rebuild, and
// never longer than its own context allows. It never returns nil.
func (m *Map) Snapshot(ctx context.Context) *Snapshot {
	now := m.cfg.Now()
	s := m.snap.Load()
	if s != nil {
		age := now.Sub(s.At)
		if age < m.cfg.TTL {
			return s
		}
		if age < maxStale {
			m.startRefresh()
			return s
		}
	}
	f := m.startRefresh()
	select {
	case <-f.done:
	case <-ctx.Done():
	}
	if s = m.snap.Load(); s != nil {
		return s
	}
	return emptySnapshot(m.cfg.Capacity, now)
}

// startRefresh joins the running rebuild or starts one (single-flight).
func (m *Map) startRefresh() *flight {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.flight != nil {
		return m.flight
	}
	f := &flight{done: make(chan struct{})}
	m.flight = f
	go func() {
		defer func() {
			m.mu.Lock()
			m.flight = nil
			m.mu.Unlock()
			close(f.done)
		}()
		ctx, cancel := context.WithTimeout(context.Background(), buildTimeout)
		defer cancel()
		m.snap.Store(m.safeBuild(ctx))
	}()
	return f
}

// safeBuild keeps a panicking Inventory/Presence from wedging the single
// flight: a snapshot must always come out, even if it knows nothing.
func (m *Map) safeBuild(ctx context.Context) (s *Snapshot) {
	defer func() {
		if r := recover(); r != nil {
			m.cfg.Logger.Error("load map: build panicked", "panic", r)
			s = emptySnapshot(m.cfg.Capacity, m.cfg.Now())
		}
	}()
	return m.build(ctx)
}

func emptySnapshot(capacity int, at time.Time) *Snapshot {
	return &Snapshot{At: at, Capacity: capacity, byID: map[int32]int{}}
}

// build never fails: whatever cannot be read simply leaves hosts unknown.
func (m *Map) build(ctx context.Context) *Snapshot {
	snap := emptySnapshot(m.cfg.Capacity, m.cfg.Now())

	hosts, err := m.inv.Hosts(ctx)
	if err != nil {
		m.cfg.Logger.Debug("load map: could not list hosts", "error", err)
		return snap
	}
	nodes, err := m.inv.Nodes(ctx)
	if err != nil {
		m.cfg.Logger.Debug("load map: could not list nodes", "error", err)
		nodes = nil
	}
	nodes = activeNodes(nodes)

	var data PresenceData
	if len(nodes) > 0 && m.pres != nil {
		ids := make([]int32, len(nodes))
		for i, n := range nodes {
			ids[i] = n.ID
		}
		if data, err = m.pres.Read(ctx, ids); err != nil {
			m.cfg.Logger.Debug("load map: could not read node presence", "error", err)
			data = PresenceData{}
		}
	}
	snap.Live = data.Live

	// Resolve node addresses once per build; a name that is not cached yet
	// just yields no addresses and the node then never matches by address.
	nodeIPs := make(map[int32]map[string]struct{}, len(nodes))
	for _, n := range nodes {
		nodeIPs[n.ID] = m.addresses(n.Address)
	}

	snap.Hosts = make([]HostLoad, 0, len(hosts))
	for _, h := range hosts {
		if IsInfo(h.Remark) {
			continue
		}
		hl := HostLoad{
			HostID: h.ID, Remark: h.Remark, Address: h.Address, Port: h.Port,
			Level: LevelUnknown, UsesLoad: UsesLoadVariable(h.Remark),
		}
		if data.Live && h.Port > 0 {
			m.compute(&hl, h, nodes, nodeIPs, data)
		}
		if hl.Known {
			snap.known = true
		}
		snap.byID[h.ID] = len(snap.Hosts)
		snap.Hosts = append(snap.Hosts, hl)
	}
	return snap
}

// compute fills hl from the nodes that serve host h. A node counts when it is
// reporting and serves the host's inbound tag and port; among those the ones
// whose address is the host's address are used, or all of them when none is
// (a proxied or unresolved address). Several nodes -> the mean.
func (m *Map) compute(hl *HostLoad, h Host, nodes []Node, nodeIPs map[int32]map[string]struct{}, data PresenceData) {
	var candidates []Node
	for _, n := range nodes {
		np, ok := data.Nodes[n.ID]
		if !ok || !np.Reporting {
			continue
		}
		if _, hot := np.Ports[h.Port]; hot || serves(n, h) {
			candidates = append(candidates, n)
		}
	}
	if len(candidates) == 0 {
		return
	}

	use := candidates
	if hostIPs := m.addresses(m.expand(h.Address)); len(hostIPs) > 0 {
		var matched []Node
		for _, n := range candidates {
			if overlaps(hostIPs, nodeIPs[n.ID]) {
				matched = append(matched, n)
			}
		}
		if len(matched) > 0 {
			use = matched
		}
	}

	sum := 0
	ids := make([]int32, len(use))
	for i, n := range use {
		sum += data.Nodes[n.ID].Ports[h.Port]
		ids[i] = n.ID
	}
	mean := float64(sum) / float64(len(use))
	hl.Known = true
	hl.Conns = int(math.Round(mean))
	hl.Percent = Percent(mean, m.cfg.Capacity)
	hl.Level = LevelFor(hl.Percent)
	hl.NodeIDs = ids
}

func (m *Map) expand(address string) string {
	if m.cfg.ExpandAddress != nil {
		return m.cfg.ExpandAddress(address)
	}
	return address
}

// addresses returns the IPs behind an address: itself for an IP literal, the
// cached DNS answer for a name (empty until the first background lookup lands),
// nothing for anything still holding a {variable}.
func (m *Map) addresses(address string) map[string]struct{} {
	address = strings.TrimSpace(address)
	if address == "" || strings.ContainsAny(address, "{}") {
		return nil
	}
	return m.dns.lookup(address)
}

// activeNodes drops nodes an admin disabled, sorted by id for stable output.
func activeNodes(nodes []Node) []Node {
	out := make([]Node, 0, len(nodes))
	for _, n := range nodes {
		if n.Status != "disabled" {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// serves reports whether a node's profile includes the host's inbound tag and
// port. Listen ports come from the hosts themselves, so a node with no filter
// listens on every enabled host's port whether or not a client is connected.
func serves(n Node, h Host) bool {
	if len(n.InboundTags) > 0 {
		found := false
		for _, t := range n.InboundTags {
			if t == h.InboundTag {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if len(n.ListenPorts) > 0 {
		found := false
		for _, p := range n.ListenPorts {
			if int(p) == h.Port {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func overlaps(a, b map[string]struct{}) bool {
	if len(a) > len(b) {
		a, b = b, a
	}
	for ip := range a {
		if _, ok := b[ip]; ok {
			return true
		}
	}
	return false
}
