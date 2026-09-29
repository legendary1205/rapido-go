package nodecore

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"
	"github.com/sagernet/sing/common/logger"
	atls "github.com/sagernet/sing/common/tls"

	anytlsclient "github.com/anytls/sing-anytls"

	forkedanytls "github.com/legendary1205/rapido-go/internal/nodecore/anytls"
	"github.com/legendary1205/rapido-go/internal/nodecore/traffic"
)

func anytlsOptions(port int, certPEM, keyPEM string, users []option.AnyTLSUser) option.Options {
	return option.Options{
		Inbounds: []option.Inbound{{
			Type: "anytls", Tag: forkTag,
			Options: &option.AnyTLSInboundOptions{
				ListenOptions: quicListenOptions(port),
				Users:         users,
				InboundTLSOptionsContainer: option.InboundTLSOptionsContainer{TLS: &option.InboundTLSOptions{
					Enabled: true, Certificate: badoption.Listable[string]{certPEM}, Key: badoption.Listable[string]{keyPEM},
				}},
			},
		}},
		Outbounds: []option.Outbound{{Type: "direct", Tag: "direct-out", Options: &option.DirectOutboundOptions{}}},
		Route:     &option.RouteOptions{Final: "direct-out"},
	}
}

// TestAnyTLSRoundTripAndHotUpdate is the real proof for the anytls fork -
// same shape as the hysteria2/tuic/snell round-trip tests: a real
// sing-anytls client (the same wire code a real sing-box-based client uses)
// dialing the real forked inbound over a real TLS handshake. Wrong
// password and a hot password rotation are both exercised against the real
// protocol, not a stand-in for it.
func TestAnyTLSRoundTripAndHotUpdate(t *testing.T) {
	certPEM, keyPEM := selfSignedCert(t)
	port := freePort(t)
	opts := anytlsOptions(port, certPEM, keyPEM, []option.AnyTLSUser{{Name: "alice", Password: "pw-v1"}})
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

	newClient := func(password string) *anytlsclient.Client {
		c, err := anytlsclient.NewClient(context.Background(), anytlsclient.ClientConfig{
			Password: password,
			Logger:   logger.NOP(),
			DialOut: func(ctx context.Context) (net.Conn, error) {
				raw, err := net.DialTimeout("tcp", nodeAddr, 2*time.Second)
				if err != nil {
					return nil, err
				}
				return atls.ClientHandshake(ctx, raw, quicClientTLS(t))
			},
		})
		if err != nil {
			t.Fatalf("anytls NewClient: %v", err)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}

	// The client's own session multiplexer opens a stream lazily, without
	// waiting on the server's auth check (same reason TUIC's DialConn does
	// not fail on a wrong password either - see that test's own comment) -
	// a wrong password shows up as a failed echo round trip, not a failed
	// CreateProxy.
	if wrong, err := newClient("wrong").CreateProxy(context.Background(), dest); err == nil {
		if err := echoRoundTrip(t, wrong, "should-not-work"); err == nil {
			t.Error("a wrong password was accepted")
		}
		wrong.Close()
	}

	client := newClient("pw-v1")
	conn, err := client.CreateProxy(context.Background(), dest)
	if err != nil {
		t.Fatalf("CreateProxy with the real password: %v", err)
	}
	if err := echoRoundTrip(t, conn, "hello-anytls"); err != nil {
		t.Fatalf("echo round trip: %v", err)
	}
	conn.Close()

	waitPresence(t, mgr, "alice online then offline", func(s traffic.PresenceSnapshot) bool { return s.Total == 0 })
	if usage := mgr.Drain()["alice"]; usage.Up == 0 || usage.Down == 0 {
		t.Errorf("traffic not counted for alice: %+v", usage)
	}

	// Hot update: the running listener gets a new password with no restart -
	// the whole point of this fork existing.
	in, err := runningInbound[*forkedanytls.Inbound](node, forkTag, "AnyTLS")
	if err != nil {
		t.Fatalf("look up the running inbound: %v", err)
	}
	in.UpdateUsers([]option.AnyTLSUser{{Name: "alice", Password: "pw-v2"}})

	if stale, err := newClient("pw-v1").CreateProxy(context.Background(), dest); err == nil {
		if err := echoRoundTrip(t, stale, "should-not-work"); err == nil {
			t.Error("the old password still works after UpdateUsers")
		}
		stale.Close()
	}
	conn2, err := newClient("pw-v2").CreateProxy(context.Background(), dest)
	if err != nil {
		t.Fatalf("CreateProxy with the new password: %v", err)
	}
	defer conn2.Close()
	if err := echoRoundTrip(t, conn2, "hello-again"); err != nil {
		t.Fatalf("echo round trip after hot update: %v", err)
	}
}
