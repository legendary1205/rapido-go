// Command node is the Rapido Go node agent: an HTTP control plane over
// mTLS, driving a sing-box instance (internal/nodecore) with a locally
// forked VLESS inbound that supports hot user add/remove with no listener
// restart - see internal/nodecore/vless's doc comment for why that fork
// exists.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"syscall"
	"time"

	sbox "github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"

	"github.com/legendary1205/rapido-go/internal/hostmetrics"
	"github.com/legendary1205/rapido-go/internal/nodecore"
	"github.com/legendary1205/rapido-go/internal/nodecore/traffic"
)

type config struct {
	ListenAddr string // control-plane HTTP address, e.g. "0.0.0.0:62051"
	CertFile   string
	KeyFile    string
	CAFile     string // the Rapido CA cert - only clients presenting a cert signed by this CA are accepted

	// PanelURL/ReportSecret are the push half of Phase 7.3's usage/health
	// reporting (see internal/httpapi/nodereport.go) - PanelURL must point
	// at the backend-singleton instance specifically, not a load-balanced
	// API pool (see that handler's own doc comment for why). ReportSecret
	// is the bearer token returned once by POST /api/node at node-creation
	// time, alongside the mTLS cert this same config already carries.
	PanelURL       string
	ReportSecret   string
	ReportInterval time.Duration
}

func loadConfig() config {
	intervalSeconds := 10
	if v, ok := os.LookupEnv("NODE_REPORT_INTERVAL_SECONDS"); ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			intervalSeconds = n
		}
	}
	cfg := config{
		ListenAddr:     getEnv("NODE_LISTEN_ADDR", "0.0.0.0:62051"),
		CertFile:       getEnv("NODE_CERT_FILE", "/etc/rapido-node/cert.pem"),
		KeyFile:        getEnv("NODE_KEY_FILE", "/etc/rapido-node/key.pem"),
		CAFile:         getEnv("NODE_CA_FILE", "/etc/rapido-node/ca.pem"),
		PanelURL:       getEnv("PANEL_URL", ""),
		ReportSecret:   getEnv("NODE_REPORT_SECRET", ""),
		ReportInterval: time.Duration(intervalSeconds) * time.Second,
	}

	// NODE_SETUP_BLOB is the one-paste alternative to hand-copying cert/key/
	// ca/report_secret into three files plus two env vars - see
	// internal/httpapi/node.go's buildNodeSetupBlob, the panel-side half of
	// this pair. Applied on every boot (writing the same bytes back out is
	// harmless), so leaving the env var set permanently in the node's own
	// service config is fine - it isn't a one-shot flag. Errors here are
	// fatal: a node started with a broken blob has no working identity at
	// all, and failing immediately with a clear reason beats limping into
	// the generic "load X509 key pair" failure a few lines later in main().
	if blob := os.Getenv("NODE_SETUP_BLOB"); blob != "" {
		if err := applyNodeSetupBlob(blob, &cfg); err != nil {
			fmt.Fprintln(os.Stderr, "NODE_SETUP_BLOB:", err)
			os.Exit(1)
		}
	}

	return cfg
}

// nodeSetupBlob mirrors internal/httpapi/node.go's own struct of the same
// name byte-for-byte (JSON field names) - kept as a separate definition
// rather than shared, matching this project's existing precedent for the
// panel/node wire-contract types (inboundSpec/coreSpec etc. below) since
// cmd/node importing internal/httpapi would be the wrong dependency
// direction.
type nodeSetupBlob struct {
	Cert     string `json:"cert"`
	Key      string `json:"key"`
	CA       string `json:"ca"`
	Secret   string `json:"secret"`
	PanelURL string `json:"panel_url,omitempty"`
}

