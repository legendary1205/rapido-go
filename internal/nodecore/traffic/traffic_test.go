package traffic

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

func TestManagerAddAndDrain(t *testing.T) {
	m := NewManager()
	m.Add("alice", 100, 200)
	m.Add("alice", 50, 0)
	m.Add("bob", 10, 10)

	got := m.Drain()
	if got["alice"] != (Usage{Up: 150, Down: 200}) {
		t.Errorf("alice = %+v, want {150 200}", got["alice"])
	}
	if got["bob"] != (Usage{Up: 10, Down: 10}) {
		t.Errorf("bob = %+v, want {10 10}", got["bob"])
	}
	if len(got) != 2 {
		t.Errorf("len(got) = %d, want 2", len(got))
	}
}

func TestManagerDrainResets(t *testing.T) {
	m := NewManager()
	m.Add("alice", 100, 0)
	_ = m.Drain()

	second := m.Drain()
	if _, ok := second["alice"]; ok {
		t.Errorf("alice still present after a second Drain with no new Add - reset-on-read broken: %+v", second)
	}

	m.Add("alice", 5, 0)
	third := m.Drain()
	if third["alice"] != (Usage{Up: 5, Down: 0}) {
		t.Errorf("alice after re-adding post-drain = %+v, want {5 0} (not carrying over the earlier 100)", third["alice"])
	}
}

func TestManagerZeroAddIsANoOp(t *testing.T) {
	m := NewManager()
	m.Add("alice", 0, 0)
	got := m.Drain()
	if len(got) != 0 {
		t.Errorf("a zero Add should not create an entry at all, got %+v", got)
	}
}

// fakePacketConn is the minimum viable N.PacketConn - just enough for
// WrapPacketConn to compile against and drive.
type fakePacketConn struct {
	readData []byte
	readAddr M.Socksaddr
	written  []byte
}

func (f *fakePacketConn) ReadPacket(buffer *buf.Buffer) (M.Socksaddr, error) {
	buffer.Write(f.readData)
	return f.readAddr, nil
}

func (f *fakePacketConn) WritePacket(buffer *buf.Buffer, destination M.Socksaddr) error {
	f.written = append(f.written, buffer.Bytes()...)
	return nil
}

func (f *fakePacketConn) Close() error                       { return nil }
func (f *fakePacketConn) LocalAddr() net.Addr                { return nil }
func (f *fakePacketConn) SetDeadline(t time.Time) error      { return nil }
func (f *fakePacketConn) SetReadDeadline(t time.Time) error  { return nil }
func (f *fakePacketConn) SetWriteDeadline(t time.Time) error { return nil }

func TestWrapPacketConnCountsReadAndWrite(t *testing.T) {
	m := NewManager()
	inner := &fakePacketConn{readData: []byte("hello")}
	wrapped := WrapPacketConn(inner, "alice", m)

	buffer := buf.NewSize(1024)
	defer buffer.Release()
	if _, err := wrapped.ReadPacket(buffer); err != nil {
		t.Fatalf("ReadPacket: %v", err)
	}
	if err := wrapped.WritePacket(buf.As([]byte("world")), M.Socksaddr{}); err != nil {
		t.Fatalf("WritePacket: %v", err)
	}

	got := m.Drain()
	if got["alice"] != (Usage{Up: 5, Down: 5}) {
		t.Errorf("alice = %+v, want {5 5} (read 'hello', wrote 'world')", got["alice"])
	}
}

// TestWrapPacketConnForwardsUpstream is a regression test for a real
// production crash: the previous hand-rolled wrapper embedded
// N.PacketConn directly and implemented no Upstream() method, making it
// opaque to sing-box's wrapper-chain walk (used to size packet buffers
// with the correct header room for whatever sits underneath - see
// WrapPacketConn's doc comment). Any wrapper that doesn't forward
// Upstream() reintroduces that exact crash under Mux+UDP traffic, so this
// asserts the contract directly rather than relying on triggering the
// real sing-vmess panic in a unit test.
func TestWrapPacketConnForwardsUpstream(t *testing.T) {
	inner := &fakePacketConn{}
	wrapped := WrapPacketConn(inner, "alice", NewManager())

	u, ok := wrapped.(interface{ Upstream() any })
	if !ok {
		t.Fatal("WrapPacketConn's result does not implement Upstream() any - " +
			"sing-box's buffer-headroom introspection cannot see past this wrapper")
	}
	if u.Upstream() != N.PacketConn(inner) {
		t.Errorf("Upstream() = %v, want the original inner conn %v", u.Upstream(), inner)
	}
}

func TestWrapConnCountsReadAndWrite(t *testing.T) {
	m := NewManager()
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	wrapped := WrapConn(server, "bob", m)
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 5)
		n, _ := wrapped.Read(buf)
		_, _ = wrapped.Write(buf[:n])
	}()

	if _, err := client.Write([]byte("hello")); err != nil {
		t.Fatalf("client write: %v", err)
	}
	readBack := make([]byte, 5)
	if _, err := client.Read(readBack); err != nil {
		t.Fatalf("client read: %v", err)
	}
	<-done

	got := m.Drain()
	if got["bob"] != (Usage{Up: 5, Down: 5}) {
		t.Errorf("bob = %+v, want {5 5}", got["bob"])
	}
}

// TestWrapConnForwardsUpstream mirrors TestWrapPacketConnForwardsUpstream
// for the stream-conn side of the same wrapper class - not yet known to
// have caused a live crash, but the same missing-Upstream() defect would
// hit the same class of sing-box introspection (e.g. splice/vectorised
// write negotiation) if a hand-rolled embed ever replaced this again.
//
// sing's own bufio.NewExtendedConn inserts one more adapter layer between
// WrapConn's result and the raw conn, so Upstream() alone only unwraps
// one level here - walk the whole chain rather than asserting on a single
// hop, same as sing-box's real introspection code does.
func TestWrapConnForwardsUpstream(t *testing.T) {
	server, _ := net.Pipe()
	defer server.Close()
	wrapped := WrapConn(server, "bob", NewManager())

	var found bool
	current := any(wrapped)
	for i := 0; i < 5; i++ {
		u, ok := current.(interface{ Upstream() any })
		if !ok {
			break
		}
		current = u.Upstream()
		if current == net.Conn(server) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("walking Upstream() from WrapConn's result never reached the original inner conn %v (stopped at %v)", server, current)
	}
}

func TestManagerConcurrentAdd(t *testing.T) {
	m := NewManager()
	const goroutines = 50
	const perGoroutine = 1000

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				m.Add("shared", 1, 2)
			}
		}()
	}
	wg.Wait()

	got := m.Drain()
	wantUp := int64(goroutines * perGoroutine)
	wantDown := int64(goroutines * perGoroutine * 2)
	if got["shared"] != (Usage{Up: wantUp, Down: wantDown}) {
		t.Errorf("shared = %+v, want {%d %d}", got["shared"], wantUp, wantDown)
	}
}
