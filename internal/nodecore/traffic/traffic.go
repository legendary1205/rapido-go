// Package traffic counts per-user bytes on the node's VLESS inbound and
// reports them to the panel - the Go rewrite has no Xray-style
// StatsService (gRPC QueryStats) to poll, so byte counting happens
// in-process at the one place a connection's user identity is already
// resolved: internal/nodecore/vless's forked newConnectionEx/
// newPacketConnectionEx. The same place feeds the exact count of connections
// open right now (see presence.go).
package traffic

import (
	"context"
	"net"
	"sync"
	"sync/atomic"

	"github.com/sagernet/sing/common/bufio"
	N "github.com/sagernet/sing/common/network"
)

// contextKey plus NewContext/FromContext thread a *Manager through
// sing-box's own inbound-construction context, since inbound.Register's
// constructor signature (ctx, router, logger, tag, options) has no room
// for an extra parameter - nodecore.New stashes the Manager, vless.NewInbound
// retrieves it.
type contextKey struct{}

func NewContext(ctx context.Context, mgr *Manager) context.Context {
	return context.WithValue(ctx, contextKey{}, mgr)
}

func FromContext(ctx context.Context) *Manager {
	mgr, _ := ctx.Value(contextKey{}).(*Manager)
	return mgr
}

// Usage is one user's accumulated uplink/downlink since the last Drain.
type Usage struct {
	Up   int64
	Down int64
}

type userCounter struct {
	up   atomic.Int64
	down atomic.Int64
}

// Manager accumulates per-user byte counts lock-free (a sync.Map of atomic
// counters, not a mutex-guarded map) since Add is called on every Read/
// Write of every active connection - a genuine hot path.
type Manager struct {
	users sync.Map // string (username) -> *userCounter

	// presence is the open-connection bookkeeping - see presence.go.
	presence atomic.Pointer[presenceSet]
}

func NewManager() *Manager {
	return &Manager{}
}

func (m *Manager) Add(user string, up, down int64) {
	if up == 0 && down == 0 {
		return
	}
	v, _ := m.users.LoadOrStore(user, &userCounter{})
	c := v.(*userCounter)
	if up != 0 {
		c.up.Add(up)
	}
	if down != 0 {
		c.down.Add(down)
	}
}

// Drain returns every user's usage accumulated since the last Drain and
// resets each counter to zero - mirrors Xray's own QueryStats(reset=true)
// semantics, so one push tick's payload is always a delta, not a
// cumulative total. A user with zero traffic this tick is omitted rather
// than reported as a zero row.
//
// Usernames seen once are never removed from the underlying map (a
// deleted/renamed user's entry just sits at zero forever) - a bounded,
// harmless leak scoped to distinct usernames seen since the node process
// last started, not worth the extra bookkeeping to fix.
func (m *Manager) Drain() map[string]Usage {
	out := make(map[string]Usage)
	m.users.Range(func(key, value any) bool {
		c := value.(*userCounter)
		up := c.up.Swap(0)
		down := c.down.Swap(0)
		if up != 0 || down != 0 {
			out[key.(string)] = Usage{Up: up, Down: down}
		}
		return true
	})
	return out
}

// WrapConn wraps a net.Conn accepted on an inbound listener, counting Read
// (bytes received from the client - uplink) and Write (bytes sent to the
// client - downlink) into mgr, keyed by user.
//
// Built on sing's own bufio.CounterConn rather than a hand-rolled
// net.Conn embed: a plain embed only forwards the methods it's given, but
// sing-box's copy/splice paths walk a wrapper chain looking for an
// `Upstream() any` (and friends: UnwrapReader/UnwrapWriter,
// CreateVectorisedWriter) to find the real underlying connection and
// negotiate things like buffer headroom with IT, not with whatever
// wrapper happens to be sitting in front. A wrapper that doesn't forward
// those is invisible to that negotiation, not neutral to it - see
// WrapPacketConn's doc comment for the real crash this caused on the
// packet side.
func WrapConn(conn net.Conn, user string, mgr *Manager) net.Conn {
	return bufio.NewCounterConn(conn,
		[]N.CountFunc{func(n int64) { mgr.Add(user, n, 0) }},
		[]N.CountFunc{func(n int64) { mgr.Add(user, 0, n) }},
	)
}

// WrapPacketConn is WrapConn's equivalent for the UDP/packet path.
//
// This used to be a hand-rolled struct embedding N.PacketConn directly
// (see git history) - it crashed a real production node under live
// Mux+UDP traffic: `panic: buffer overflow: capacity 16384, start 0,
// need 16` inside sing-vmess's mux WritePacket (mux is protocol-agnostic
// in this ecosystem - sing-box reuses it for VLESS too, not just VMess).
// sing-box's packet-copy loop pre-sizes buffers by walking the wrapper
// chain via `Upstream() any` to find how much header room the REAL
// innermost conn needs, so mux can prepend its header into reserved
// space instead of reallocating. A hand-rolled wrapper with no
// Upstream() is opaque to that walk, so the loop assumed zero headroom
// and handed mux a buffer with none to prepend into. sing's own
// bufio.CounterPacketConn does this exact byte-counting job already,
// with Upstream() and every other introspection method correctly
// forwarded.
func WrapPacketConn(conn N.PacketConn, user string, mgr *Manager) N.PacketConn {
	return bufio.NewCounterPacketConn(conn,
		[]N.CountFunc{func(n int64) { mgr.Add(user, n, 0) }},
		[]N.CountFunc{func(n int64) { mgr.Add(user, 0, n) }},
	)
}