// applyNodeSetupBlob decodes one base64 setup blob, writes its cert/key/ca
// PEMs out to cfg's own file paths (creating parent directories as needed),
// and fills in cfg.PanelURL/ReportSecret from the blob when it carries them
// - blob values win over whatever NODE_SETUP_BLOB's sibling env vars
// (PANEL_URL, NODE_REPORT_SECRET) already set in cfg, since the whole point
// is that one pasted value should be enough on its own.
func applyNodeSetupBlob(blob string, cfg *config) error {
	raw, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		return fmt.Errorf("not valid base64: %w", err)
	}
	var parsed nodeSetupBlob
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return fmt.Errorf("not a valid setup blob: %w", err)
	}
	if parsed.Cert == "" || parsed.Key == "" || parsed.CA == "" || parsed.Secret == "" {
		return fmt.Errorf("setup blob is missing cert/key/ca/secret")
	}

	writes := []struct {
		path string
		data string
		mode os.FileMode
	}{
		{cfg.CertFile, parsed.Cert, 0o644},
		{cfg.KeyFile, parsed.Key, 0o600},
		{cfg.CAFile, parsed.CA, 0o644},
	}
	for _, w := range writes {
		if err := os.MkdirAll(filepath.Dir(w.path), 0o755); err != nil {
			return fmt.Errorf("create directory for %s: %w", w.path, err)
		}
		if err := os.WriteFile(w.path, []byte(w.data), w.mode); err != nil {
			return fmt.Errorf("write %s: %w", w.path, err)
		}
	}

	cfg.ReportSecret = parsed.Secret
	if parsed.PanelURL != "" {
		cfg.PanelURL = parsed.PanelURL
	}
	return nil
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func writeJSON(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

// server holds the one sing-box instance this node runs at a time - there
// is only ever one, matching the current rapido-node's single-core model.
type server struct {
	mu     sync.Mutex
	node   *nodecore.Node
	logger *slog.Logger
	// ctx is the node's own long-lived context, NOT any HTTP request's -
	// nodecore.New must never be given an http.Request's Context(), since
	// net/http cancels that the moment the handler returns, which would
	// cascade-cancel every dial on every connection the instance ever
	// accepts afterward (only surfaced once a connection outlived the
	// original /start request, as every future outbound dial silently
	// failing with "operation was canceled").
	ctx context.Context

	// traffic persists across a stop/start cycle (unlike node itself,
	// which is nil'd on POST /stop) so a core restart never loses
	// in-flight byte counts the push loop hasn't drained yet.
	traffic *traffic.Manager

	// lastPulled is the last config successfully fetched from
	// GET /api/internal/node-config and applied (hot or full) - nil until
	// the first successful pull. Guarded by mu, same as node: applying a
	// pulled config and handling a manual POST /start/POST /stop must
	// never interleave.
	lastPulled *pulledConfig
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg := loadConfig()
	if os.Getenv("NODE_SETUP_BLOB") != "" {
		logger.Info("applied NODE_SETUP_BLOB", "cert_file", cfg.CertFile, "key_file", cfg.KeyFile, "ca_file", cfg.CAFile)
	}

	cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		logger.Error("load node certificate", "error", err)
		os.Exit(1)
	}
	caPEM, err := os.ReadFile(cfg.CAFile)
	if err != nil {
		logger.Error("read CA certificate", "error", err)
		os.Exit(1)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caPEM) {
		logger.Error("CA file did not contain a valid certificate", "path", cfg.CAFile)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := &server{logger: logger, ctx: ctx, traffic: traffic.NewManager()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", srv.handleHealth)
	mux.HandleFunc("POST /start", srv.handleStart)
	mux.HandleFunc("POST /stop", srv.handleStop)
	mux.HandleFunc("PUT /inbounds/{tag}/users", srv.handleUpdateUsers)

	if cfg.PanelURL != "" && cfg.ReportSecret != "" {
		go srv.syncLoop(ctx, cfg)
	} else {
		logger.Warn("PANEL_URL/NODE_REPORT_SECRET not set - usage/health reporting and config sync with the panel are both disabled")
	}

	httpServer := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: mux,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{cert},
			ClientAuth:   tls.RequireAndVerifyClientCert,
			ClientCAs:    caPool,
			MinVersion:   tls.VersionTLS12,
		},
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		httpServer.Shutdown(shutdownCtx)
		srv.mu.Lock()
		if srv.node != nil {
			srv.node.Close()
		}
		srv.mu.Unlock()
	}()

	logger.Info("listening", "addr", cfg.ListenAddr)
	if err := httpServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
		logger.Error("serve", "error", err)
		os.Exit(1)
	}
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	running := s.node != nil
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "core_running": running})
}

// inboundSpec is Rapido's own wire contract for one inbound, deliberately
// simpler than sing-box's full JSON schema (which needs a registry-aware
// decoder) - the node translates this into option.Options directly in Go.
// Only vless currently supports hot user updates (handleUpdateUsers below);
// vmess/trojan/shadowsocks inbounds start fine (same upstream sing-box
// packages, unmodified) but a user-list change on those requires a full
// POST /start to rebuild the instance, until they get the same fork
// treatment as vless.
type inboundSpec struct {
	Tag        string     `json:"tag"`
	Protocol   string     `json:"protocol"` // vless | vmess | trojan | shadowsocks
	ListenPort uint16     `json:"listen_port"`
	Users      []userSpec `json:"users"`
	TLS        *tlsSpec   `json:"tls,omitempty"`
}

type userSpec struct {
	Name     string `json:"name"`
	UUID     string `json:"uuid,omitempty"`     // vmess/vless
	Password string `json:"password,omitempty"` // trojan/shadowsocks
	Flow     string `json:"flow,omitempty"`     // vless
	Method   string `json:"method,omitempty"`   // shadowsocks
}

type tlsSpec struct {
	ServerName  string       `json:"server_name"`
	Certificate string       `json:"certificate"` // PEM
	Key         string       `json:"key"`         // PEM
	Reality     *realitySpec `json:"reality,omitempty"`
}

