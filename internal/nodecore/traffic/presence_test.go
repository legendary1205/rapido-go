package traffic

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
)

func TestPresenceOpenAndClose(t *testing.T) {
	m := NewManager()

	if snap := m.Presence(); snap.Total != 0 || len(snap.Users) != 0 || len(snap.Ports) != 0 {
		t.Fatalf("a fresh manager reports presence: %+v", snap)
	}

	a1 := m.OpenConn("alice", 443)
	a2 := m.OpenConn("alice", 443)
	a3 := m.OpenConn("alice", 8443)
	b1 := m.OpenConn("bob", 443)

	snap := m.Presence()
	if snap.Total != 4 {
		t.Errorf("total = %d, want 4", snap.Total)
	}
	if want := []UserConns{{"alice", 3}, {"bob", 1}}; !equalUsers(snap.Users, want) {
		t.Errorf("users = %+v, want %+v", snap.Users, want)
	}
	if snap.Ports[443] != 3 || snap.Ports[8443] != 1 || len(snap.Ports) != 2 {
		t.Errorf("ports = %+v, want 443:3 8443:1", snap.Ports)
	}

	a1()
	a3()
	snap = m.Presence()
	if snap.Total != 2 {
		t.Errorf("total after two closes = %d, want 2", snap.Total)
	}
	if want := []UserConns{{"alice", 1}, {"bob", 1}}; !equalUsers(snap.Users, want) {
		t.Errorf("users after two closes = %+v, want %+v", snap.Users, want)
	}
	if _, ok := snap.Ports[8443]; ok || snap.Ports[443] != 2 {
		t.Errorf("ports after two closes = %+v, want only 443:2 (a port with nothing open is omitted)", snap.Ports)
	}

	a2()
	b1()
	snap = m.Presence()
	if snap.Total != 0 || len(snap.Users) != 0 || len(snap.Ports) != 0 {
		t.Errorf("everything closed but presence says %+v", snap)
	}
}

func equalUsers(a, b []UserConns) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A close signal that arrives twice must not count twice.
func TestPresenceDoubleCloseCountsOnce(t *testing.T) {
	m := NewManager()
	first := m.OpenConn("alice", 443)
	second := m.OpenConn("alice", 443)

	first()
	first()
	first()

	snap := m.Presence()
	if snap.Total != 1 || snap.Ports[443] != 1 || !equalUsers(snap.Users, []UserConns{{"alice", 1}}) {
		t.Fatalf("after closing one of two connections three times, presence = %+v, want 1 open", snap)
	}
	second()
	if snap := m.Presence(); snap.Total != 0 {
		t.Errorf("presence after the last close = %+v, want nothing", snap)
	}
}

func TestPresenceUnknownPortStillCountsForTheUser(t *testing.T) {
	m := NewManager()
	end := m.OpenConn("alice", 0)
	snap := m.Presence()
	if snap.Total != 1 || len(snap.Ports) != 0 || !equalUsers(snap.Users, []UserConns{{"alice", 1}}) {
		t.Errorf("presence = %+v, want alice online, total 1, no port entry", snap)
	}
	end()
	if snap := m.Presence(); snap.Total != 0 || len(snap.Users) != 0 {
		t.Errorf("presence after close = %+v", snap)
	}
}

// Presence never touches the byte counters and the byte counters never touch
// presence.
func TestPresenceIsIndependentOfByteCounting(t *testing.T) {
	m := NewManager()
	m.Add("alice", 10, 20)
	if snap := m.Presence(); snap.Total != 0 || len(snap.Users) != 0 {
		t.Errorf("counting bytes made a user look online: %+v", snap)
	}
	end := m.OpenConn("alice", 1)
	if got := m.Drain(); got["alice"] != (Usage{Up: 10, Down: 20}) || len(got) != 1 {
		t.Errorf("opening a connection disturbed the byte counters: %+v", got)
	}
	end()
}

func TestPresenceConcurrent(t *testing.T) {
	m := NewManager()
	const goroutines, rounds = 64, 500

	stop := make(chan struct{})
	var readers sync.WaitGroup
	readers.Add(1)
	go func() {
		defer readers.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			snap := m.Presence()
			if snap.Total < 0 {
				t.Errorf("negative total %d", snap.Total)
				return
			}
			for _, u := range snap.Users {
				if u.Conns <= 0 {
					t.Errorf("user %q listed with %d connections", u.User, u.Conns)
					return
				}
			}
		}
	}()

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			user := []string{"alice", "bob", "carol"}[g%3]
			port := uint16(20000 + g%4)
			for range rounds {
				end := m.OpenConn(user, port)
				end()
				end() // a stray second close must be harmless under contention too
			}
		}()
	}
	wg.Wait()
	close(stop)
	readers.Wait()

	if snap := m.Presence(); snap.Total != 0 || len(snap.Users) != 0 || len(snap.Ports) != 0 {
		t.Fatalf("after %d balanced open/close pairs presence = %+v, want empty", goroutines*rounds, snap)
	}
}

