package nodecore

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/netip"
	"strconv"
	"testing"
	"time"

	boxtls "github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/option"
	quichysteria2 "github.com/sagernet/sing-quic/hysteria2"
	quictuic "github.com/sagernet/sing-quic/tuic"
	"github.com/sagernet/sing/common/json/badoption"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	atls "github.com/sagernet/sing/common/tls"

	"github.com/gofrs/uuid/v5"

	forkedhysteria2 "github.com/legendary1205/rapido-go/internal/nodecore/hysteria2"
	"github.com/legendary1205/rapido-go/internal/nodecore/traffic"
	forkedtuic "github.com/legendary1205/rapido-go/internal/nodecore/tuic"
)

// selfSignedCert is a throwaway leaf certificate for "127.0.0.1", generated
// fresh per test - the QUIC-based forks (hysteria2, tuic) refuse to even
// start without TLS (see each fork's own doc comment), unlike the plaintext
// TCP forks the rest of this package's tests use.
func selfSignedCert(t *testing.T) (certPEM, keyPEM string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	return certPEM, keyPEM
}

func quicListenOptions(port int) option.ListenOptions {
	listen := badoption.Addr(netip.IPv4Unspecified())
	return option.ListenOptions{Listen: &listen, ListenPort: uint16(port)}
}

// quicClientTLS is the client half of selfSignedCert: no CA to verify
// against (a throwaway test cert), so it trusts on first use like a client
// pointed at an SNI/insecure config would - matching how allowinsecure is
// meant to be used, not a production posture.
func quicClientTLS(t *testing.T) atls.Config {
	cfg, err := boxtls.NewClient(context.Background(), logger.NOP(), "127.0.0.1", option.OutboundTLSOptions{Enabled: true, Insecure: true})
	if err != nil {
		t.Fatalf("build client TLS config: %v", err)
	}
	return cfg
}

func dialDest(t *testing.T, echoAddr string) M.Socksaddr {
	t.Helper()
	host, port, err := net.SplitHostPort(echoAddr)
	if err != nil {
		t.Fatalf("split echo addr: %v", err)
	}
	p, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("parse echo port: %v", err)
	}
	return M.Socksaddr{Addr: netip.MustParseAddr(host), Port: uint16(p)}
}