type realitySpec struct {
	PrivateKey string   `json:"private_key"`
	ShortID    []string `json:"short_id"`
	Handshake  struct {
		ServerName string `json:"server_name"`
		ServerPort uint16 `json:"server_port"`
	} `json:"handshake"`
}

// outboundSpec/routingRuleSpec/dnsServerSpec/coreSpec mirror
// internal/httpapi/coreconfig.go's outboundDTO/routingRuleDTO/
// dnsServerDTO/coreConfigDTO byte-for-byte (JSON field names) - see
// nodeConfigResponse's own doc comment in that file for why these two
// definitions have to be kept in sync by hand rather than shared.
type outboundSpec struct {
	Tag        string   `json:"tag"`
	Type       string   `json:"type"` // direct | block | socks | http | shadowsocks | vmess | trojan | vless | hysteria2 | tuic | selector | urltest
	Server     string   `json:"server,omitempty"`
	ServerPort int      `json:"server_port,omitempty"`
	Username   string   `json:"username,omitempty"`
	Password   string   `json:"password,omitempty"`
	Outbounds  []string `json:"outbounds,omitempty"` // selector/urltest member tags

	UUID              string `json:"uuid,omitempty"`
	Flow              string `json:"flow,omitempty"`
	Method            string `json:"method,omitempty"`
	Security          string `json:"security,omitempty"`
	CongestionControl string `json:"congestion_control,omitempty"`

	TLSEnabled    bool   `json:"tls_enabled,omitempty"`
	TLSServerName string `json:"tls_server_name,omitempty"`
	TLSInsecure   bool   `json:"tls_insecure,omitempty"`

	BindInterface string `json:"bind_interface,omitempty"`
}

type routingRuleSpec struct {
	Inbound       []string `json:"inbound,omitempty"`
	Domain        []string `json:"domain,omitempty"`
	DomainSuffix  []string `json:"domain_suffix,omitempty"`
	DomainKeyword []string `json:"domain_keyword,omitempty"`
	IPCIDR        []string `json:"ip_cidr,omitempty"`
	IPIsPrivate   bool     `json:"ip_is_private,omitempty"`
	Port          []int    `json:"port,omitempty"`
	PortRange     []string `json:"port_range,omitempty"`
	Network       []string `json:"network,omitempty"`
	Protocol      []string `json:"protocol,omitempty"`
	OutboundTag   string   `json:"outbound_tag"`
}

type dnsServerSpec struct {
	Tag     string `json:"tag"`
	Type    string `json:"type"` // local | udp | tcp | tls | https
	Address string `json:"address,omitempty"`
	Port    int    `json:"port,omitempty"`
	Path    string `json:"path,omitempty"`
}

// coreSpec is the fleet-wide slice of Phase 7.4's Core Config - everything
// buildOptions used to hardcode (a single direct outbound, a bare Final
// route) now comes from here instead, sourced from the panel's
// core_config table via GET /api/internal/node-config.
type coreSpec struct {
	LogLevel     string            `json:"log_level"`
	SniffEnabled bool              `json:"sniff_enabled"`
	Outbounds    []outboundSpec    `json:"outbounds"`
	RoutingRules []routingRuleSpec `json:"routing_rules"`
	DNSServers   []dnsServerSpec   `json:"dns_servers"`
}

type startRequest struct {
	Inbounds []inboundSpec `json:"inbounds"`
	// Core is optional on the manual POST /start path (nil means "no
	// custom outbounds/routing/dns - just the built-in direct/block",
	// preserving this endpoint's original behavior for anyone still using
	// it directly instead of through the pull loop below).
	Core *coreSpec `json:"core,omitempty"`
}

func (s *server) handleStart(w http.ResponseWriter, r *http.Request) {
	var req startRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": err.Error()})
		return
	}

	opts, err := buildOptions(req)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": err.Error()})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.node != nil {
		// Matches the current rapido-node's "Xray is started already"
		// contract that the panel's client checks for verbatim before
		// falling back to a restart - see app/xray/node.py.
		writeJSON(w, http.StatusConflict, map[string]any{"detail": "core is started already"})
		return
	}

	node, err := nodecore.New(s.ctx, opts, s.traffic)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	if err := node.Start(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	s.node = node
	writeJSON(w, http.StatusOK, map[string]any{"detail": "started"})
}

