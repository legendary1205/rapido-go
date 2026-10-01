package nodecore

import (
	"context"
	"net/netip"
	"sync"
	"testing"

	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/gofrs/uuid/v5"
	quichysteria2 "github.com/sagernet/sing-quic/hysteria2"
	quictuic "github.com/sagernet/sing-quic/tuic"

	"github.com/legendary1205/rapido-go/internal/nodecore/traffic"
)

// runConcurrentUsageCheck opens n concurrent logical connections via dial,
// each writing/echoing its own distinct payload size, then asserts the
// drained usage for user matches the exact sum - the same reproduction
// shape as concurrent_usage_test.go's VLESS/AnyTLS checks, for the two
// protocols that multiplex over QUIC instead of a TCP+smux-style session
// (a different library, sing-quic, so a double-count bug in one family
// wouldn't necessarily appear in the other).
func runConcurrentUsageCheck(t *testing.T, mgr *traffic.Manager, user string, n int, dial func(payloadSize int) error) {
	t.Helper()
	var wg sync.WaitGroup
	var wantTotal int64
	var mu sync.Mutex
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		size := 1000 + i*137
		mu.Lock()
		wantTotal += int64(size)
		mu.Unlock()
		wg.Add(1)
		go func(payloadSize int) {
			defer wg.Done()
			if err := dial(payloadSize); err != nil {
				errs <- err
			}
		}(size)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("connection failed: %v", err)
	}

	usage := mgr.Drain()[user]
	if usage.Up != wantTotal || usage.Down != wantTotal {
		t.Errorf("usage after %d concurrent connections = {Up:%d Down:%d}, want {Up:%d Down:%d} (%.2fx up, %.2fx down)",
			n, usage.Up, usage.Down, wantTotal, wantTotal,
			float64(usage.Up)/float64(wantTotal), float64(usage.Down)/float64(wantTotal))
	}
}

func echoRoundTripOnConn(t *testing.T, conn interface {
	Write([]byte) (int, error)
	Read([]byte) (int, error)
}, payloadSize int) error {
	payload := make([]byte, payloadSize)
	for j := range payload {
		payload[j] = byte(j)
	}
	if _, err := conn.Write(payload); err != nil {
		return err
	}
	buf := make([]byte, payloadSize)
	n := 0
	for n < payloadSize {
		m, err := conn.Read(buf[n:])
		if err != nil {
			return err
		}
		n += m
	}
	return nil
}

func TestConcurrentHysteria2StreamsOnOneSessionAreNotDoubleCounted(t *testing.T) {
	certPEM, keyPEM := selfSignedCert(t)
	port := freePort(t)
	opts := option.Options{
		Inbounds: []option.Inbound{{
			Type: "hysteria2", Tag: forkTag,
			Options: &option.Hysteria2InboundOptions{
				ListenOptions: quicListenOptions(port),
				Users:         []option.Hysteria2User{{Name: "idmuser", Password: "pw-v1"}},
				InboundTLSOptionsContainer: option.InboundTLSOptionsContainer{TLS: &option.InboundTLSOptions{
					Enabled: true, Certificate: badoption.Listable[string]{certPEM}, Key: badoption.Listable[string]{keyPEM},
				}},
			},
		}},
		Outbounds: []option.Outbound{{Type: "direct", Tag: "direct-out", Options: &option.DirectOutboundOptions{}}},
		Route:     &option.RouteOptions{Final: "direct-out"},
	}
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

	client, err := quichysteria2.NewClient(quichysteria2.ClientOptions{
		Context: context.Background(), Dialer: N.SystemDialer, Logger: logger.NOP(),
		ServerAddress: M.Socksaddr{Addr: netip.MustParseAddr("127.0.0.1"), Port: uint16(port)},
		Password:      "pw-v1", TLSConfig: quicClientTLS(t),
	})
	if err != nil {
		t.Fatalf("hysteria2 NewClient: %v", err)
	}
	t.Cleanup(func() { client.CloseWithError(nil) })

	runConcurrentUsageCheck(t, mgr, "idmuser", 16, func(payloadSize int) error {
		conn, err := client.DialConn(context.Background(), dest)
		if err != nil {
			return err
		}
		defer conn.Close()
		return echoRoundTripOnConn(t, conn, payloadSize)
	})
}

func TestConcurrentTUICStreamsOnOneSessionAreNotDoubleCounted(t *testing.T) {
	certPEM, keyPEM := selfSignedCert(t)
	port := freePort(t)
	const uuidStr = "8f8a4c1e-1e2a-4b8a-9b1a-0000000000c1"
	opts := option.Options{
		Inbounds: []option.Inbound{{
			Type: "tuic", Tag: forkTag,
			Options: &option.TUICInboundOptions{
				ListenOptions: quicListenOptions(port),
				Users:         []option.TUICUser{{Name: "idmuser", UUID: uuidStr, Password: "pw-v1"}},
				InboundTLSOptionsContainer: option.InboundTLSOptionsContainer{TLS: &option.InboundTLSOptions{
					Enabled: true, Certificate: badoption.Listable[string]{certPEM}, Key: badoption.Listable[string]{keyPEM},
				}},
			},
		}},
		Outbounds: []option.Outbound{{Type: "direct", Tag: "direct-out", Options: &option.DirectOutboundOptions{}}},
		Route:     &option.RouteOptions{Final: "direct-out"},
	}
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

	parsedUUIDValue, err := uuid.FromString(uuidStr)
	if err != nil {
		t.Fatalf("parse test uuid: %v", err)
	}
	var parsedUUID [16]byte = parsedUUIDValue

	client, err := quictuic.NewClient(quictuic.ClientOptions{
		Context: context.Background(), Dialer: N.SystemDialer,
		ServerAddress: M.Socksaddr{Addr: netip.MustParseAddr("127.0.0.1"), Port: uint16(port)},
		UUID:          parsedUUID, Password: "pw-v1", TLSConfig: quicClientTLS(t),
	})
	if err != nil {
		t.Fatalf("tuic NewClient: %v", err)
	}
	t.Cleanup(func() { client.CloseWithError(nil) })

	runConcurrentUsageCheck(t, mgr, "idmuser", 16, func(payloadSize int) error {
		conn, err := client.DialConn(context.Background(), dest)
		if err != nil {
			return err
		}
		defer conn.Close()
		return echoRoundTripOnConn(t, conn, payloadSize)
	})
}