func TestHysteria2RoundTripAndHotUpdate(t *testing.T) {
	certPEM, keyPEM := selfSignedCert(t)
	port := freePort(t)
	opts := option.Options{
		Inbounds: []option.Inbound{{
			Type: "hysteria2", Tag: forkTag,
			Options: &option.Hysteria2InboundOptions{
				ListenOptions: quicListenOptions(port),
				Users:         []option.Hysteria2User{{Name: "alice", Password: "pw-v1"}},
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

	newClient := func(password string) *quichysteria2.Client {
		c, err := quichysteria2.NewClient(quichysteria2.ClientOptions{
			Context: context.Background(), Dialer: N.SystemDialer, Logger: logger.NOP(),
			ServerAddress: M.Socksaddr{Addr: netip.MustParseAddr("127.0.0.1"), Port: uint16(port)},
			Password:      password, TLSConfig: quicClientTLS(t),
		})
		if err != nil {
			t.Fatalf("hysteria2 NewClient: %v", err)
		}
		t.Cleanup(func() { c.CloseWithError(nil) })
		return c
	}

	// Wrong password is rejected, not silently proxied.
	if _, err := newClient("wrong").DialConn(context.Background(), dest); err == nil {
		t.Error("a wrong password was accepted")
	}

	client := newClient("pw-v1")
	conn, err := client.DialConn(context.Background(), dest)
	if err != nil {
		t.Fatalf("DialConn with the real password: %v", err)
	}
	if err := echoRoundTrip(t, conn, "hello-hysteria2"); err != nil {
		t.Fatalf("echo round trip: %v", err)
	}
	conn.Close()

	waitPresence(t, mgr, "alice online then offline", func(s traffic.PresenceSnapshot) bool { return s.Total == 0 })
	if usage := mgr.Drain()["alice"]; usage.Up == 0 || usage.Down == 0 {
		t.Errorf("traffic not counted for alice: %+v", usage)
	}

	// Hot update: the running listener gets a new password with no restart -
	// the whole point of this fork existing.
	in, err := runningInbound[*forkedhysteria2.Inbound](node, forkTag, "Hysteria2")
	if err != nil {
		t.Fatalf("look up the running inbound: %v", err)
	}
	in.UpdateUsers([]option.Hysteria2User{{Name: "alice", Password: "pw-v2"}})

	if _, err := newClient("pw-v1").DialConn(context.Background(), dest); err == nil {
		t.Error("the old password still works after UpdateUsers")
	}
	conn2, err := newClient("pw-v2").DialConn(context.Background(), dest)
	if err != nil {
		t.Fatalf("DialConn with the new password: %v", err)
	}
	defer conn2.Close()
	if err := echoRoundTrip(t, conn2, "hello-again"); err != nil {
		t.Fatalf("echo round trip after hot update: %v", err)
	}
}

func TestTUICRoundTripAndHotUpdate(t *testing.T) {
	certPEM, keyPEM := selfSignedCert(t)
	port := freePort(t)
	const uuidStr = "8f8a4c1e-1e2a-4b8a-9b1a-0000000000c1"
	opts := option.Options{
		Inbounds: []option.Inbound{{
			Type: "tuic", Tag: forkTag,
			Options: &option.TUICInboundOptions{
				ListenOptions: quicListenOptions(port),
				Users:         []option.TUICUser{{Name: "alice", UUID: uuidStr, Password: "pw-v1"}},
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

	newClient := func(uuidBytes [16]byte, password string) *quictuic.Client {
		c, err := quictuic.NewClient(quictuic.ClientOptions{
			Context: context.Background(), Dialer: N.SystemDialer,
			ServerAddress: M.Socksaddr{Addr: netip.MustParseAddr("127.0.0.1"), Port: uint16(port)},
			UUID:          uuidBytes, Password: password, TLSConfig: quicClientTLS(t),
		})
		if err != nil {
			t.Fatalf("tuic NewClient: %v", err)
		}
		t.Cleanup(func() { c.CloseWithError(nil) })
		return c
	}

	// TUIC's DialConn returns a lazily-opened stream without waiting on the
	// server's own auth check (unlike hysteria2's - see that test's own
	// version of this check) - a bad password shows up as a failed read/
	// write on the connection, not a failed DialConn.
	if wrong, err := newClient(parsedUUID, "wrong").DialConn(context.Background(), dest); err == nil {
		if err := echoRoundTrip(t, wrong, "should-not-work"); err == nil {
			t.Error("a wrong password was accepted")
		}
		wrong.Close()
	}

	client := newClient(parsedUUID, "pw-v1")
	conn, err := client.DialConn(context.Background(), dest)
	if err != nil {
		t.Fatalf("DialConn with the real password: %v", err)
	}
	if err := echoRoundTrip(t, conn, "hello-tuic"); err != nil {
		t.Fatalf("echo round trip: %v", err)
	}
	conn.Close()

	waitPresence(t, mgr, "alice online then offline", func(s traffic.PresenceSnapshot) bool { return s.Total == 0 })
	if usage := mgr.Drain()["alice"]; usage.Up == 0 || usage.Down == 0 {
		t.Errorf("traffic not counted for alice: %+v", usage)
	}

	in, err := runningInbound[*forkedtuic.Inbound](node, forkTag, "TUIC")
	if err != nil {
		t.Fatalf("look up the running inbound: %v", err)
	}
	if err := in.UpdateUsers([]option.TUICUser{{Name: "alice", UUID: uuidStr, Password: "pw-v2"}}); err != nil {
		t.Fatalf("UpdateUsers: %v", err)
	}

	if stale, err := newClient(parsedUUID, "pw-v1").DialConn(context.Background(), dest); err == nil {
		if err := echoRoundTrip(t, stale, "should-not-work"); err == nil {
			t.Error("the old password still works after UpdateUsers")
		}
		stale.Close()
	}
	conn2, err := newClient(parsedUUID, "pw-v2").DialConn(context.Background(), dest)
	if err != nil {
		t.Fatalf("DialConn with the new password: %v", err)
	}
	defer conn2.Close()
	if err := echoRoundTrip(t, conn2, "hello-again"); err != nil {
		t.Fatalf("echo round trip after hot update: %v", err)
	}
}
