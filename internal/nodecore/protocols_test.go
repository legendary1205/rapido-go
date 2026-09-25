package nodecore

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"
	boxtrojan "github.com/sagernet/sing-box/transport/trojan"
	"github.com/sagernet/sing-shadowsocks/shadowaead"
	vmessclient "github.com/sagernet/sing-vmess"
	M "github.com/sagernet/sing/common/metadata"

	"github.com/legendary1205/rapido-go/internal/nodecore/traffic"
)

const forkTag = "fork-in"

// testUser is one account of a protocol under test. secret is the UUID for
// vmess and the password for trojan and shadowsocks.
type testUser struct {
	name   string
	secret string
}

// protocolCase is everything the shared tests need to stand up one inbound
// protocol and talk to it with a real client library - the same wire code
// sing-box's own outbounds use.
type protocolCase struct {
	name     string
	protocol string
	inbound  func(listen option.ListenOptions, users []testUser) option.Inbound
	dialTCP  func(nodeAddr string, u testUser, dest M.Socksaddr) (net.Conn, error)
	dialUDP  func(nodeAddr string, u testUser, dest M.Socksaddr) (net.PacketConn, error)
}

func (pc protocolCase) users(us []testUser) []User {
	out := make([]User, len(us))
	for i, u := range us {
		out[i] = User{Name: u.name}
		if pc.protocol == "vmess" {
			out[i].UUID = u.secret
		} else {
			out[i].Password = u.secret
		}
	}
	return out
}

func (pc protocolCase) options(port int, us []testUser) option.Options {
	listen := badoptionAddr("127.0.0.1")
	return option.Options{
		Inbounds: []option.Inbound{pc.inbound(option.ListenOptions{Listen: &listen, ListenPort: uint16(port)}, us)},
		Outbounds: []option.Outbound{
			{Type: "direct", Tag: "direct-out", Options: &option.DirectOutboundOptions{}},
		},
		Route: &option.RouteOptions{Final: "direct-out"},
	}
}

func dialRaw(nodeAddr string) (net.Conn, error) {
	return net.DialTimeout("tcp", nodeAddr, 2*time.Second)
}

func shadowsocksCase(method string) protocolCase {
	return protocolCase{
		name:     "shadowsocks-" + method,
		protocol: "shadowsocks",
		inbound: func(listen option.ListenOptions, us []testUser) option.Inbound {
			users := make([]option.ShadowsocksUser, len(us))
			for i, u := range us {
				users[i] = option.ShadowsocksUser{Name: u.name, Password: u.secret}
			}
			return option.Inbound{Type: "shadowsocks", Tag: forkTag, Options: &option.ShadowsocksInboundOptions{
				ListenOptions: listen,
				Method:        method,
				Users:         users,
			}}
		},
		dialTCP: func(nodeAddr string, u testUser, dest M.Socksaddr) (net.Conn, error) {
			raw, err := dialRaw(nodeAddr)
			if err != nil {
				return nil, err
			}
			m, err := shadowaead.New(method, nil, u.secret)
			if err != nil {
				raw.Close()
				return nil, err
			}
			conn, err := m.DialConn(raw, dest)
			if err != nil {
				raw.Close()
				return nil, err
			}
			return conn, nil
		},
		dialUDP: func(nodeAddr string, u testUser, dest M.Socksaddr) (net.PacketConn, error) {
			addr, err := net.ResolveUDPAddr("udp", nodeAddr)
			if err != nil {
				return nil, err
			}
			raw, err := net.DialUDP("udp", nil, addr)
			if err != nil {
				return nil, err
			}
			m, err := shadowaead.New(method, nil, u.secret)
			if err != nil {
				raw.Close()
				return nil, err
			}
			return m.DialPacketConn(raw), nil
		},
	}
}