func (s *server) handleStop(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.node == nil {
		writeJSON(w, http.StatusOK, map[string]any{"detail": "already stopped"})
		return
	}
	if err := s.node.Close(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	s.node = nil
	writeJSON(w, http.StatusOK, map[string]any{"detail": "stopped"})
}

type updateUsersRequest struct {
	Protocol string     `json:"protocol"`
	Users    []userSpec `json:"users"`
}

func (s *server) handleUpdateUsers(w http.ResponseWriter, r *http.Request) {
	tag := r.PathValue("tag")
	var req updateUsersRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": err.Error()})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.node == nil {
		writeJSON(w, http.StatusConflict, map[string]any{"detail": "core is not started"})
		return
	}

	if req.Protocol != "vless" {
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"detail": "hot user update is only implemented for vless inbounds so far - restart with POST /start to change users on a " + req.Protocol + " inbound",
		})
		return
	}

	users := make([]sbox.VLESSUser, 0, len(req.Users))
	for _, u := range req.Users {
		users = append(users, sbox.VLESSUser{Name: u.Name, UUID: u.UUID, Flow: u.Flow})
	}
	if err := s.node.UpdateVLESSUsers(tag, users); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"detail": "updated"})
}

func buildOptions(req startRequest) (sbox.Options, error) {
	inbounds := make([]sbox.Inbound, 0, len(req.Inbounds))
	for _, in := range req.Inbounds {
		var tlsOpts *sbox.InboundTLSOptions
		if in.TLS != nil {
			tlsOpts = &sbox.InboundTLSOptions{
				Enabled:     true,
				ServerName:  in.TLS.ServerName,
				Certificate: badoption.Listable[string]{in.TLS.Certificate},
				Key:         badoption.Listable[string]{in.TLS.Key},
			}
			if in.TLS.Reality != nil {
				tlsOpts.Reality = &sbox.InboundRealityOptions{
					Enabled:    true,
					PrivateKey: in.TLS.Reality.PrivateKey,
					ShortID:    in.TLS.Reality.ShortID,
					Handshake: sbox.InboundRealityHandshakeOptions{
						ServerOptions: sbox.ServerOptions{
							Server:     in.TLS.Reality.Handshake.ServerName,
							ServerPort: in.TLS.Reality.Handshake.ServerPort,
						},
					},
				}
			}
		}

		listen := badoption.Addr(netip.IPv4Unspecified())
		listenOptions := sbox.ListenOptions{Listen: &listen, ListenPort: in.ListenPort}

		switch in.Protocol {
		case "vless":
			users := make([]sbox.VLESSUser, 0, len(in.Users))
			for _, u := range in.Users {
				users = append(users, sbox.VLESSUser{Name: u.Name, UUID: u.UUID, Flow: u.Flow})
			}
			inbounds = append(inbounds, sbox.Inbound{Type: "vless", Tag: in.Tag, Options: &sbox.VLESSInboundOptions{
				ListenOptions:              listenOptions,
				Users:                      users,
				InboundTLSOptionsContainer: sbox.InboundTLSOptionsContainer{TLS: tlsOpts},
			}})
		case "vmess":
			users := make([]sbox.VMessUser, 0, len(in.Users))
			for _, u := range in.Users {
				users = append(users, sbox.VMessUser{Name: u.Name, UUID: u.UUID})
			}
			inbounds = append(inbounds, sbox.Inbound{Type: "vmess", Tag: in.Tag, Options: &sbox.VMessInboundOptions{
				ListenOptions:              listenOptions,
				Users:                      users,
				InboundTLSOptionsContainer: sbox.InboundTLSOptionsContainer{TLS: tlsOpts},
			}})
		case "trojan":
			users := make([]sbox.TrojanUser, 0, len(in.Users))
			for _, u := range in.Users {
				users = append(users, sbox.TrojanUser{Name: u.Name, Password: u.Password})
			}
			inbounds = append(inbounds, sbox.Inbound{Type: "trojan", Tag: in.Tag, Options: &sbox.TrojanInboundOptions{
				ListenOptions:              listenOptions,
				Users:                      users,
				InboundTLSOptionsContainer: sbox.InboundTLSOptionsContainer{TLS: tlsOpts},
			}})
		case "shadowsocks":
			users := make([]sbox.ShadowsocksUser, 0, len(in.Users))
			for _, u := range in.Users {
				users = append(users, sbox.ShadowsocksUser{Name: u.Name, Password: u.Password})
			}
			method := "2022-blake3-aes-128-gcm"
			if len(in.Users) > 0 && in.Users[0].Method != "" {
				method = in.Users[0].Method
			}
			inbounds = append(inbounds, sbox.Inbound{Type: "shadowsocks", Tag: in.Tag, Options: &sbox.ShadowsocksInboundOptions{
				ListenOptions: listenOptions,
				Method:        method,
				Users:         users,
			}})
		}
	}

	outbounds, route, dns, log, err := buildCoreOptions(req.Core)
	if err != nil {
		return sbox.Options{}, err
	}
	return sbox.Options{
		Log:       log,
		DNS:       dns,
		Inbounds:  inbounds,
		Outbounds: outbounds,
		Route:     route,
	}, nil
}

