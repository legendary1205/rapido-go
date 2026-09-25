package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	boxtrojan "github.com/sagernet/sing-box/transport/trojan"
	"github.com/sagernet/sing-shadowsocks/shadowaead"
	vmessclient "github.com/sagernet/sing-vmess"
	M "github.com/sagernet/sing/common/metadata"

	"github.com/legendary1205/rapido-go/internal/nodecore/traffic"
)

// protoFixture is one inbound protocol as the panel describes it, plus enough
// to speak to it with a real client library.
type protoFixture struct {
	name     string
	protocol string
	method   string // shadowsocks only; "" leaves the choice to the node
}

func protoFixtures() []protoFixture {
	return []protoFixture{
		{"vmess", "vmess", ""},
		{"trojan", "trojan", ""},
		{"shadowsocks-default-cipher", "shadowsocks", ""},
		{"shadowsocks-aes-256-gcm", "shadowsocks", "aes-256-gcm"},
	}
}

// user returns the n'th account of the fixture's protocol.
func (f protoFixture) user(name string, n int) userSpec {
	switch f.protocol {
	case "vmess":
		return userSpec{Name: name, UUID: fmt.Sprintf("8f8a4c1e-1e2a-4b8a-9b1a-0000000002%02d", n)}
	default:
		return userSpec{Name: name, Password: fmt.Sprintf("pw-%s-%d", f.protocol, n), Method: f.method}
	}
}

// clientMethod is the cipher the client must use: what the fixture names, or
// the node's default.
func (f protoFixture) clientMethod() string {
	if f.method != "" {
		return f.method
	}
	return defaultShadowsocksMethod
}

func (f protoFixture) inbound(tag string, ports []uint16, users ...userSpec) inboundSpec {
	in := inboundSpec{Tag: tag, Protocol: f.protocol, ListenPort: ports[0], Users: users}
	if len(ports) > 1 {
		in.ListenPorts = ports
	}
	return in
}

// dialProxied opens a real client connection of the fixture's protocol to a
// node listener, asking it to proxy to the echo server.
func (f protoFixture) dialProxied(nodePort uint16, u userSpec, echoHost string, echoPort int) (net.Conn, error) {
	raw, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(nodePort))), 2*time.Second)
	if err != nil {
		return nil, err
	}
	dest := M.Socksaddr{Addr: netip.MustParseAddr(echoHost), Port: uint16(echoPort)}
	var conn net.Conn
	switch f.protocol {
	case "vmess":
		client, cerr := vmessclient.NewClient(u.UUID, "aes-128-gcm", 0)
		if cerr != nil {
			raw.Close()
			return nil, cerr
		}
		conn, err = client.DialConn(raw, dest)
	case "trojan":
		conn = boxtrojan.NewClientConn(raw, boxtrojan.Key(u.Password), dest)
	case "shadowsocks":
		method, cerr := shadowaead.New(f.clientMethod(), nil, u.Password)
		if cerr != nil {
			raw.Close()
			return nil, cerr
		}
		conn, err = method.DialConn(raw, dest)
	default:
		err = fmt.Errorf("no test client for %q", f.protocol)
	}
	if err != nil {
		raw.Close()
		return nil, err
	}
	return conn, nil
}

func echoConn(conn net.Conn, payload string) error {
	if _, err := conn.Write([]byte(payload)); err != nil {
		return err
	}
	got := make([]byte, len(payload))
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(conn, got); err != nil {
		return err
	}
	if string(got) != payload {
		return fmt.Errorf("echo mismatch: got %q, want %q", got, payload)
	}
	return nil
}

// echoProxied is one whole request through the node: nil means the payload
// came back intact.
func (f protoFixture) echoProxied(nodePort uint16, u userSpec, echoHost string, echoPort int) error {
	conn, err := f.dialProxied(nodePort, u, echoHost, echoPort)
	if err != nil {
		return err
	}
	defer conn.Close()
	return echoConn(conn, "ping-through-the-node")
}

