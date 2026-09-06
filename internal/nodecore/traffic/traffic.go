// Package traffic counts per-user bytes on the node's VLESS inbound and
// reports them to the panel - the Go rewrite has no Xray-style
// StatsService (gRPC QueryStats) to poll, so byte counting happens
// in-process at the one place a connection's user identity is already
// resolved: internal/nodecore/vless's forked newConnectionEx/
// newPacketConnectionEx.
package traffic

import (
	"context"
	"net"
	"sync"
	"sync/atomic"

	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
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

// CountingConn wraps a net.Conn accepted on an inbound listener, counting
// Read (bytes received from the client - uplink) and Write (bytes sent to
// the client - downlink) into mgr, keyed by user.
type CountingConn struct {
	net.Conn
	user string
	mgr  *Manager
}

func WrapConn(conn net.Conn, user string, mgr *Manager) net.Conn {
	return &CountingConn{Conn: conn, user: user, mgr: mgr}
}

func (c *CountingConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.mgr.Add(c.user, int64(n), 0)
	}
	return n, err
}

func (c *CountingConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if n > 0 {
		c.mgr.Add(c.user, 0, int64(n))
	}
	return n, err
}

// CountingPacketConn is CountingConn's equivalent for the UDP/packet path.
type CountingPacketConn struct {
	N.PacketConn
	user string
	mgr  *Manager
}

func WrapPacketConn(conn N.PacketConn, user string, mgr *Manager) N.PacketConn {
	return &CountingPacketConn{PacketConn: conn, user: user, mgr: mgr}
}

func (c *CountingPacketConn) ReadPacket(buffer *buf.Buffer) (M.Socksaddr, error) {
	destination, err := c.PacketConn.ReadPacket(buffer)
	if err == nil {
		c.mgr.Add(c.user, int64(buffer.Len()), 0)
	}
	return destination, err
}

func (c *CountingPacketConn) WritePacket(buffer *buf.Buffer, destination M.Socksaddr) error {
	n := buffer.Len()
	err := c.PacketConn.WritePacket(buffer, destination)
	if err == nil {
		c.mgr.Add(c.user, 0, int64(n))
	}
	return err
}