func protocolCases() []protocolCase {
	vmess := protocolCase{
		name:     "vmess",
		protocol: "vmess",
		inbound: func(listen option.ListenOptions, us []testUser) option.Inbound {
			users := make([]option.VMessUser, len(us))
			for i, u := range us {
				users[i] = option.VMessUser{Name: u.name, UUID: u.secret}
			}
			return option.Inbound{Type: "vmess", Tag: forkTag, Options: &option.VMessInboundOptions{ListenOptions: listen, Users: users}}
		},
		dialTCP: func(nodeAddr string, u testUser, dest M.Socksaddr) (net.Conn, error) {
			raw, err := dialRaw(nodeAddr)
			if err != nil {
				return nil, err
			}
			client, err := vmessclient.NewClient(u.secret, "aes-128-gcm", 0)
			if err != nil {
				raw.Close()
				return nil, err
			}
			conn, err := client.DialConn(raw, dest)
			if err != nil {
				raw.Close()
				return nil, err
			}
			return conn, nil
		},
		dialUDP: func(nodeAddr string, u testUser, dest M.Socksaddr) (net.PacketConn, error) {
			raw, err := dialRaw(nodeAddr)
			if err != nil {
				return nil, err
			}
			client, err := vmessclient.NewClient(u.secret, "aes-128-gcm", 0)
			if err != nil {
				raw.Close()
				return nil, err
			}
			conn, err := client.DialPacketConn(raw, dest)
			if err != nil {
				raw.Close()
				return nil, err
			}
			return conn, nil
		},
	}
	trojan := protocolCase{
		name:     "trojan",
		protocol: "trojan",
		inbound: func(listen option.ListenOptions, us []testUser) option.Inbound {
			users := make([]option.TrojanUser, len(us))
			for i, u := range us {
				users[i] = option.TrojanUser{Name: u.name, Password: u.secret}
			}
			return option.Inbound{Type: "trojan", Tag: forkTag, Options: &option.TrojanInboundOptions{ListenOptions: listen, Users: users}}
		},
		dialTCP: func(nodeAddr string, u testUser, dest M.Socksaddr) (net.Conn, error) {
			raw, err := dialRaw(nodeAddr)
			if err != nil {
				return nil, err
			}
			return boxtrojan.NewClientConn(raw, boxtrojan.Key(u.secret), dest), nil
		},
		dialUDP: func(nodeAddr string, u testUser, dest M.Socksaddr) (net.PacketConn, error) {
			raw, err := dialRaw(nodeAddr)
			if err != nil {
				return nil, err
			}
			return boxtrojan.NewClientPacketConn(raw, boxtrojan.Key(u.secret)), nil
		},
	}
	return []protocolCase{
		vmess,
		trojan,
		shadowsocksCase("chacha20-ietf-poly1305"),
		shadowsocksCase("aes-256-gcm"),
	}
}

func eachProtocol(t *testing.T, fn func(t *testing.T, pc protocolCase)) {
	for _, pc := range protocolCases() {
		t.Run(pc.name, func(t *testing.T) { fn(t, pc) })
	}
}

// forkedNode is a running Node with one inbound of the protocol under test and
// a TCP and a UDP echo server to proxy to.
type forkedNode struct {
	pc      protocolCase
	node    *Node
	mgr     *traffic.Manager
	addr    string
	tcpDest M.Socksaddr
	udpDest *net.UDPAddr
}

func startForkedNode(t *testing.T, pc protocolCase, users ...testUser) *forkedNode {
	t.Helper()
	echoHost, echoPort, err := net.SplitHostPort(startEchoServer(t))
	if err != nil {
		t.Fatalf("split echo address: %v", err)
	}
	port, err := strconv.Atoi(echoPort)
	if err != nil {
		t.Fatalf("parse echo port: %v", err)
	}
	nodePort := freePort(t)
	mgr := traffic.NewManager()
	node, err := New(context.Background(), pc.options(nodePort, users), mgr)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := node.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { node.Close() })
	return &forkedNode{
		pc:      pc,
		node:    node,
		mgr:     mgr,
		addr:    net.JoinHostPort("127.0.0.1", strconv.Itoa(nodePort)),
		tcpDest: M.ParseSocksaddrHostPort(echoHost, uint16(port)),
		udpDest: startUDPEchoServer(t),
	}
}

func startUDPEchoServer(t *testing.T) *net.UDPAddr {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp echo server: %v", err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 65535)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			pc.WriteTo(buf[:n], addr)
		}
	}()
	return pc.LocalAddr().(*net.UDPAddr)
}

func (f *forkedNode) update(t *testing.T, users ...testUser) {
	t.Helper()
	if err := f.node.UpdateUsers(forkTag, f.pc.protocol, f.pc.users(users)); err != nil {
		t.Fatalf("UpdateUsers: %v", err)
	}
}