// TestEveryProtocolWorksThroughBuildOptionsPlan takes a panel-shaped inbound of
// each protocol - single-port and multi-port - through buildOptionsPlan into a
// real nodecore.New/Start, talks to every listener with a real client, and
// hot-updates the users the way the node's own paths do, listener by listener.
func TestEveryProtocolWorksThroughBuildOptionsPlan(t *testing.T) {
	for _, f := range protoFixtures() {
		for _, layout := range []struct {
			name  string
			ports int
		}{{"single-port", 1}, {"multi-port", 2}} {
			t.Run(f.name+"/"+layout.name, func(t *testing.T) {
				echoHost, echoPort := startEcho(t)
				var ports []uint16
				for range layout.ports {
					ports = append(ports, freeTCPPort(t))
				}
				alice, bob := f.user("alice", 1), f.user("bob", 2)
				node, plan := startPlanned(t, startRequest{Inbounds: []inboundSpec{f.inbound("main", ports, alice)}})

				if got := len(plan.listenerTags("main")); layout.ports > 1 && got != layout.ports {
					t.Fatalf("listenerTags(main) has %d entries, want one per port (%d)", got, layout.ports)
				}
				if f.protocol == "shadowsocks" {
					if got, want := plan.shadowsocksMethods["main"], f.clientMethod(); got != want {
						t.Fatalf("plan records cipher %q, want %q", got, want)
					}
				}

				each := func(check func(port uint16)) {
					for _, p := range ports {
						check(p)
					}
				}
				each(func(p uint16) {
					if err := f.echoProxied(p, alice, echoHost, echoPort); err != nil {
						t.Errorf("alice on port %d: %v", p, err)
					}
					if err := f.echoProxied(p, bob, echoHost, echoPort); err == nil {
						t.Errorf("bob is not registered yet but got through on port %d", p)
					}
				})

				update := func(users ...userSpec) {
					for _, listener := range plan.listenerTags("main") {
						if err := node.UpdateUsers(listener, f.protocol, nodeUsers(users)); err != nil {
							t.Fatalf("UpdateUsers(%s): %v", listener, err)
						}
					}
				}
				update(alice, bob)
				each(func(p uint16) {
					if err := f.echoProxied(p, bob, echoHost, echoPort); err != nil {
						t.Errorf("hot-added bob on port %d: %v", p, err)
					}
				})

				update(alice)
				each(func(p uint16) {
					if err := f.echoProxied(p, bob, echoHost, echoPort); err == nil {
						t.Errorf("removed bob still got through on port %d", p)
					}
					if err := f.echoProxied(p, alice, echoHost, echoPort); err != nil {
						t.Errorf("alice on port %d after bob's removal: %v", p, err)
					}
				})
			})
		}
	}
}

func TestShadowsocksCipherIsCheckedBeforeTheNodeStarts(t *testing.T) {
	build := func(method string) error {
		_, _, err := buildOptionsPlan(startRequest{Inbounds: []inboundSpec{{
			Tag: "ss", Protocol: "shadowsocks", ListenPort: 8388,
			Users: []userSpec{{Name: "a", Password: "p", Method: method}},
		}}})
		return err
	}
	if err := build(""); err != nil {
		t.Errorf("no cipher named: %v, want the classic default to be used", err)
	}
	if err := build("aes-128-gcm"); err != nil {
		t.Errorf("classic cipher rejected: %v", err)
	}
	if err := build("2022-blake3-aes-128-gcm"); err == nil || !strings.Contains(err.Error(), "server key") {
		t.Errorf("2022 cipher: err = %v, want a clear refusal mentioning the server key", err)
	}
	if err := build("rot13"); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("unknown cipher: err = %v, want unsupported", err)
	}

	// The same refusal must reach POST /start's caller as a 422, not surface
	// later as a failed start.
	body, _ := json.Marshal(startRequest{Inbounds: []inboundSpec{{
		Tag: "ss", Protocol: "shadowsocks", ListenPort: 8388,
		Users: []userSpec{{Name: "a", Password: "p", Method: "2022-blake3-aes-128-gcm"}},
	}}})
	rec := httptest.NewRecorder()
	(&server{logger: quietLogger()}).handleStart(rec, httptest.NewRequest(http.MethodPost, "/start", bytes.NewReader(body)))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("POST /start with a 2022 cipher = %d, want 422", rec.Code)
	}
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestServer(t *testing.T) *server {
	t.Helper()
	srv := &server{logger: quietLogger(), ctx: context.Background(), traffic: traffic.NewManager()}
	t.Cleanup(func() {
		srv.mu.Lock()
		srv.stopNodeLocked()
		srv.mu.Unlock()
	})
	return srv
}

