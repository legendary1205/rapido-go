package nodecore

import (
	"context"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/logger"
	atls "github.com/sagernet/sing/common/tls"

	anytlsclient "github.com/anytls/sing-anytls"

	"github.com/legendary1205/rapido-go/internal/nodecore/traffic"
)

// TestConcurrentConnectionsForOneUserAreNotDoubleCounted is a real-world
// reproduction attempt for a reported production symptom: usage looking
// roughly 2x the real transferred amount specifically for clients that open
// several simultaneous connections (segmented downloaders like IDM, multi-
// connection speed tests). Every inbound fork wraps exactly once per
// logical connection (see traffic.WrapConn's own callers), so this isn't
// expected to find a bug here - but asserting it directly, under real
// concurrency and real distinct payload sizes per connection (so a
// cross-connection mixup would show up as a size mismatch, not just a
// wrong total), is worth more than reasoning about the code by eye.
func TestConcurrentConnectionsForOneUserAreNotDoubleCounted(t *testing.T) {
	const uuidStr = "8f8a4c1e-1e2a-4b8a-9b1a-00000000beef"
	const conns = 16

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
	opts := buildTestOptions(nodePort, []option.VLESSUser{{Name: "idmuser", UUID: uuidStr}})
	mgr := traffic.NewManager()
	node, err := New(context.Background(), opts, mgr)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := node.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { node.Close() })

	nodeAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(nodePort))

	var wg sync.WaitGroup
	var wantTotal int64
	var mu sync.Mutex
	errs := make(chan error, conns)
	for i := 0; i < conns; i++ {
		// Distinct, non-uniform sizes per connection - a bug that mixed up
		// which connection's bytes went where would corrupt the echoed
		// payload (caught by echoRoundTrip's own mismatch check), not just
		// the total.
		size := 1000 + i*137
		mu.Lock()
		wantTotal += int64(size)
		mu.Unlock()
		wg.Add(1)
		go func(payloadSize int) {
			defer wg.Done()
			conn := dialVLESS(t, nodeAddr, uuidStr, echoHost, echoPort)
			defer conn.Close()
			payload := make([]byte, payloadSize)
			for j := range payload {
				payload[j] = byte(j)
			}
			if err := echoRoundTrip(t, conn, string(payload)); err != nil {
				errs <- err
			}
		}(size)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("connection failed: %v", err)
	}

	usage := mgr.Drain()["idmuser"]
	if usage.Up != wantTotal || usage.Down != wantTotal {
		t.Errorf("usage after %d concurrent connections = {Up:%d Down:%d}, want {Up:%d Down:%d} (%.2fx up, %.2fx down)",
			conns, usage.Up, usage.Down, wantTotal, wantTotal,
			float64(usage.Up)/float64(wantTotal), float64(usage.Down)/float64(wantTotal))
	}
}

// TestConcurrentAnyTLSStreamsOnOneSessionAreNotDoubleCounted is the same
// reproduction attempt against anytls specifically - unlike VLESS, several
// of its "connections" can share one underlying TLS carrier as separate
// multiplexed streams, which is architecturally the more plausible place
// for a double-count (if the carrier's own bytes were ever counted
// alongside each stream's) to hide.
func TestConcurrentAnyTLSStreamsOnOneSessionAreNotDoubleCounted(t *testing.T) {
	certPEM, keyPEM := selfSignedCert(t)
	port := freePort(t)
	opts := anytlsOptions(port, certPEM, keyPEM, []option.AnyTLSUser{{Name: "idmuser", Password: "pw-v1"}})
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

	client, err := anytlsclient.NewClient(context.Background(), anytlsclient.ClientConfig{
		Password: "pw-v1",
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
	t.Cleanup(func() { client.Close() })

	const streams = 16
	var wg sync.WaitGroup
	var wantTotal int64
	var mu sync.Mutex
	errs := make(chan error, streams)
	for i := 0; i < streams; i++ {
		size := 1000 + i*137
		mu.Lock()
		wantTotal += int64(size)
		mu.Unlock()
		wg.Add(1)
		go func(payloadSize int) {
			defer wg.Done()
			conn, err := client.CreateProxy(context.Background(), dest)
			if err != nil {
				errs <- err
				return
			}
			defer conn.Close()
			payload := make([]byte, payloadSize)
			for j := range payload {
				payload[j] = byte(j)
			}
			if err := echoRoundTrip(t, conn, string(payload)); err != nil {
				errs <- err
			}
		}(size)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("stream failed: %v", err)
	}

	usage := mgr.Drain()["idmuser"]
	if usage.Up != wantTotal || usage.Down != wantTotal {
		t.Errorf("usage after %d concurrent anytls streams on one session = {Up:%d Down:%d}, want {Up:%d Down:%d} (%.2fx up, %.2fx down)",
			streams, usage.Up, usage.Down, wantTotal, wantTotal,
			float64(usage.Up)/float64(wantTotal), float64(usage.Down)/float64(wantTotal))
	}
}