// implicitOutboundTag{Direct,Block} are always present regardless of
// whether any custom outbound is configured - matches
// internal/httpapi/coreconfig.go's implicitOutboundTags, and is what let
// buildOptions hardcode a working single-outbound setup before Phase 7.4
// existed at all.
const (
	implicitOutboundTagDirect = "direct-out"
	implicitOutboundTagBlock  = "block-out"
)

// buildOutboundTLS builds the shared TLS sub-options for every TLS-capable
// outbound type this Core Config supports. forceEnabled is for the
// QUIC-based protocols (hysteria2/tuic), where TLS is mandatory at the
// transport level - the admin never gets a toggle to disable it, unlike
// the classic TCP protocols (vmess/trojan/vless) where TLS is genuinely
// optional and ob.TLSEnabled reflects a real admin choice.
func buildOutboundTLS(ob outboundSpec, forceEnabled bool) *sbox.OutboundTLSOptions {
	if !forceEnabled && !ob.TLSEnabled {
		return nil
	}
	return &sbox.OutboundTLSOptions{Enabled: true, ServerName: ob.TLSServerName, Insecure: ob.TLSInsecure}
}

// resolveOutboundTag maps a Core Config outbound_tag reference (which uses
// the bare admin-facing names "direct"/"block", or a custom tag) to the
// actual sing-box outbound tag that's really running.
func resolveOutboundTag(tag string) string {
	switch tag {
	case "direct":
		return implicitOutboundTagDirect
	case "block":
		return implicitOutboundTagBlock
	default:
		return tag
	}
}