func putUsers(t *testing.T, srv *server, tag, protocol string, users ...userSpec) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /inbounds/{tag}/users", srv.handleUpdateUsers)
	body, _ := json.Marshal(updateUsersRequest{Protocol: protocol, Users: users})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/inbounds/"+tag+"/users", bytes.NewReader(body)))
	return rec
}

func TestPutInboundUsersHotUpdatesEveryProtocol(t *testing.T) {
	for _, f := range protoFixtures() {
		t.Run(f.name, func(t *testing.T) {
			echoHost, echoPort := startEcho(t)
			p1, p2 := freeTCPPort(t), freeTCPPort(t)
			alice, bob := f.user("alice", 1), f.user("bob", 2)

			srv := newTestServer(t)
			if rec := putUsers(t, srv, "main", f.protocol, alice); rec.Code != http.StatusConflict {
				t.Fatalf("PUT before the core started = %d, want 409", rec.Code)
			}
			srv.mu.Lock()
			err := srv.startNodeLocked(startRequest{Inbounds: []inboundSpec{f.inbound("main", []uint16{p1, p2}, alice)}})
			srv.mu.Unlock()
			if err != nil {
				t.Fatalf("start: %v", err)
			}

			if err := f.echoProxied(p2, bob, echoHost, echoPort); err == nil {
				t.Fatal("bob got through before being added")
			}
			if rec := putUsers(t, srv, "main", f.protocol, alice, bob); rec.Code != http.StatusOK {
				t.Fatalf("PUT = %d %s, want 200", rec.Code, rec.Body)
			}
			for _, p := range []uint16{p1, p2} {
				if err := f.echoProxied(p, bob, echoHost, echoPort); err != nil {
					t.Errorf("hot-added bob on port %d: %v", p, err)
				}
			}

			if rec := putUsers(t, srv, "main", "wireguard", alice); rec.Code != http.StatusUnprocessableEntity {
				t.Errorf("PUT with an unsupported protocol = %d, want 422", rec.Code)
			}
			other := "trojan"
			if f.protocol == "trojan" {
				other = "vmess"
			}
			if rec := putUsers(t, srv, "main", other, alice); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "inbound") {
				t.Errorf("PUT naming the wrong protocol = %d %s, want a 422 explaining the mismatch", rec.Code, rec.Body)
			}
			if rec := putUsers(t, srv, "no-such-inbound", f.protocol, alice); rec.Code != http.StatusUnprocessableEntity {
				t.Errorf("PUT for an unknown inbound = %d, want 422", rec.Code)
			}
		})
	}
}

func TestPutInboundUsersRefusesToChangeAShadowsocksCipher(t *testing.T) {
	f := protoFixture{protocol: "shadowsocks", method: "aes-128-gcm"}
	port := freeTCPPort(t)
	srv := newTestServer(t)
	srv.mu.Lock()
	err := srv.startNodeLocked(startRequest{Inbounds: []inboundSpec{f.inbound("ss", []uint16{port}, f.user("a", 1))}})
	srv.mu.Unlock()
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	changed := userSpec{Name: "a", Password: "pw", Method: "aes-256-gcm"}
	if rec := putUsers(t, srv, "ss", "shadowsocks", changed); rec.Code != http.StatusConflict {
		t.Errorf("PUT with a different cipher = %d %s, want 409", rec.Code, rec.Body)
	}
	same := userSpec{Name: "b", Password: "pw", Method: "aes-128-gcm"}
	if rec := putUsers(t, srv, "ss", "shadowsocks", same); rec.Code != http.StatusOK {
		t.Errorf("PUT with the running cipher = %d %s, want 200", rec.Code, rec.Body)
	}
}

// fakePanel serves GET /api/internal/node-config the way the panel does: with
// an ETag when etags is set, answering 304 to a matching If-None-Match.
type fakePanel struct {
	mu    sync.Mutex
	cfg   pulledConfig
	etags bool
	seen  []string // the If-None-Match of each request, "" when absent
}

func (p *fakePanel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen = append(p.seen, r.Header.Get("If-None-Match"))
	if r.Header.Get("Authorization") != "Bearer secret" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if p.etags {
		etag := `"` + p.cfg.Version + `"`
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	json.NewEncoder(w).Encode(p.cfg)
}

func (p *fakePanel) set(cfg pulledConfig) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg = cfg
}

