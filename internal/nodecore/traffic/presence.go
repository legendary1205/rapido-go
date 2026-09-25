package traffic

import (
	"net"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"

	N "github.com/sagernet/sing/common/network"
)

// Presence is the exact set of client connections open right now, kept next to
// the byte counters because it is fed from the same place: the forked inbounds,
// the one spot where a connection's user is already resolved.
//
// A connection is counted from the moment its user is known until sing-box
// reports it closed, through the onClose handler every inbound receives with a
// connection. That handler is the only close signal that is correct for every
// path a connection can take (both copy directions done, a failed dial, a
// refused route, a mux session ending). Detecting the close from a net.Conn
// wrapper is NOT an option: see WrapConn for why a hand-rolled wrapper in that
// chain breaks sing-box's splice negotiation. A connection whose authentication
// failed never reaches the code that counts, so it is never counted.
//
// Everything here is lock-free on the connection path: an atomic counter per
// user and per listening port, found through a sync.Map, so a connection costs
// two map hits and a few atomic adds - and nothing at all per byte.

// presenceSet is one generation of counters. A close func only ever touches the
// generation it opened in, so ResetPresence can start a fresh one at any moment
// without a late close from the old core driving a counter below zero.
type presenceSet struct {
	users sync.Map // string -> *atomic.Int64
	ports sync.Map // uint16 -> *atomic.Int64
	total atomic.Int64
}

func (m *Manager) currentPresence() *presenceSet {
	if set := m.presence.Load(); set != nil {
		return set
	}
	m.presence.CompareAndSwap(nil, new(presenceSet))
	return m.presence.Load()
}

func counterFor[K comparable](m *sync.Map, key K) *atomic.Int64 {
	if v, ok := m.Load(key); ok {
		return v.(*atomic.Int64)
	}
	v, _ := m.LoadOrStore(key, new(atomic.Int64))
	return v.(*atomic.Int64)
}

// OpenConn counts one open client connection of user that arrived on the given
// listening port, and returns the func that ends it. The func is idempotent:
// only the first call counts, so a close signal delivered twice cannot skew the
// numbers. A zero port (unknown) still counts for the user and the total, just
// not for any port.
func (m *Manager) OpenConn(user string, port uint16) (closeFn func()) {
	set := m.currentPresence()
	uc := counterFor(&set.users, user)
	uc.Add(1)
	var pc *atomic.Int64
	if port != 0 {
		pc = counterFor(&set.ports, port)
		pc.Add(1)
	}
	set.total.Add(1)

	var closed atomic.Bool
	return func() {
		if !closed.CompareAndSwap(false, true) {
			return
		}
		uc.Add(-1)
		if pc != nil {
			pc.Add(-1)
		}
		set.total.Add(-1)
	}
}

// TrackClose is OpenConn for an inbound handler: it counts the connection on
// conn's local port (fallbackPort when conn cannot say) and returns onClose with
// the end of that count chained in front of it. Hand the result to the router in
// place of onClose. onClose may be nil.
//
// It only reads conn.LocalAddr; conn itself is not wrapped and must not be
// replaced by anything derived from this call.
func (m *Manager) TrackClose(user string, conn net.Conn, fallbackPort uint16, onClose N.CloseHandlerFunc) N.CloseHandlerFunc {
	end := m.OpenConn(user, LocalPort(conn, fallbackPort))
	return func(err error) {
		end()
		if onClose != nil {
			onClose(err)
		}
	}
}

// LocalPort is the local (listening) port of conn, or fallback when conn does
// not report a usable one. It never panics: some sing-box connection types stub
// LocalAddr out.
func LocalPort(conn net.Conn, fallback uint16) (port uint16) {
	defer func() {
		if recover() != nil {
			port = fallback
		}
	}()
	if conn == nil {
		return fallback
	}
	if p := addrPort(conn.LocalAddr()); p != 0 {
		return p
	}
	return fallback
}

func addrPort(addr net.Addr) uint16 {
	switch a := addr.(type) {
	case nil:
		return 0
	case *net.TCPAddr:
		if a == nil {
			return 0
		}
		return uint16(a.Port)
	case *net.UDPAddr:
		if a == nil {
			return 0
		}
		return uint16(a.Port)
	}
	_, p, err := net.SplitHostPort(addr.String())
	if err != nil {
		return 0
	}
	n, err := strconv.ParseUint(p, 10, 16)
	if err != nil {
		return 0
	}
	return uint16(n)
}

// UserConns is one online user and how many connections they have open.
type UserConns struct {
	User  string
	Conns int64
}

// PresenceSnapshot is a point-in-time reading of the open connections.
type PresenceSnapshot struct {
	// Total is every open client connection.
	Total int64
	// Ports is the open count per listening port, only ports with any.
	Ports map[uint16]int64
	// Users is everyone with at least one open connection, sorted by name.
	Users []UserConns
}

// Presence reads the counters. The reading is not one atomic instant - a
// connection opening while it runs may show in one figure and not another - which
// is immaterial for a value that is replaced every few seconds.
func (m *Manager) Presence() PresenceSnapshot {
	set := m.currentPresence()
	snap := PresenceSnapshot{Ports: map[uint16]int64{}, Users: []UserConns{}}
	set.users.Range(func(key, value any) bool {
		if n := value.(*atomic.Int64).Load(); n > 0 {
			snap.Users = append(snap.Users, UserConns{User: key.(string), Conns: n})
		}
		return true
	})
	sort.Slice(snap.Users, func(i, j int) bool { return snap.Users[i].User < snap.Users[j].User })
	set.ports.Range(func(key, value any) bool {
		if n := value.(*atomic.Int64).Load(); n > 0 {
			snap.Ports[key.(uint16)] = n
		}
		return true
	})
	snap.Total = max(set.total.Load(), 0)
	return snap
}

// ResetPresence forgets every open connection. The node calls it once a core has
// been closed: nothing that core accepted is open any more, and should some
// close signal never have arrived, the leftover count would otherwise stay for
// the life of the process. Closes still to come from the old core land on the
// discarded generation and change nothing.
func (m *Manager) ResetPresence() {
	m.presence.Store(new(presenceSet))
}