// buildCoreOptions translates Phase 7.4's Core Config (outbounds/routing
// rules/DNS/log level/sniffing default) into real sing-box option types -
// see internal/httpapi/coreconfig.go's validateCoreConfig for the
// server-side checks a core is guaranteed to have already passed before
// ever reaching a node (unknown outbound references, invalid enum values).
// core may be nil (the manual POST /start path predates Core Config and
// still works with none configured at all).
func buildCoreOptions(core *coreSpec) ([]sbox.Outbound, *sbox.RouteOptions, *sbox.DNSOptions, *sbox.LogOptions, error) {
	outbounds := []sbox.Outbound{
		{Type: "direct", Tag: implicitOutboundTagDirect, Options: &sbox.DirectOutboundOptions{}},
		{Type: "block", Tag: implicitOutboundTagBlock, Options: &sbox.StubOptions{}},
	}
	route := &sbox.RouteOptions{Final: implicitOutboundTagDirect}
	if core == nil {
		return outbounds, route, nil, nil, nil
	}

	for _, ob := range core.Outbounds {
		var opts any
		// Shared by every leaf (non-group) outbound type below - the sing-box
		// equivalent of Xray's streamSettings.sockopt.interface, which the
		// real production fleet's per-location WireGuard-tunnel exit
		// selection depends on entirely. selector/urltest (group types)
		// don't embed DialerOptions at all, so it's simply never referenced
		// in those two cases - a set BindInterface on one of those is inert,
		// never wired to anything.
		dialerOptions := sbox.DialerOptions{AbstractDialerOptions: sbox.AbstractDialerOptions{BindInterface: ob.BindInterface}}
		switch ob.Type {
		case "direct":
			opts = &sbox.DirectOutboundOptions{DialerOptions: dialerOptions}
		case "block":
			opts = &sbox.StubOptions{}
		case "socks":
			opts = &sbox.SOCKSOutboundOptions{
				DialerOptions: dialerOptions,
				ServerOptions: sbox.ServerOptions{Server: ob.Server, ServerPort: uint16(ob.ServerPort)},
				Username:      ob.Username, Password: ob.Password,
			}
		case "http":
			opts = &sbox.HTTPOutboundOptions{
				DialerOptions: dialerOptions,
				ServerOptions: sbox.ServerOptions{Server: ob.Server, ServerPort: uint16(ob.ServerPort)},
				Username:      ob.Username, Password: ob.Password,
			}
		case "shadowsocks":
			opts = &sbox.ShadowsocksOutboundOptions{
				DialerOptions: dialerOptions,
				ServerOptions: sbox.ServerOptions{Server: ob.Server, ServerPort: uint16(ob.ServerPort)},
				Method:        ob.Method, Password: ob.Password,
			}
		case "vmess":
			opts = &sbox.VMessOutboundOptions{
				DialerOptions: dialerOptions,
				ServerOptions: sbox.ServerOptions{Server: ob.Server, ServerPort: uint16(ob.ServerPort)},
				UUID:          ob.UUID, Security: ob.Security,
				OutboundTLSOptionsContainer: sbox.OutboundTLSOptionsContainer{TLS: buildOutboundTLS(ob, false)},
			}
		case "trojan":
			opts = &sbox.TrojanOutboundOptions{
				DialerOptions:               dialerOptions,
				ServerOptions:               sbox.ServerOptions{Server: ob.Server, ServerPort: uint16(ob.ServerPort)},
				Password:                    ob.Password,
				OutboundTLSOptionsContainer: sbox.OutboundTLSOptionsContainer{TLS: buildOutboundTLS(ob, false)},
			}
		case "vless":
			opts = &sbox.VLESSOutboundOptions{
				DialerOptions: dialerOptions,
				ServerOptions: sbox.ServerOptions{Server: ob.Server, ServerPort: uint16(ob.ServerPort)},
				UUID:          ob.UUID, Flow: ob.Flow,
				OutboundTLSOptionsContainer: sbox.OutboundTLSOptionsContainer{TLS: buildOutboundTLS(ob, false)},
			}
		case "hysteria2":
			// QUIC-based - TLS is mandatory at the transport level, not an
			// admin-toggleable option like the classic TCP protocols above.
			opts = &sbox.Hysteria2OutboundOptions{
				DialerOptions:               dialerOptions,
				ServerOptions:               sbox.ServerOptions{Server: ob.Server, ServerPort: uint16(ob.ServerPort)},
				Password:                    ob.Password,
				OutboundTLSOptionsContainer: sbox.OutboundTLSOptionsContainer{TLS: buildOutboundTLS(ob, true)},
			}
		case "tuic":
			// Also QUIC-based - same mandatory-TLS reasoning as hysteria2.
			opts = &sbox.TUICOutboundOptions{
				DialerOptions: dialerOptions,
				ServerOptions: sbox.ServerOptions{Server: ob.Server, ServerPort: uint16(ob.ServerPort)},
				UUID:          ob.UUID, Password: ob.Password, CongestionControl: ob.CongestionControl,
				OutboundTLSOptionsContainer: sbox.OutboundTLSOptionsContainer{TLS: buildOutboundTLS(ob, true)},
			}
		case "selector":
			opts = &sbox.SelectorOutboundOptions{Outbounds: resolveOutboundTags(ob.Outbounds)}
		case "urltest":
			opts = &sbox.URLTestOutboundOptions{Outbounds: resolveOutboundTags(ob.Outbounds)}
		default:
			return nil, nil, nil, nil, fmt.Errorf("unknown outbound type: %s", ob.Type)
		}
		outbounds = append(outbounds, sbox.Outbound{Type: ob.Type, Tag: ob.Tag, Options: opts})
	}

	rules := make([]sbox.Rule, 0, len(core.RoutingRules)+1)
	if core.SniffEnabled {
		// An unconditional leading rule (no match criteria at all) applies
		// to every connection - the modern sing-box replacement for the
		// deprecated per-inbound InboundOptions.SniffEnabled field (see
		// migration 00007's own doc comment for why there's no
		// "override destination" equivalent to also carry over).
		rules = append(rules, sbox.Rule{Type: "default", DefaultOptions: sbox.DefaultRule{
			RuleAction: sbox.RuleAction{Action: "sniff"},
		}})
	}
	for _, r := range core.RoutingRules {
		raw := sbox.RawDefaultRule{
			IPIsPrivate: r.IPIsPrivate,
		}
		if len(r.Inbound) > 0 {
			raw.Inbound = badoption.Listable[string](r.Inbound)
		}
		if len(r.Domain) > 0 {
			raw.Domain = badoption.Listable[string](r.Domain)
		}
		if len(r.DomainSuffix) > 0 {
			raw.DomainSuffix = badoption.Listable[string](r.DomainSuffix)
		}
		if len(r.DomainKeyword) > 0 {
			raw.DomainKeyword = badoption.Listable[string](r.DomainKeyword)
		}
		if len(r.IPCIDR) > 0 {
			raw.IPCIDR = badoption.Listable[string](r.IPCIDR)
		}
		if len(r.PortRange) > 0 {
			raw.PortRange = badoption.Listable[string](r.PortRange)
		}
		if len(r.Network) > 0 {
			raw.Network = badoption.Listable[string](r.Network)
		}
		if len(r.Protocol) > 0 {
			raw.Protocol = badoption.Listable[string](r.Protocol)
		}
		if len(r.Port) > 0 {
			ports := make(badoption.Listable[uint16], len(r.Port))
			for i, p := range r.Port {
				ports[i] = uint16(p)
			}
			raw.Port = ports
		}
		rules = append(rules, sbox.Rule{Type: "default", DefaultOptions: sbox.DefaultRule{
			RawDefaultRule: raw,
			RuleAction: sbox.RuleAction{
				Action:       "route",
				RouteOptions: sbox.RouteActionOptions{Outbound: resolveOutboundTag(r.OutboundTag)},
			},
		}})
	}
	route.Rules = rules

	var dns *sbox.DNSOptions
	if len(core.DNSServers) > 0 {
		servers := make([]sbox.DNSServerOptions, 0, len(core.DNSServers))
		for _, s := range core.DNSServers {
			var opts any
			switch s.Type {
			case "local":
				opts = &sbox.LocalDNSServerOptions{}
			case "udp", "tcp":
				opts = &sbox.RemoteDNSServerOptions{
					DNSServerAddressOptions: sbox.DNSServerAddressOptions{Server: s.Address, ServerPort: uint16(s.Port)},
				}
			case "tls":
				opts = &sbox.RemoteTLSDNSServerOptions{
					RemoteDNSServerOptions: sbox.RemoteDNSServerOptions{
						DNSServerAddressOptions: sbox.DNSServerAddressOptions{Server: s.Address, ServerPort: uint16(s.Port)},
					},
				}
			case "https":
				opts = &sbox.RemoteHTTPSDNSServerOptions{
					RemoteTLSDNSServerOptions: sbox.RemoteTLSDNSServerOptions{
						RemoteDNSServerOptions: sbox.RemoteDNSServerOptions{
							DNSServerAddressOptions: sbox.DNSServerAddressOptions{Server: s.Address, ServerPort: uint16(s.Port)},
						},
					},
					Path: s.Path,
				}
			default:
				return nil, nil, nil, nil, fmt.Errorf("unknown dns server type: %s", s.Type)
			}
			servers = append(servers, sbox.DNSServerOptions{Type: s.Type, Tag: s.Tag, Options: opts})
		}
		dns = &sbox.DNSOptions{RawDNSOptions: sbox.RawDNSOptions{Servers: servers}}
	}

	var log *sbox.LogOptions
	if core.LogLevel != "" {
		log = &sbox.LogOptions{Level: core.LogLevel}
	}

	return outbounds, route, dns, log, nil
}