func (p *fakePanel) requests() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.seen...)
}

func pullFrom(t *testing.T, panel *fakePanel) (pull func(), srv *server) {
	t.Helper()
	ts := httptest.NewServer(panel)
	t.Cleanup(ts.Close)
	srv = newTestServer(t)
	cfg := config{PanelURL: ts.URL, ReportSecret: "secret"}
	return func() { srv.pullOnce(context.Background(), ts.Client(), cfg) }, srv
}

func TestPullOnceSendsTheLastVersionAndTreatsNotModifiedAsNoChange(t *testing.T) {
	panel := &fakePanel{etags: true, cfg: pulledConfig{Version: "v1"}}
	pull, srv := pullFrom(t, panel)

	pull()
	if srv.lastPulled == nil || srv.lastPulled.Version != "v1" {
		t.Fatalf("after the first pull lastPulled = %+v, want v1", srv.lastPulled)
	}
	applied := srv.lastPulled

	pull() // the panel answers 304
	if srv.lastPulled != applied {
		t.Error("a 304 replaced lastPulled, want it left exactly as it was")
	}

	panel.set(pulledConfig{Version: "v2"})
	pull() // still asks with v1, so the panel answers 200 with v2
	if srv.lastPulled == nil || srv.lastPulled.Version != "v2" {
		t.Fatalf("after the panel moved to v2 lastPulled = %+v, want v2", srv.lastPulled)
	}
	pull()

	want := []string{"", `"v1"`, `"v1"`, `"v2"`}
	if got := panel.requests(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("If-None-Match sent = %q, want %q", got, want)
	}
}

// A panel that has never heard of ETags answers every pull with the full body;
// the pull loop must behave exactly as it did before conditional requests.
func TestPullOnceAgainstAPanelWithoutETagsKeepsWorking(t *testing.T) {
	echoHost, echoPort := startEcho(t)
	port := freeTCPPort(t)
	f := protoFixtures()[1]
	alice, bob := f.user("alice", 1), f.user("bob", 2)
	panel := &fakePanel{cfg: pulledConfig{Version: "v1", Inbounds: []inboundSpec{f.inbound("main", []uint16{port}, alice)}}}
	pull, srv := pullFrom(t, panel)

	pull()
	node := srv.node
	if node == nil {
		t.Fatal("the first pull did not start the node")
	}
	pull() // same version, full 200 body
	if srv.node != node {
		t.Error("an unchanged version restarted the node")
	}

	panel.set(pulledConfig{Version: "v2", Inbounds: []inboundSpec{f.inbound("main", []uint16{port}, alice, bob)}})
	pull()
	if srv.node != node {
		t.Error("a user-only change restarted the node, want a hot apply")
	}
	if srv.lastPulled.Version != "v2" {
		t.Errorf("lastPulled = %q, want v2", srv.lastPulled.Version)
	}
	if err := f.echoProxied(port, bob, echoHost, echoPort); err != nil {
		t.Errorf("bob, added by the pulled config: %v", err)
	}
	if got := panel.requests(); got[0] != "" || got[1] != `"v1"` {
		t.Errorf("If-None-Match sent = %q, want none first and then \"v1\"", got)
	}
}

// pullOnce must hot-apply a user-only change of every protocol on every
// listener of a multi-port inbound, without restarting the core and without
// touching a connection that is already open.
func TestPullOnceHotAppliesUsersOfEveryProtocolOnEveryListener(t *testing.T) {
	for _, f := range protoFixtures() {
		t.Run(f.name, func(t *testing.T) {
			echoHost, echoPort := startEcho(t)
			ports := []uint16{freeTCPPort(t), freeTCPPort(t)}
			alice, bob := f.user("alice", 1), f.user("bob", 2)
			panel := &fakePanel{etags: true, cfg: pulledConfig{Version: "v1", Inbounds: []inboundSpec{f.inbound("main", ports, alice)}}}
			pull, srv := pullFrom(t, panel)

			pull()
			node := srv.node
			if node == nil {
				t.Fatal("the first pull did not start the node")
			}
			open, err := f.dialProxied(ports[0], alice, echoHost, echoPort)
			if err != nil {
				t.Fatalf("dial alice: %v", err)
			}
			t.Cleanup(func() { open.Close() })
			if err := echoConn(open, "before"); err != nil {
				t.Fatalf("alice before the update: %v", err)
			}
			for _, p := range ports {
				if err := f.echoProxied(p, bob, echoHost, echoPort); err == nil {
					t.Fatalf("bob got through on port %d before being added", p)
				}
			}

			panel.set(pulledConfig{Version: "v2", Inbounds: []inboundSpec{f.inbound("main", ports, alice, bob)}})
			pull()

			if srv.node != node {
				t.Fatal("a user-only change restarted the node")
			}
			if srv.lastPulled.Version != "v2" {
				t.Fatalf("lastPulled = %q, want v2", srv.lastPulled.Version)
			}
			if err := echoConn(open, "after"); err != nil {
				t.Errorf("alice's open connection broke across the hot update: %v", err)
			}
			for _, p := range ports {
				if err := f.echoProxied(p, bob, echoHost, echoPort); err != nil {
					t.Errorf("bob on port %d after the hot update: %v", p, err)
				}
			}
		})
	}
}

