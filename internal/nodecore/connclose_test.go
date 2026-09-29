package nodecore

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-mux"
	"github.com/sagernet/sing-vmess/vless"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/legendary1205/rapido-go/internal/nodecore/traffic"
)

// muxCarrierDialer is the client half of a multiplexed VLESS session: sing-mux
// asks it for the single carrier connection, and it opens one to the node as a
// VLESS client addressed to sing-mux's magic destination. It keeps the
// connections it hands out so a test can watch them from the client side.
type muxCarrierDialer struct {
	t        *testing.T
	nodeAddr string
	uuid     string

	mu       sync.Mutex
	carriers []net.Conn
}

func (d *muxCarrierDialer) DialContext(ctx context.Context, network string, _ M.Socksaddr) (net.Conn, error) {
	raw, err := net.DialTimeout("tcp", d.nodeAddr, 2*time.Second)
	if err != nil {
		return nil, err
	}
	client, err := vless.NewClient(d.uuid, "", logger.NOP())
	if err != nil {
		raw.Close()
		return nil, err
	}
	conn, err := client.DialConn(raw, mux.Destination)
	if err != nil {
		raw.Close()
		return nil, err
	}
	d.mu.Lock()
	d.carriers = append(d.carriers, raw)
	d.mu.Unlock()
	return conn, nil
}

func (d *muxCarrierDialer) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, os.ErrInvalid
}

var _ N.Dialer = (*muxCarrierDialer)(nil)

func muxEnabledOptions(port int, users []option.VLESSUser) option.Options {
	opts := buildTestOptions(port, users)
	opts.Inbounds[0].Options.(*option.VLESSInboundOptions).Multiplex = &option.InboundMultiplexOptions{Enabled: true}
	return opts
}

// A multiplexed session's carrier connection is read by the inbound itself, so
// closing the core has to close it too. Left open, it kept accepting streams
// from the client and routing them into the closed core, where every one failed
// - a connection that looked alive to the client and served nothing, until the
// client happened to reconnect.
func TestCoreCloseEndsMultiplexedSessions(t *testing.T) {
	const uuid = "8f8a4c1e-1e2a-4b8a-9b1a-000000000101"
	echoAddr := startEchoServer(t)
	echoHost, echoPort, _ := net.SplitHostPort(echoAddr)
	port, err := strconv.Atoi(echoPort)
	if err != nil {
		t.Fatalf("parse echo port: %v", err)
	}
	dest := M.Socksaddr{Addr: netip.MustParseAddr(echoHost), Port: uint16(port)}

	nodePort := freePort(t)
	node, err := New(context.Background(), muxEnabledOptions(nodePort, []option.VLESSUser{{Name: "alice", UUID: uuid}}), traffic.NewManager())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := node.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { node.Close() })

	dialer := &muxCarrierDialer{t: t, nodeAddr: net.JoinHostPort("127.0.0.1", strconv.Itoa(nodePort)), uuid: uuid}
	client, err := mux.NewClient(mux.Options{Dialer: dialer, Protocol: "smux"})
	if err != nil {
		t.Fatalf("mux.NewClient: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	stream, err := client.DialContext(context.Background(), "tcp", dest)
	if err != nil {
		t.Fatalf("open a stream through the mux session: %v", err)
	}
	t.Cleanup(func() { stream.Close() })
	if err := echoRoundTrip(t, stream, "through-mux"); err != nil {
		t.Fatalf("echo through the mux session: %v", err)
	}
	if len(dialer.carriers) != 1 {
		t.Fatalf("carrier connections = %d, want 1", len(dialer.carriers))
	}

	node.Close()

	// The server must have closed the carrier: a read sees the end of the
	// connection, not silence until the deadline.
	carrier := dialer.carriers[0]
	carrier.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 4096)
	for {
		_, err := carrier.Read(buf)
		if err == nil {
			continue // frames the session sent while shutting down
		}
		if errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatal("the multiplexed session's connection is still open 3s after the core was closed")
		}
		break
	}
}

// Tracking must not outlive the connections it tracks: an ordinary proxied
// connection leaves the group when it ends, or a busy node would hold every
// connection it ever served.
func TestConnGroupEmptiesWhenConnectionsEnd(t *testing.T) {
	eachProtocol(t, func(t *testing.T, pc protocolCase) {
		alice := testUser{"alice", testSecret(pc, 1)}
		f := startForkedNode(t, pc, alice)

		conns := make([]net.Conn, 3)
		for i := range conns {
			conns[i] = f.dial(t, alice)
			if err := echoRoundTrip(t, conns[i], "held"); err != nil {
				t.Fatal(err)
			}
		}
		if n := f.node.conns.Len(); n != len(conns) {
			t.Fatalf("group holds %d connections with %d open", n, len(conns))
		}
		for _, c := range conns {
			c.Close()
		}
		deadline := time.Now().Add(3 * time.Second)
		for f.node.conns.Len() != 0 {
			if time.Now().After(deadline) {
				t.Fatalf("group still holds %d connections after every client closed", f.node.conns.Len())
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
}