func resolveOutboundTags(tags []string) []string {
	out := make([]string, len(tags))
	for i, t := range tags {
		out[i] = resolveOutboundTag(t)
	}
	return out
}

// singBoxVersion is the pinned version from go.mod, reported as-is rather
// than plumbed through build-time ldflags - cosmetic display data on the
// Monitoring page, not worth the extra build-script complexity yet.
const singBoxVersion = "sing-box v1.14.0"

type reportUserUsage struct {
	Username string `json:"username"`
	Uplink   int64  `json:"uplink"`
	Downlink int64  `json:"downlink"`
}

type reportRequest struct {
	Users []reportUserUsage  `json:"users"`
	Host  hostmetrics.Sample `json:"host"`
}

// syncLoop is both halves of the node<->panel sync mechanism: Phase 7.3's
// usage/health push (see internal/httpapi/nodereport.go for the receiving
// side) and Phase 7.4's config pull (see internal/httpapi/nodeconfig.go).
// Combined into one loop/ticker rather than two independent ones since
// they share the same interval and HTTP client, and there's no reason for
// a node to make two separate round trips to the same panel every tick.
// Runs for the node process's whole lifetime, independent of whether a
// sing-box core is currently started - an idle node still reports its own
// host health and still checks whether it should start one.
func (s *server) syncLoop(ctx context.Context, cfg config) {
	client := &http.Client{Timeout: 5 * time.Second}
	ticker := time.NewTicker(cfg.ReportInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.pushOnce(ctx, client, cfg)
			s.pullOnce(ctx, client, cfg)
		}
	}
}