// A hot update the inbound refuses (here two trojan users with one password)
// must not be recorded as applied: the running list is untouched, and the next
// version that is valid gets through.
func TestPullOnceKeepsThePreviousVersionWhenAHotUpdateFails(t *testing.T) {
	echoHost, echoPort := startEcho(t)
	port := freeTCPPort(t)
	f := protoFixtures()[1]
	alice, bob := f.user("alice", 1), f.user("bob", 2)
	clash := userSpec{Name: "bob", Password: alice.Password}
	panel := &fakePanel{etags: true, cfg: pulledConfig{Version: "v1", Inbounds: []inboundSpec{f.inbound("main", []uint16{port}, alice)}}}
	pull, srv := pullFrom(t, panel)

	pull()
	node := srv.node

	panel.set(pulledConfig{Version: "v2", Inbounds: []inboundSpec{f.inbound("main", []uint16{port}, alice, clash)}})
	pull()
	if srv.node != node {
		t.Fatal("a refused hot update restarted the node")
	}
	if srv.lastPulled.Version != "v1" {
		t.Errorf("lastPulled = %q after a refused update, want v1 so the next tick retries", srv.lastPulled.Version)
	}
	if err := f.echoProxied(port, alice, echoHost, echoPort); err != nil {
		t.Errorf("alice after the refused update: %v", err)
	}

	panel.set(pulledConfig{Version: "v3", Inbounds: []inboundSpec{f.inbound("main", []uint16{port}, alice, bob)}})
	pull()
	if srv.lastPulled.Version != "v3" {
		t.Fatalf("lastPulled = %q, want v3", srv.lastPulled.Version)
	}
	if err := f.echoProxied(port, bob, echoHost, echoPort); err != nil {
		t.Errorf("bob after the valid update: %v", err)
	}
}

// A restart that fails leaves the node stopped while lastPulled still names the
// previous config. Reverting the panel to that config must bring the node back
// up: the node may not claim "unchanged" (If-None-Match) or skip the equal
// version while nothing is running.
func TestPullOnceRecoversWhenTheConfigIsRevertedAfterAFailedRestart(t *testing.T) {
	echoHost, echoPort := startEcho(t)
	port := freeTCPPort(t)
	f := protoFixtures()[1]
	alice := f.user("alice", 1)
	good := pulledConfig{Version: "v1", Inbounds: []inboundSpec{f.inbound("main", []uint16{port}, alice)}}
	// a 2022 shadowsocks cipher is refused when the core is built
	bad := pulledConfig{Version: "v2", Inbounds: []inboundSpec{{
		Tag: "main", Protocol: "shadowsocks", ListenPort: port,
		Users: []userSpec{{Name: "a", Password: "x", Method: "2022-blake3-aes-128-gcm"}},
	}}}
	panel := &fakePanel{etags: true, cfg: good}
	pull, srv := pullFrom(t, panel)

	pull()
	if srv.node == nil {
		t.Fatal("the first pull did not start the node")
	}

	panel.set(bad)
	pull()
	if srv.node != nil {
		t.Fatal("setup: the bad config was expected to leave the node stopped")
	}

	panel.set(good)
	pull()
	if srv.node == nil {
		t.Fatal("reverting to the previous config did not restart the stopped node")
	}
	if err := f.echoProxied(port, alice, echoHost, echoPort); err != nil {
		t.Errorf("alice after the revert: %v", err)
	}
}
