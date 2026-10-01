package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/netip"
	"testing"
	"time"

	sbox "github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/transport/v2raywebsocket"
	"github.com/sagernet/sing-vmess/vless"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// tlsDialer is the N.Dialer a WebSocket test client dials through: plain TCP
// to the node, wrapped in crypto/tls when tlsConf is set - the same layering
// an Xray client of a "security=tls&type=ws" link uses (TLS, then the
// WebSocket upgrade inside it).
type tlsDialer struct{ tlsConf *tls.Config }

func (d tlsDialer) DialContext(ctx context.Context, network string, dest M.Socksaddr) (net.Conn, error) {
	var nd net.Dialer
	conn, err := nd.DialContext(ctx, "tcp", dest.String())
	if err != nil || d.tlsConf == nil {
		return conn, err
	}
	tc := tls.Client(conn, d.tlsConf)
	if err := tc.HandshakeContext(ctx); err != nil {
		conn.Close()
		return nil, err
	}
	return tc, nil
}

func (tlsDialer) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, net.ErrClosed
}

var _ N.Dialer = tlsDialer{}

// echoViaWebSocket is one request through a vless+ws inbound: WebSocket
// upgrade on path, then VLESS inside it, then the echo round trip.
func echoViaWebSocket(nodePort uint16, path, uuid string, tlsConf *tls.Config, echoHost string, echoPort int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server := M.ParseSocksaddrHostPort("127.0.0.1", nodePort)
	ws, err := v2raywebsocket.NewClient(ctx, tlsDialer{tlsConf}, server, sbox.V2RayWebsocketOptions{Path: path}, nil)
	if err != nil {
		return err
	}
	raw, err := ws.DialContext(ctx)
	if err != nil {
		return err
	}
	defer raw.Close()
	client, err := vless.NewClient(uuid, "", logger.NOP())
	if err != nil {
		return err
	}
	conn, err := client.DialConn(raw, M.Socksaddr{Addr: netip.MustParseAddr(echoHost), Port: uint16(echoPort)})
	if err != nil {
		return err
	}
	return echoConn(conn, "ping-through-the-websocket")
}

// selfSignedPEM is a throwaway certificate/key for name, as PEM - what an
// admin pastes into a tls inbound.
func selfSignedPEM(t *testing.T, name string) (certPEM, keyPEM string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     []string{name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
}

// TestVLESSOverWebSocketWorksThroughBuildOptionsPlan is the shape a migrated
// PasarGuard/Xray panel needs: "vless + ws (+ tls)" on a path. Without the
// transport the node used to serve plain TCP there and every ws client failed.
func TestVLESSOverWebSocketWorksThroughBuildOptionsPlan(t *testing.T) {
	const uuid = "688b1a71-fde2-4819-aa5d-b9d71dfa7780"
	for _, withTLS := range []bool{false, true} {
		name := "plain"
		if withTLS {
			name = "tls"
		}
		t.Run(name, func(t *testing.T) {
			echoHost, echoPort := startEcho(t)
			port := freeTCPPort(t)
			in := inboundSpec{
				Tag: "VLESS un", Protocol: "vless", ListenPort: port,
				Users:     []userSpec{{Name: "alice", UUID: uuid}},
				Transport: &transportSpec{Type: "ws", Path: "/trk01b"},
			}
			var clientTLS *tls.Config
			if withTLS {
				certPEM, keyPEM := selfSignedPEM(t, "ggv2.example.com")
				in.TLS = &tlsSpec{ServerName: "ggv2.example.com", Certificate: certPEM, Key: keyPEM}
				clientTLS = &tls.Config{ServerName: "ggv2.example.com", InsecureSkipVerify: true, NextProtos: []string{"http/1.1"}}
			}
			startPlanned(t, startRequest{Inbounds: []inboundSpec{in}})

			if err := echoViaWebSocket(port, "/trk01b", uuid, clientTLS, echoHost, echoPort); err != nil {
				t.Fatalf("vless over ws on the inbound's path: %v", err)
			}
			if err := echoViaWebSocket(port, "/elsewhere", uuid, clientTLS, echoHost, echoPort); err == nil {
				t.Error("a WebSocket upgrade on the wrong path got through")
			}
			if err := echoViaWebSocket(port, "/trk01b", "11111111-1111-1111-1111-111111111111", clientTLS, echoHost, echoPort); err == nil {
				t.Error("an unknown UUID got through")
			}
			if !withTLS {
				// A plain-TCP VLESS client is exactly what this inbound must
				// no longer answer like.
				if err := echoVia(port, uuid, echoHost, echoPort); err == nil {
					t.Error("a plain-TCP VLESS client got through a ws inbound")
				}
			}
		})
	}
}

func TestBuildInboundTransport(t *testing.T) {
	ws, err := buildInboundTransport(inboundSpec{Tag: "a", Transport: &transportSpec{Type: "ws", Path: "/p"}})
	if err != nil || ws == nil || ws.Type != "ws" || ws.WebsocketOptions.Path != "/p" {
		t.Errorf("ws = %+v, %v", ws, err)
	}
	hu, err := buildInboundTransport(inboundSpec{Tag: "b", Transport: &transportSpec{Type: "httpupgrade", Path: "/u", Host: "h.example"}})
	if err != nil || hu == nil || hu.Type != "httpupgrade" || hu.HTTPUpgradeOptions.Path != "/u" || hu.HTTPUpgradeOptions.Host != "h.example" {
		t.Errorf("httpupgrade = %+v, %v", hu, err)
	}
	if none, err := buildInboundTransport(inboundSpec{Tag: "c"}); none != nil || err != nil {
		t.Errorf("no transport = %+v, %v", none, err)
	}
	if _, err := buildInboundTransport(inboundSpec{Tag: "d", Transport: &transportSpec{Type: "xhttp"}}); err == nil {
		t.Error("an unsupported transport was accepted")
	}
	if _, _, err := buildOptionsPlan(startRequest{Inbounds: []inboundSpec{{Tag: "d", Protocol: "vless", ListenPort: 1, Transport: &transportSpec{Type: "xhttp"}}}}); err == nil {
		t.Error("buildOptionsPlan accepted an unsupported transport")
	}
}
