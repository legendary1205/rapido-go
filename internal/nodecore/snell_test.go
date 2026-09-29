package nodecore

import (
	"context"
	"net"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-snell/snellv6"
	M "github.com/sagernet/sing/common/metadata"

	forkedsnell "github.com/legendary1205/rapido-go/internal/nodecore/snell"
	"github.com/legendary1205/rapido-go/internal/nodecore/traffic"
)

func snellOptions(port int, psk string, users []option.SnellUser) option.Options {
	listen := badoptionAddr("127.0.0.1")
	return option.Options{
		Inbounds: []option.Inbound{{
			Type: "snell", Tag: forkTag,
			Options: &option.SnellInboundOptions{
				Version: 6,
				AbstractSnellInboundOptions: option.AbstractSnellInboundOptions{
					ListenOptions: option.ListenOptions{Listen: &listen, ListenPort: uint16(port)},
					PSK:           psk, Users: users,
				},
			},
		}},
		Outbounds: []option.Outbound{{Type: "direct", Tag: "direct-out", Options: &option.DirectOutboundOptions{}}},
		Route:     &option.RouteOptions{Final: "direct-out"},
	}
}

// TestSnellRoundTripAndHotUpdate is the real proof for the snell fork -
// same shape as TestHysteria2RoundTripAndHotUpdate/TestTUICRoundTripAndHotUpdate:
// a real sing-snell v6 client (the same wire code a real sing-box-based
// client uses) dialing the real forked inbound. Wrong PSK, wrong user key,
// and a hot key rotation are all exercised against the real protocol, not a
// stand-in for it.
func TestSnellRoundTripAndHotUpdate(t *testing.T) {
	const psk = "correct-horse-battery-staple" // > 12 bytes, snellv6's own minimum
	port := freePort(t)
	opts := snellOptions(port, psk, []option.SnellUser{{Name: "alice", UserKey: "key-v1"}})
	mgr := traffic.NewManager()
	node, err := New(context.Background(), opts, mgr)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := node.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { node.Close() })

	echoAddr := startEchoServer(t)
	dest := dialDest(t, echoAddr)
	nodeAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))

	dial := func(clientPSK, userKey string) (net.Conn, error) {
		client, err := snellv6.NewClient(snellv6.ClientOptions{
			PSK: []byte(clientPSK), UserKey: []byte(userKey), Mode: snellv6.ModeDefault,
			Server: M.Socksaddr{Addr: netip.MustParseAddr("127.0.0.1"), Port: uint16(port)},
		})
		if err != nil {
			return nil, err
		}
		raw, err := net.DialTimeout("tcp", nodeAddr, 2*time.Second)
		if err != nil {
			return nil, err
		}
		return client.DialConn(raw, dest)
	}

	// A wrong PSK cannot even parse the outer envelope: an active client
	// mismatch, not silence, matching sing-snell's own bad-envelope
	// behavior.
	if wrong, err := dial("wrong-psk-wrong-psk-wrong", "key-v1"); err == nil {
		if err := echoRoundTrip(t, wrong, "should-not-work"); err == nil {
			t.Error("a wrong PSK was accepted")
		}
		wrong.Close()
	}

	// A right PSK but wrong user key is rejected once past the envelope.
	if wrong, err := dial(psk, "not-the-real-key"); err == nil {
		if err := echoRoundTrip(t, wrong, "should-not-work"); err == nil {
			t.Error("a wrong user key was accepted")
		}
		wrong.Close()
	}

	conn, err := dial(psk, "key-v1")
	if err != nil {
		t.Fatalf("dial with the real psk+userkey: %v", err)
	}
	if err := echoRoundTrip(t, conn, "hello-snell"); err != nil {
		t.Fatalf("echo round trip: %v", err)
	}
	conn.Close()

	waitPresence(t, mgr, "alice online then offline", func(s traffic.PresenceSnapshot) bool { return s.Total == 0 })
	if usage := mgr.Drain()["alice"]; usage.Up == 0 || usage.Down == 0 {
		t.Errorf("traffic not counted for alice: %+v", usage)
	}

	// Hot update: the running listener gets a new user key with no restart.
	in, err := runningInbound[*forkedsnell.Inbound](node, forkTag, "Snell")
	if err != nil {
		t.Fatalf("look up the running inbound: %v", err)
	}
	if err := in.UpdateUsers([]option.SnellUser{{Name: "alice", UserKey: "key-v2"}}); err != nil {
		t.Fatalf("UpdateUsers: %v", err)
	}

	if stale, err := dial(psk, "key-v1"); err == nil {
		if err := echoRoundTrip(t, stale, "should-not-work"); err == nil {
			t.Error("the old user key still works after UpdateUsers")
		}
		stale.Close()
	}
	conn2, err := dial(psk, "key-v2")
	if err != nil {
		t.Fatalf("dial with the new user key: %v", err)
	}
	defer conn2.Close()
	if err := echoRoundTrip(t, conn2, "hello-again"); err != nil {
		t.Fatalf("echo round trip after hot update: %v", err)
	}
}

// TestSnellUpdateUsersRejectsEmptyListWithoutErroring proves the zero-users
// substitution documented on the fork's own UpdateUsers: sing-snell itself
// refuses an empty list outright, but this fork's exported method must not
// propagate that as an error - a freshly zeroed-out inbound (every one of
// its users removed) is a valid, if useless, state.
func TestSnellUpdateUsersRejectsEmptyListWithoutErroring(t *testing.T) {
	port := freePort(t)
	opts := snellOptions(port, "correct-horse-battery-staple", []option.SnellUser{{Name: "alice", UserKey: "key-v1"}})
	node, err := New(context.Background(), opts, traffic.NewManager())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := node.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { node.Close() })

	in, err := runningInbound[*forkedsnell.Inbound](node, forkTag, "Snell")
	if err != nil {
		t.Fatalf("look up the running inbound: %v", err)
	}
	if err := in.UpdateUsers(nil); err != nil {
		t.Fatalf("UpdateUsers(nil) returned an error, want the synthetic-placeholder path: %v", err)
	}

	// And the previously-valid user is now genuinely refused, not still
	// authenticating against a leftover map entry.
	client, err := snellv6.NewClient(snellv6.ClientOptions{
		PSK: []byte("correct-horse-battery-staple"), UserKey: []byte("key-v1"), Mode: snellv6.ModeDefault,
		Server: M.Socksaddr{Addr: netip.MustParseAddr("127.0.0.1"), Port: uint16(port)},
	})
	if err != nil {
		t.Fatalf("snellv6.NewClient: %v", err)
	}
	raw, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn, err := client.DialConn(raw, dialDest(t, startEchoServer(t)))
	if err == nil {
		if err := echoRoundTrip(t, conn, "should-not-work"); err == nil {
			t.Error("a user removed via UpdateUsers(nil) still authenticated")
		}
		conn.Close()
	}
}
