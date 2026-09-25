package nodecore

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"

	"github.com/legendary1205/rapido-go/internal/nodecore/traffic"
)

// waitPresence polls the manager until ok accepts a snapshot, and fails with the
// last snapshot if that does not happen in time. Closes reach the node through
// the kernel, so they are not instantaneous.
func waitPresence(t *testing.T, mgr *traffic.Manager, what string, ok func(traffic.PresenceSnapshot) bool) traffic.PresenceSnapshot {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		snap := mgr.Presence()
		if ok(snap) {
			return snap
		}
		if time.Now().After(deadline) {
			t.Fatalf("presence never reached %q, last reading: %+v", what, snap)
		}
		time.Sleep(15 * time.Millisecond)
	}
}

func usersOf(snap traffic.PresenceSnapshot) map[string]int64 {
	out := map[string]int64{}
	for _, u := range snap.Users {
		out[u.User] = u.Conns
	}
	return out
}

func portOf(t *testing.T, addr string) uint16 {
	t.Helper()
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		t.Fatal(err)
	}
	return uint16(n)
}

// TestForkedProtocolsPresence: through a real node, an authenticated
// connection is counted for its user and its listening port from the moment the
// user is known until the client goes away, a refused client and a UDP
// association are never counted, and everything returns to zero.
func TestForkedProtocolsPresence(t *testing.T) {
	eachProtocol(t, func(t *testing.T, pc protocolCase) {
		alice := testUser{"alice", testSecret(pc, 1)}
		bob := testUser{"bob", testSecret(pc, 2)}
		f := startForkedNode(t, pc, alice, bob)
		port := portOf(t, f.addr)

		if snap := f.mgr.Presence(); snap.Total != 0 || len(snap.Users) != 0 {
			t.Fatalf("a node nobody connected to reports presence: %+v", snap)
		}

		a1 := f.dial(t, alice)
		a2 := f.dial(t, alice)
		b1 := f.dial(t, bob)
		for name, c := range map[string]net.Conn{"alice-1": a1, "alice-2": a2, "bob": b1} {
			if err := echoRoundTrip(t, c, "presence-"+name); err != nil {
				t.Fatalf("%s round trip: %v", name, err)
			}
		}
		snap := waitPresence(t, f.mgr, "alice 2, bob 1", func(s traffic.PresenceSnapshot) bool { return s.Total == 3 })
		if got := usersOf(snap); got["alice"] != 2 || got["bob"] != 1 || len(got) != 2 {
			t.Errorf("users = %v, want alice 2, bob 1", got)
		}
		if snap.Ports[port] != 3 || len(snap.Ports) != 1 {
			t.Errorf("ports = %v, want only the listening port %d with 3", snap.Ports, port)
		}

		// A client that fails authentication was never a user's connection.
		f.requireRejected(t, testUser{"mallory", testSecret(pc, 99)})
		time.Sleep(100 * time.Millisecond)
		if snap := f.mgr.Presence(); snap.Total != 3 || usersOf(snap)["mallory"] != 0 {
			t.Errorf("a rejected client changed presence: %+v", snap)
		}

		// A UDP association is not a connection.
		pconn, err := pc.dialUDP(f.addr, alice, M.SocksaddrFromNet(f.udpDest))
		if err != nil {
			t.Fatalf("dial udp: %v", err)
		}
		t.Cleanup(func() { pconn.Close() })
		if err := udpEchoOnce(pconn, f.udpDest, []byte("presence-udp")); err != nil {
			t.Fatalf("udp round trip: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
		if snap := f.mgr.Presence(); snap.Total != 3 || usersOf(snap)["alice"] != 2 {
			t.Errorf("a UDP association was counted as a connection: %+v", snap)
		}

		a1.Close()
		snap = waitPresence(t, f.mgr, "alice 1, bob 1", func(s traffic.PresenceSnapshot) bool { return s.Total == 2 })
		if got := usersOf(snap); got["alice"] != 1 || got["bob"] != 1 {
			t.Errorf("users after one close = %v, want alice 1, bob 1", got)
		}

		a2.Close()
		b1.Close()
		waitPresence(t, f.mgr, "nobody online", func(s traffic.PresenceSnapshot) bool {
			return s.Total == 0 && len(s.Users) == 0 && len(s.Ports) == 0
		})
	})
}

// A connection that opens and dies before a single payload byte must not leave
// a count behind.
func TestForkedProtocolsPresenceEarlyClose(t *testing.T) {
	eachProtocol(t, func(t *testing.T, pc protocolCase) {
		alice := testUser{"alice", testSecret(pc, 1)}
		f := startForkedNode(t, pc, alice)

		for range 5 {
			conn, err := pc.dialTCP(f.addr, alice, f.tcpDest)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			conn.Close()
		}
		// A destination that refuses the connection: the dial fails after the user
		// is known.
		closed, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		deadDest := M.SocksaddrFromNet(closed.Addr())
		closed.Close()
		for range 3 {
			conn, err := pc.dialTCP(f.addr, alice, deadDest)
			if err != nil {
				t.Fatalf("dial to a dead destination: %v", err)
			}
			conn.Write([]byte("x"))
			conn.Close()
		}
		waitPresence(t, f.mgr, "nobody online", func(s traffic.PresenceSnapshot) bool { return s.Total == 0 && len(s.Users) == 0 })
	})
}

func TestVLESSPresence(t *testing.T) {
	uuidA := "8f8a4c1e-1e2a-4b8a-9b1a-000000000011"
	uuidB := "8f8a4c1e-1e2a-4b8a-9b1a-000000000012"
	echoHost, echoPortStr, err := net.SplitHostPort(startEchoServer(t))
	if err != nil {
		t.Fatal(err)
	}
	echoPort, _ := strconv.Atoi(echoPortStr)

	nodePort := freePort(t)
	mgr := traffic.NewManager()
	node, err := New(context.Background(), buildTestOptions(nodePort, []option.VLESSUser{{Name: "userA", UUID: uuidA}, {Name: "userB", UUID: uuidB}}), mgr)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := node.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { node.Close() })
	nodeAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(nodePort))

	a1 := dialVLESS(t, nodeAddr, uuidA, echoHost, echoPort)
	a2 := dialVLESS(t, nodeAddr, uuidA, echoHost, echoPort)
	b1 := dialVLESS(t, nodeAddr, uuidB, echoHost, echoPort)
	for _, c := range []net.Conn{a1, a2, b1} {
		if err := echoRoundTrip(t, c, "presence"); err != nil {
			t.Fatalf("round trip: %v", err)
		}
	}
	snap := waitPresence(t, mgr, "userA 2, userB 1", func(s traffic.PresenceSnapshot) bool { return s.Total == 3 })
	if got := usersOf(snap); got["userA"] != 2 || got["userB"] != 1 {
		t.Errorf("users = %v, want userA 2, userB 1", got)
	}
	if snap.Ports[uint16(nodePort)] != 3 {
		t.Errorf("ports = %v, want %d with 3", snap.Ports, nodePort)
	}

	// A connection that never handshakes has no user to be counted for.
	rawReject, err := net.DialTimeout("tcp", nodeAddr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer rawReject.Close()
	time.Sleep(100 * time.Millisecond)
	if snap := mgr.Presence(); snap.Total != 3 {
		t.Errorf("a bare TCP connection with no handshake was counted: %+v", snap)
	}

	// A destination that refuses the connection: the dial fails after the user is
	// known, and the failure path must still end the count.
	refusing, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadPort := refusing.Addr().(*net.TCPAddr).Port
	refusing.Close()
	for range 3 {
		c := dialVLESS(t, nodeAddr, uuidA, echoHost, deadPort)
		c.Write([]byte("x"))
		c.Close()
	}

	a1.Close()
	a2.Close()
	waitPresence(t, mgr, "userB only", func(s traffic.PresenceSnapshot) bool {
		return s.Total == 1 && usersOf(s)["userB"] == 1 && len(s.Users) == 1
	})
	b1.Close()
	waitPresence(t, mgr, "nobody online", func(s traffic.PresenceSnapshot) bool { return s.Total == 0 })
}

// Stopping a core drops every connection; a caller that then resets presence must
// see zero even though the old core's last closes may still be in flight.
func TestPresenceResetAfterCoreClose(t *testing.T) {
	pc := protocolCases()[1] // trojan
	alice := testUser{"alice", testSecret(pc, 1)}
	f := startForkedNode(t, pc, alice)

	conn := f.dial(t, alice)
	if err := echoRoundTrip(t, conn, "before-close"); err != nil {
		t.Fatal(err)
	}
	waitPresence(t, f.mgr, "alice online", func(s traffic.PresenceSnapshot) bool { return s.Total == 1 })

	f.node.Close()
	f.mgr.ResetPresence()
	if snap := f.mgr.Presence(); snap.Total != 0 || len(snap.Users) != 0 {
		t.Fatalf("presence after core close + reset = %+v", snap)
	}
	time.Sleep(200 * time.Millisecond) // late closes from the old core
	if snap := f.mgr.Presence(); snap.Total != 0 || len(snap.Users) != 0 {
		t.Errorf("late closes disturbed the reset presence: %+v", snap)
	}
}