func TestPresenceConcurrentHoldsExactCount(t *testing.T) {
	m := NewManager()
	const goroutines, each = 32, 100

	var ends sync.Map
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range each {
				ends.Store(g*each+i, m.OpenConn("shared", 7))
			}
		}()
	}
	wg.Wait()

	snap := m.Presence()
	if snap.Total != goroutines*each || snap.Ports[7] != goroutines*each || snap.Users[0].Conns != goroutines*each {
		t.Fatalf("presence = %+v, want %d open everywhere", snap, goroutines*each)
	}
	ends.Range(func(_, v any) bool { v.(func())(); return true })
	if snap := m.Presence(); snap.Total != 0 {
		t.Errorf("after closing them all: %+v", snap)
	}
}

func TestTrackCloseChainsTheParentHandler(t *testing.T) {
	m := NewManager()
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	var parentCalls atomic.Int32
	var gotErr error
	onClose := m.TrackClose("alice", server, 443, func(err error) {
		parentCalls.Add(1)
		gotErr = err
	})
	if snap := m.Presence(); snap.Total != 1 || snap.Ports[443] != 1 {
		t.Fatalf("presence after TrackClose = %+v, want alice on the fallback port 443 (a pipe has no port)", snap)
	}
	if parentCalls.Load() != 0 {
		t.Fatal("the parent handler ran before the connection closed")
	}

	boom := errors.New("boom")
	onClose(boom)
	if parentCalls.Load() != 1 || gotErr != boom {
		t.Errorf("parent called %d times with %v, want once with the original error", parentCalls.Load(), gotErr)
	}
	if snap := m.Presence(); snap.Total != 0 {
		t.Errorf("presence after close = %+v, want nothing", snap)
	}

	// The count must be gone even if the parent misbehaves, and a nil parent is
	// fine: sing-box passes nil on some paths.
	onClose = m.TrackClose("bob", server, 443, nil)
	onClose(nil)
	onClose(nil)
	if snap := m.Presence(); snap.Total != 0 {
		t.Errorf("presence after a nil-parent close, twice = %+v", snap)
	}
}

func TestLocalPortReadsTheListeningPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, _ := ln.Accept()
		accepted <- c
	}()
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server := <-accepted
	defer server.Close()

	want := uint16(ln.Addr().(*net.TCPAddr).Port)
	if got := LocalPort(server, 1); got != want {
		t.Errorf("LocalPort = %d, want the listener's port %d", got, want)
	}
	// The dialing side's local port is an ephemeral one - proof it is LocalAddr
	// that is read and not something fixed.
	if got := LocalPort(client, 1); got == want || got == 1 {
		t.Errorf("LocalPort(client) = %d", got)
	}
}

type panickyConn struct{ net.Conn }

func (panickyConn) LocalAddr() net.Addr { panic("stub!") }

type nilAddrConn struct{ net.Conn }

func (nilAddrConn) LocalAddr() net.Addr { return nil }

func TestLocalPortNeverPanics(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	for name, conn := range map[string]net.Conn{
		"pipe":          server,
		"stubbed":       panickyConn{server},
		"nil address":   nilAddrConn{server},
		"nil interface": nil,
	} {
		if got := LocalPort(conn, 4242); got != 4242 {
			t.Errorf("%s: LocalPort = %d, want the fallback 4242", name, got)
		}
	}
}

// A core restart discards its connections. Their close signals may still be on
// the way, or never come; neither may corrupt the count of the next core.
func TestResetPresenceIgnoresLateClosesFromTheOldGeneration(t *testing.T) {
	m := NewManager()
	oldA := m.OpenConn("alice", 443)
	oldB := m.OpenConn("bob", 443)

	m.ResetPresence()
	if snap := m.Presence(); snap.Total != 0 || len(snap.Users) != 0 || len(snap.Ports) != 0 {
		t.Fatalf("presence after a reset = %+v, want empty", snap)
	}

	fresh := m.OpenConn("alice", 443)
	oldA() // a straggler from before the reset
	oldB()
	snap := m.Presence()
	if snap.Total != 1 || !equalUsers(snap.Users, []UserConns{{"alice", 1}}) || snap.Ports[443] != 1 {
		t.Fatalf("presence = %+v, want exactly the one new connection (late closes must not drive counts negative)", snap)
	}
	fresh()
	if snap := m.Presence(); snap.Total != 0 {
		t.Errorf("presence = %+v, want empty", snap)
	}
}

// The zero Manager (no NewManager) must work too.
func TestPresenceOnAZeroManager(t *testing.T) {
	var m Manager
	end := m.OpenConn("alice", 1)
	if snap := m.Presence(); snap.Total != 1 {
		t.Errorf("presence = %+v", snap)
	}
	end()
}

func TestPresenceSnapshotIsSortedAndStable(t *testing.T) {
	m := NewManager()
	for _, name := range []string{"zed", "amy", "mia"} {
		m.OpenConn(name, 1)
	}
	got := m.Presence().Users
	if len(got) != 3 || got[0].User != "amy" || got[1].User != "mia" || got[2].User != "zed" {
		t.Errorf("users = %+v, want sorted by name", got)
	}
	// Never nil, so it marshals as [] and {} rather than null.
	empty := NewManager().Presence()
	if empty.Users == nil || empty.Ports == nil {
		t.Errorf("an empty snapshot has nil collections: %+v", empty)
	}
}