func (f *forkedNode) dial(t *testing.T, u testUser) net.Conn {
	t.Helper()
	conn, err := f.pc.dialTCP(f.addr, u, f.tcpDest)
	if err != nil {
		t.Fatalf("dial as %q: %v", u.name, err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// echoOnce is echoRoundTrip without the testing.T, for worker goroutines.
func echoOnce(conn net.Conn, payload []byte) error {
	if _, err := conn.Write(payload); err != nil {
		return err
	}
	got := make([]byte, len(payload))
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(conn, got); err != nil {
		return err
	}
	if !bytes.Equal(got, payload) {
		return fmt.Errorf("echo mismatch: got %d bytes that differ from the %d sent", len(got), len(payload))
	}
	return nil
}

func (f *forkedNode) requireRejected(t *testing.T, u testUser) {
	t.Helper()
	conn, err := f.pc.dialTCP(f.addr, u, f.tcpDest)
	if err != nil {
		return
	}
	defer conn.Close()
	if err := echoOnce(conn, []byte("should-not-work")); err == nil {
		t.Fatalf("user %q was proxied, want a rejection", u.name)
	}
}

func udpEchoOnce(conn net.PacketConn, dest net.Addr, payload []byte) error {
	if _, err := conn.WriteTo(payload, dest); err != nil {
		return err
	}
	got := make([]byte, 2048)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := conn.ReadFrom(got)
	if err != nil {
		return err
	}
	if !bytes.Equal(got[:n], payload) {
		return fmt.Errorf("udp echo mismatch: got %d bytes that differ from the %d sent", n, len(payload))
	}
	return nil
}

func testSecret(pc protocolCase, n int) string {
	if pc.protocol == "vmess" {
		return fmt.Sprintf("8f8a4c1e-1e2a-4b8a-9b1a-0000000001%02d", n)
	}
	return fmt.Sprintf("secret-%d", n)
}

// TestForkedProtocolsRoundTrip: a real client of each protocol authenticates
// and moves data through a real node, over TCP and over UDP, and a client with
// the wrong secret is refused.
func TestForkedProtocolsRoundTrip(t *testing.T) {
	eachProtocol(t, func(t *testing.T, pc protocolCase) {
		alice := testUser{"alice", testSecret(pc, 1)}
		f := startForkedNode(t, pc, alice)

		conn := f.dial(t, alice)
		if err := echoRoundTrip(t, conn, "hello-through-the-fork"); err != nil {
			t.Fatalf("tcp round trip: %v", err)
		}

		pconn, err := pc.dialUDP(f.addr, alice, M.SocksaddrFromNet(f.udpDest))
		if err != nil {
			t.Fatalf("dial udp: %v", err)
		}
		t.Cleanup(func() { pconn.Close() })
		if err := udpEchoOnce(pconn, f.udpDest, []byte("udp-through-the-fork")); err != nil {
			t.Fatalf("udp round trip: %v", err)
		}

		f.requireRejected(t, testUser{"mallory", testSecret(pc, 99)})
	})
}

// TestForkedProtocolsHotUpdate: a user added by UpdateUsers can connect while a
// connection opened before the update keeps working, a user not yet added is
// refused, and a removed user is refused on new connections.
func TestForkedProtocolsHotUpdate(t *testing.T) {
	eachProtocol(t, func(t *testing.T, pc protocolCase) {
		alice := testUser{"alice", testSecret(pc, 1)}
		bob := testUser{"bob", testSecret(pc, 2)}
		f := startForkedNode(t, pc, alice)

		connA := f.dial(t, alice)
		if err := echoRoundTrip(t, connA, "alice-before"); err != nil {
			t.Fatalf("alice before update: %v", err)
		}
		f.requireRejected(t, bob)

		f.update(t, alice, bob)

		if err := echoRoundTrip(t, connA, "alice-after"); err != nil {
			t.Fatalf("alice's open connection broke across the update: %v", err)
		}
		connB := f.dial(t, bob)
		if err := echoRoundTrip(t, connB, "bob-after"); err != nil {
			t.Fatalf("hot-added bob: %v", err)
		}
		if err := echoRoundTrip(t, f.dial(t, alice), "alice-new-connection"); err != nil {
			t.Fatalf("alice's new connection after the update: %v", err)
		}

		f.update(t, alice)

		f.requireRejected(t, bob)
		if err := echoRoundTrip(t, connA, "alice-after-removal"); err != nil {
			t.Fatalf("alice's open connection broke when bob was removed: %v", err)
		}
		if err := echoRoundTrip(t, f.dial(t, alice), "alice-still-fine"); err != nil {
			t.Fatalf("alice's new connection after bob's removal: %v", err)
		}
	})
}

// TestForkedProtocolsStartWithNoUsers: an inbound built with an empty user
// list must still be hot-updatable. For shadowsocks that is the case upstream
// gets wrong - it would build the single-user service.
func TestForkedProtocolsStartWithNoUsers(t *testing.T) {
	eachProtocol(t, func(t *testing.T, pc protocolCase) {
		alice := testUser{"alice", testSecret(pc, 1)}
		f := startForkedNode(t, pc)

		f.requireRejected(t, alice)
		f.update(t, alice)
		if err := echoRoundTrip(t, f.dial(t, alice), "hello"); err != nil {
			t.Fatalf("user added to an inbound that started empty: %v", err)
		}
	})
}

// drainUntil accumulates Drain() until want has been reached for user (or two
// seconds pass), then waits a little longer and drains once more, so that a
// counter that over-counts cannot hide behind the moment we stopped looking.
func drainUntil(t *testing.T, mgr *traffic.Manager, user string, want traffic.Usage) map[string]traffic.Usage {
	t.Helper()
	total := make(map[string]traffic.Usage)
	collect := func() {
		for name, u := range mgr.Drain() {
			cur := total[name]
			cur.Up += u.Up
			cur.Down += u.Down
			total[name] = cur
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		collect()
		if got := total[user]; got.Up >= want.Up && got.Down >= want.Down {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond)
	collect()
	return total
}

// TestForkedProtocolsTrafficExact: after echo round trips, Drain reports for
// the user exactly the payload bytes the client wrote (Up) and read back
// (Down) - no wire framing, no double counting - and nothing for anyone else.
func TestForkedProtocolsTrafficExact(t *testing.T) {
	eachProtocol(t, func(t *testing.T, pc protocolCase) {
		alice := testUser{"alice", testSecret(pc, 1)}
		bob := testUser{"bob", testSecret(pc, 2)}
		f := startForkedNode(t, pc, alice, bob)

		var wantAlice traffic.Usage
		conn := f.dial(t, alice)
		for _, size := range []int{24, 5000, 16000} {
			if err := echoOnce(conn, bytes.Repeat([]byte{'a'}, size)); err != nil {
				t.Fatalf("alice tcp round trip of %d bytes: %v", size, err)
			}
			wantAlice.Up += int64(size)
			wantAlice.Down += int64(size)
		}

		connB := f.dial(t, bob)
		if err := echoOnce(connB, bytes.Repeat([]byte{'b'}, 777)); err != nil {
			t.Fatalf("bob tcp round trip: %v", err)
		}
		wantBob := traffic.Usage{Up: 777, Down: 777}

		got := drainUntil(t, f.mgr, "alice", wantAlice)
		if got["alice"] != wantAlice {
			t.Errorf("alice tcp usage = %+v, want %+v", got["alice"], wantAlice)
		}
		if got["bob"] != wantBob {
			t.Errorf("bob tcp usage = %+v, want %+v", got["bob"], wantBob)
		}
		if len(got) != 2 {
			t.Errorf("usage has entries beyond alice and bob: %+v", got)
		}

		pconn, err := pc.dialUDP(f.addr, alice, M.SocksaddrFromNet(f.udpDest))
		if err != nil {
			t.Fatalf("dial udp: %v", err)
		}
		t.Cleanup(func() { pconn.Close() })
		wantUDP := traffic.Usage{}
		for _, size := range []int{31, 1200} {
			if err := udpEchoOnce(pconn, f.udpDest, bytes.Repeat([]byte{'u'}, size)); err != nil {
				t.Fatalf("alice udp round trip of %d bytes: %v", size, err)
			}
			wantUDP.Up += int64(size)
			wantUDP.Down += int64(size)
		}
		got = drainUntil(t, f.mgr, "alice", wantUDP)
		if got["alice"] != wantUDP {
			t.Errorf("alice udp usage = %+v, want %+v", got["alice"], wantUDP)
		}
		if len(got) != 1 {
			t.Errorf("udp usage has entries beyond alice: %+v", got)
		}
	})
}

// TestForkedProtocolsUnnamedUsersCountByIndex: a user without a name is
// accounted under its position in the list, as the VLESS inbound always has.
func TestForkedProtocolsUnnamedUsersCountByIndex(t *testing.T) {
	eachProtocol(t, func(t *testing.T, pc protocolCase) {
		first := testUser{"", testSecret(pc, 1)}
		second := testUser{"", testSecret(pc, 2)}
		f := startForkedNode(t, pc, first, second)

		if err := echoOnce(f.dial(t, second), bytes.Repeat([]byte{'x'}, 100)); err != nil {
			t.Fatalf("second user round trip: %v", err)
		}
		got := drainUntil(t, f.mgr, "1", traffic.Usage{Up: 100, Down: 100})
		if want := (traffic.Usage{Up: 100, Down: 100}); got["1"] != want || len(got) != 1 {
			t.Errorf("usage = %+v, want only {\"1\": %+v}", got, want)
		}
	})
}

// TestForkedProtocolsUpdateNeverMisbillsUsers hammers the inbound with
// connections while UpdateUsers keeps reordering (and resizing) the user list.
// Every user is in every list, so a connection is never legitimately refused
// for want of a user; what must never happen is bytes landing on another
// user's account, or a panic from a stale index. A connection may occasionally
// fail its handshake in the instant a list is swapped - that is not billed,
// so the successes alone must add up exactly.
func TestForkedProtocolsUpdateNeverMisbillsUsers(t *testing.T) {
	eachProtocol(t, func(t *testing.T, pc protocolCase) {
		alice := testUser{"alice", testSecret(pc, 1)}
		bob := testUser{"bob", testSecret(pc, 2)}
		carol := testUser{"carol", testSecret(pc, 3)}
		f := startForkedNode(t, pc, alice, bob)

		lists := [][]testUser{{alice, bob}, {bob, alice}, {carol, bob, alice}, {alice, carol, bob}}
		stopUpdates := make(chan struct{})
		var updates atomic.Int64
		var updater sync.WaitGroup
		updater.Add(1)
		go func() {
			defer updater.Done()
			for i := 0; ; i++ {
				select {
				case <-stopUpdates:
					return
				default:
				}
				if err := f.node.UpdateUsers(forkTag, pc.protocol, pc.users(lists[i%len(lists)])); err != nil {
					t.Errorf("UpdateUsers: %v", err)
					return
				}
				updates.Add(1)
				time.Sleep(time.Millisecond)
			}
		}()

		// Each attempt costs loopback ports that Windows holds for minutes, so
		// the number of connections is bounded rather than time-boxed.
		const workersPerUser, attemptsPerWorker = 2, 25
		type worker struct {
			user       testUser
			size       int
			ok, failed atomic.Int64
		}
		workers := map[string]*worker{
			"alice": {user: alice, size: 11},
			"bob":   {user: bob, size: 23},
		}
		var clients sync.WaitGroup
		for _, w := range workers {
			for range workersPerUser {
				clients.Add(1)
				go func() {
					defer clients.Done()
					payload := bytes.Repeat([]byte{'m'}, w.size)
					for range attemptsPerWorker {
						conn, err := pc.dialTCP(f.addr, w.user, f.tcpDest)
						if err != nil {
							w.failed.Add(1)
							continue
						}
						if err := echoOnce(conn, payload); err != nil {
							w.failed.Add(1)
						} else {
							w.ok.Add(1)
						}
						conn.Close()
						time.Sleep(2 * time.Millisecond)
					}
				}()
			}
		}
		clients.Wait()
		close(stopUpdates)
		updater.Wait()

		if updates.Load() < 20 {
			t.Fatalf("only %d list updates ran, the test did not exercise the swap", updates.Load())
		}
		for name, w := range workers {
			if w.ok.Load() < workersPerUser*attemptsPerWorker/2 {
				t.Fatalf("%s completed only %d of %d round trips", name, w.ok.Load(), workersPerUser*attemptsPerWorker)
			}
		}
		t.Logf("%d updates; alice %d ok/%d failed, bob %d ok/%d failed", updates.Load(),
			workers["alice"].ok.Load(), workers["alice"].failed.Load(), workers["bob"].ok.Load(), workers["bob"].failed.Load())

		want := map[string]traffic.Usage{}
		for name, w := range workers {
			n := int64(w.size) * w.ok.Load()
			want[name] = traffic.Usage{Up: n, Down: n}
		}
		got := drainUntil(t, f.mgr, "alice", want["alice"])
		for name, w := range want {
			if got[name] != w {
				t.Errorf("%s usage = %+v, want %+v", name, got[name], w)
			}
		}
		if len(got) != 2 {
			t.Errorf("usage has entries beyond alice and bob: %+v", got)
		}
	})
}

// TestShadowsocksUDPSurvivesUserUpdate: an update swaps the whole service (see
// the fork's doc comment), so a UDP client that was mid-session must simply
// carry on with its next packet.
func TestShadowsocksUDPSurvivesUserUpdate(t *testing.T) {
	pc := shadowsocksCase("chacha20-ietf-poly1305")
	alice := testUser{"alice", testSecret(pc, 1)}
	bob := testUser{"bob", testSecret(pc, 2)}
	f := startForkedNode(t, pc, alice)

	dest := M.SocksaddrFromNet(f.udpDest)
	pa, err := pc.dialUDP(f.addr, alice, dest)
	if err != nil {
		t.Fatalf("dial udp: %v", err)
	}
	t.Cleanup(func() { pa.Close() })
	if err := udpEchoOnce(pa, f.udpDest, []byte("before")); err != nil {
		t.Fatalf("udp before update: %v", err)
	}

	f.update(t, alice, bob)

	if err := udpEchoOnce(pa, f.udpDest, []byte("after")); err != nil {
		t.Fatalf("udp session did not survive the update: %v", err)
	}
	pb, err := pc.dialUDP(f.addr, bob, dest)
	if err != nil {
		t.Fatalf("dial udp as bob: %v", err)
	}
	t.Cleanup(func() { pb.Close() })
	if err := udpEchoOnce(pb, f.udpDest, []byte("bob")); err != nil {
		t.Fatalf("hot-added bob over udp: %v", err)
	}
}

func TestShadowsocksInboundMethodValidation(t *testing.T) {
	cases := []struct {
		method  string
		wantErr string
	}{
		{"2022-blake3-aes-128-gcm", "server key"},
		{"2022-blake3-chacha20-poly1305", "server key"},
		{"none", "unsupported method"},
		{"rot13", "unsupported method"},
		{"", "unsupported method"},
		{"chacha20-ietf-poly1305", ""},
	}
	for _, c := range cases {
		t.Run(c.method, func(t *testing.T) {
			pc := shadowsocksCase(c.method)
			node, err := New(context.Background(), pc.options(freePort(t), nil), traffic.NewManager())
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("New: %v", err)
				}
				node.Close()
				return
			}
			if err == nil {
				node.Close()
				t.Fatalf("New accepted method %q, want an error mentioning %q", c.method, c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("error = %q, want it to mention %q", err, c.wantErr)
			}
		})
	}
}

func TestUpdateUsersRejectsBadTargets(t *testing.T) {
	trojan := protocolCases()[1]
	alice := testUser{"alice", "pw-alice"}
	f := startForkedNode(t, trojan, alice)
	users := trojan.users([]testUser{alice})

	if err := f.node.UpdateUsers("no-such-tag", "trojan", users); err == nil {
		t.Error("updating an unknown tag succeeded")
	}
	if err := f.node.UpdateUsers(forkTag, "vmess", users); err == nil || !strings.Contains(err.Error(), "not a VMess inbound") {
		t.Errorf("updating a trojan inbound as vmess: err = %v, want a type mismatch", err)
	}
	if err := f.node.UpdateUsers(forkTag, "hysteria2", users); err == nil || !strings.Contains(err.Error(), "unsupported protocol") {
		t.Errorf("unknown protocol: err = %v, want unsupported protocol", err)
	}

	// Two users sharing a password is refused, and refusal must leave the
	// running list exactly as it was.
	clash := trojan.users([]testUser{alice, {"bob", "pw-alice"}})
	if err := f.node.UpdateUsers(forkTag, "trojan", clash); err == nil {
		t.Fatal("a duplicate password was accepted")
	}
	if err := echoRoundTrip(t, f.dial(t, alice), "still-alice"); err != nil {
		t.Fatalf("the failed update disturbed the running user list: %v", err)
	}
}
