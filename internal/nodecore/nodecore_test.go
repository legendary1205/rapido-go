package nodecore

import (
	"context"
	"io"
	"net"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-vmess/vless"
	"github.com/sagernet/sing/common/json/badoption"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
)

func badoptionAddr(s string) badoption.Addr {
	return badoption.Addr(netip.MustParseAddr(s))
}

// startEchoServer starts a plain TCP echo server and returns its address.
func startEchoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo server: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(c, c)
			}(conn)
		}
	}()
	return ln.Addr().String()
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

// dialVLESS opens a raw TCP connection to nodeAddr and performs a VLESS
// handshake as uuid, proxying to destAddr - a real client using the same
// wire-protocol library the server (via the forked inbound) uses, so this
// exercises the actual VLESS framing, not a stand-in.
func dialVLESS(t *testing.T, nodeAddr, uuid string, destHost string, destPort int) net.Conn {
	t.Helper()
	raw, err := net.DialTimeout("tcp", nodeAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial node: %v", err)
	}
	client, err := vless.NewClient(uuid, "", logger.NOP())
	if err != nil {
		raw.Close()
		t.Fatalf("vless.NewClient: %v", err)
	}
	dest := M.Socksaddr{Addr: netip.MustParseAddr(destHost), Port: uint16(destPort)}
	conn, err := client.DialConn(raw, dest)
	if err != nil {
		raw.Close()
		t.Fatalf("vless DialConn: %v", err)
	}
	return conn
}

func echoRoundTrip(t *testing.T, conn net.Conn, payload string) error {
	t.Helper()
	if _, err := conn.Write([]byte(payload)); err != nil {
		return err
	}
	buf := make([]byte, len(payload))
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil {
		return err
	}
	if string(buf) != payload {
		t.Fatalf("echo mismatch: got %q, want %q", buf, payload)
	}
	return nil
}

func buildTestOptions(port int, users []option.VLESSUser) option.Options {
	listen := badoptionAddr("127.0.0.1")
	return option.Options{
		Inbounds: []option.Inbound{
			{
				Type: "vless",
				Tag:  "vless-in",
				Options: &option.VLESSInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     &listen,
						ListenPort: uint16(port),
					},
					Users: users,
				},
			},
		},
		Outbounds: []option.Outbound{
			{Type: "direct", Tag: "direct-out", Options: &option.DirectOutboundOptions{}},
		},
		Route: &option.RouteOptions{Final: "direct-out"},
	}
}

func TestVLESSHotAddUserWithoutRestart(t *testing.T) {
	uuidA := "8f8a4c1e-1e2a-4b8a-9b1a-000000000001"
	uuidB := "8f8a4c1e-1e2a-4b8a-9b1a-000000000002"

	echoAddr := startEchoServer(t)
	echoHost, echoPortStr, err := net.SplitHostPort(echoAddr)
	if err != nil {
		t.Fatalf("split echo addr: %v", err)
	}
	echoPort, err := strconv.Atoi(echoPortStr)
	if err != nil {
		t.Fatalf("parse echo port: %v", err)
	}

	nodePort := freePort(t)
	opts := buildTestOptions(nodePort, []option.VLESSUser{{Name: "userA", UUID: uuidA}})

	node, err := New(context.Background(), opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := node.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { node.Close() })

	nodeAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(nodePort))

	// A pre-existing user connects and proxies real data before any update.
	connA := dialVLESS(t, nodeAddr, uuidA, echoHost, echoPort)
	t.Cleanup(func() { connA.Close() })
	if err := echoRoundTrip(t, connA, "hello-from-a-before-update"); err != nil {
		t.Fatalf("userA round trip before update: %v", err)
	}

	// A user not yet registered must be rejected.
	rawReject, err := net.DialTimeout("tcp", nodeAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial node for reject test: %v", err)
	}
	clientB, err := vless.NewClient(uuidB, "", logger.NOP())
	if err != nil {
		t.Fatalf("vless.NewClient: %v", err)
	}
	dest := M.Socksaddr{Addr: netip.MustParseAddr(echoHost), Port: uint16(echoPort)}
	connReject, err := clientB.DialConn(rawReject, dest)
	if err == nil {
		// The handshake itself may succeed (VLESS acks before the server has
		// fully validated in some code paths); the proof that userB isn't
		// registered yet is that the server never proxies its data, so a
		// round trip must fail or time out.
		if roundTripErr := echoRoundTrip(t, connReject, "should-not-work"); roundTripErr == nil {
			t.Fatal("userB succeeded before being registered, want a rejection")
		}
		connReject.Close()
	}

	// Hot-add userB - no restart, connA must be unaffected.
	if err := node.UpdateVLESSUsers("vless-in", []option.VLESSUser{
		{Name: "userA", UUID: uuidA},
		{Name: "userB", UUID: uuidB},
	}); err != nil {
		t.Fatalf("UpdateVLESSUsers: %v", err)
	}

	if err := echoRoundTrip(t, connA, "hello-from-a-after-update"); err != nil {
		t.Fatalf("userA's pre-existing connection broke after a hot user update: %v", err)
	}

	connB := dialVLESS(t, nodeAddr, uuidB, echoHost, echoPort)
	t.Cleanup(func() { connB.Close() })
	if err := echoRoundTrip(t, connB, "hello-from-b-after-update"); err != nil {
		t.Fatalf("newly hot-added userB round trip: %v", err)
	}
}