func (s *server) pushOnce(ctx context.Context, client *http.Client, cfg config) {
	usage := s.traffic.Drain()
	users := make([]reportUserUsage, 0, len(usage))
	for username, u := range usage {
		users = append(users, reportUserUsage{Username: username, Uplink: u.Up, Downlink: u.Down})
	}

	s.mu.Lock()
	running := s.node != nil
	s.mu.Unlock()

	sample := hostmetrics.Collect(running, singBoxVersion)
	body, err := json.Marshal(reportRequest{Users: users, Host: sample})
	if err != nil {
		s.logger.Error("push report: marshal", "error", err)
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.PanelURL+"/api/internal/node-report", bytes.NewReader(body))
	if err != nil {
		s.logger.Error("push report: build request", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.ReportSecret)

	resp, err := client.Do(req)
	if err != nil {
		s.logger.Warn("push report: request failed, will retry next tick", "error", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		s.logger.Warn("push report: panel rejected report", "status", resp.StatusCode)
	}
}

// pulledConfig is GET /api/internal/node-config's response shape - see
// internal/httpapi/nodeconfig.go's nodeConfigResponse, which this mirrors
// field-for-field (Inbounds reuses the exact same inboundSpec/userSpec/
// tlsSpec/realitySpec types startRequest already decodes for POST /start).
type pulledConfig struct {
	Version  string        `json:"version"`
	Inbounds []inboundSpec `json:"inbounds"`
	Core     coreSpec      `json:"core"`
}

// pullOnce fetches the panel's current desired config and, if it differs
// from what's currently running, applies it - hot where possible (only a
// VLESS inbound's user list changed, using the same UpdateVLESSUsers path
// PUT /inbounds/{tag}/users already exposes), otherwise a full stop+
// rebuild+start. See diffPulledConfig for exactly what counts as which.
func (s *server) pullOnce(ctx context.Context, client *http.Client, cfg config) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.PanelURL+"/api/internal/node-config", nil)
	if err != nil {
		s.logger.Error("pull config: build request", "error", err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+cfg.ReportSecret)

	resp, err := client.Do(req)
	if err != nil {
		s.logger.Warn("pull config: request failed, will retry next tick", "error", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		s.logger.Warn("pull config: panel rejected request", "status", resp.StatusCode)
		return
	}
	var pulled pulledConfig
	if err := json.NewDecoder(resp.Body).Decode(&pulled); err != nil {
		s.logger.Error("pull config: decode response", "error", err)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.lastPulled != nil && s.lastPulled.Version == pulled.Version {
		return
	}

	needsRestart, vlessTagsChanged := diffPulledConfig(s.lastPulled, pulled)

	if s.node == nil {
		if len(pulled.Inbounds) == 0 {
			s.lastPulled = &pulled
			return
		}
		needsRestart = true
	}

	if needsRestart {
		s.applyFullLocked(pulled)
		return
	}

	byTag := make(map[string]inboundSpec, len(pulled.Inbounds))
	for _, in := range pulled.Inbounds {
		byTag[in.Tag] = in
	}
	for _, tag := range vlessTagsChanged {
		in := byTag[tag]
		users := make([]sbox.VLESSUser, 0, len(in.Users))
		for _, u := range in.Users {
			users = append(users, sbox.VLESSUser{Name: u.Name, UUID: u.UUID, Flow: u.Flow})
		}
		if err := s.node.UpdateVLESSUsers(tag, users); err != nil {
			s.logger.Error("pull config: hot-apply users", "tag", tag, "error", err)
		} else {
			s.logger.Info("pull config: hot-applied user list", "tag", tag, "users", len(users))
		}
	}
	s.lastPulled = &pulled
}

// applyFullLocked stops whatever's currently running (if anything) and
// starts fresh from pulled - the same internal path POST /start already
// uses. Caller must hold s.mu.
func (s *server) applyFullLocked(pulled pulledConfig) {
	if s.node != nil {
		if err := s.node.Close(); err != nil {
			s.logger.Warn("pull config: close previous node before restart", "error", err)
		}
		s.node = nil
	}
	opts, err := buildOptions(startRequest{Inbounds: pulled.Inbounds, Core: &pulled.Core})
	if err != nil {
		s.logger.Error("pull config: build options", "error", err)
		return
	}
	node, err := nodecore.New(s.ctx, opts, s.traffic)
	if err != nil {
		s.logger.Error("pull config: build node", "error", err)
		return
	}
	if err := node.Start(); err != nil {
		s.logger.Error("pull config: start node", "error", err)
		return
	}
	s.node = node
	s.lastPulled = &pulled
	s.logger.Info("pull config: applied full restart", "inbounds", len(pulled.Inbounds), "version", pulled.Version)
}

// diffPulledConfig decides what changed between the last applied config
// and a newly-pulled one. needsRestart covers anything a hot update can't
// handle: a first-ever config (old == nil), any inbound added/removed,
// any inbound's shape changing (protocol/port/TLS - everything except its
// user list), the fleet-wide Core section changing at all, or a non-VLESS
// inbound's user list changing (no hot-update path exists for those
// protocols yet - see internal/nodecore/vless's own doc comment on why
// only VLESS has the fork this needs). When needsRestart is false,
// vlessTags lists exactly the VLESS-tagged inbounds whose user list
// actually changed and should be hot-applied.
func diffPulledConfig(old *pulledConfig, next pulledConfig) (needsRestart bool, vlessTags []string) {
	if old == nil {
		return true, nil
	}
	if !reflect.DeepEqual(old.Core, next.Core) {
		return true, nil
	}
	oldByTag := make(map[string]inboundSpec, len(old.Inbounds))
	for _, in := range old.Inbounds {
		oldByTag[in.Tag] = in
	}
	if len(oldByTag) != len(next.Inbounds) {
		return true, nil
	}
	for _, in := range next.Inbounds {
		oldIn, ok := oldByTag[in.Tag]
		if !ok {
			return true, nil
		}
		oldShape, newShape := oldIn, in
		oldShape.Users, newShape.Users = nil, nil
		if !reflect.DeepEqual(oldShape, newShape) {
			return true, nil
		}
		if !reflect.DeepEqual(oldIn.Users, in.Users) {
			if in.Protocol != "vless" {
				return true, nil
			}
			vlessTags = append(vlessTags, in.Tag)
		}
	}
	return false, vlessTags
}
