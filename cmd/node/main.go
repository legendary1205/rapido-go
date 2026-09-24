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
	"slices"
	"strconv"
	"sync"
	"syscall"
	"time"

	sbox "github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"

	"github.com/legendary1205/rapido-go/internal/hostmetrics"
	"github.com/legendary1205/rapido-go/internal/nodecore"
	"github.com/legendary1205/rapido-go/internal/nodecore/traffic"
	"github.com/legendary1205/rapido-go/internal/tunnelhealth"
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

	// monitor probes every WireGuard tunnel for the life of the process,
	// whether or not a core is running: the panel's monitoring page wants the
	// verdicts even on an idle node. Nil in tests.
	monitor *tunnelhealth.Monitor

	// plan, supervisor and supCancel describe the running core and are set and
	// cleared together with node, under mu.
	plan       nodePlan
	supervisor *nodecore.Supervisor
	supCancel  context.CancelFunc
}

// fallbackCheckInterval is how often the supervisor compares each tunnel's
// health with the outbound currently in use. Health itself only changes when
// the monitor completes a probe round, so this just bounds the reaction time
// after one does.
const fallbackCheckInterval = time.Second

// startNodeLocked builds, starts and supervises a core from req. Caller holds mu
// and has already checked that no core is running.
func (s *server) startNodeLocked(req startRequest) error {
	opts, plan, err := buildOptionsPlan(req)
	if err != nil {
		return err
	}
	node, err := nodecore.New(s.ctx, opts, s.traffic)
	if err != nil {
		return err
	}
	if err := node.Start(); err != nil {
		node.Close()
		return err
	}
	s.node = node
	s.plan = plan
	s.startSupervisorLocked(plan)
	return nil
}

// stopNodeLocked stops supervising and closes the running core, if any.
func (s *server) stopNodeLocked() error {
	if s.supCancel != nil {
		s.supCancel()
		s.supCancel, s.supervisor = nil, nil
	}
	if s.monitor != nil {
		s.monitor.Watch(nil)
	}
	s.plan = nodePlan{}
	if s.node == nil {
		return nil
	}
	err := s.node.Close()
	s.node = nil
	return err
}

func (s *server) startSupervisorLocked(plan nodePlan) {
	if s.monitor == nil {
		return
	}
	s.monitor.Watch(plan.watchedInterfaces())
	if len(plan.fallbacks) == 0 {
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	sup := nodecore.NewSupervisor(s.node, plan.fallbacks, s.monitor, s.logger)
	sup.Foreign = func(iface string) bool { return !slices.Contains(hostmetrics.TunnelNames(), iface) }
	s.supervisor, s.supCancel = sup, cancel
	go sup.Run(ctx, fallbackCheckInterval)
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
	srv.monitor = tunnelhealth.New(tunnelhealth.Options{Logger: logger})
	go srv.monitor.Run(ctx)
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
		srv.stopNodeLocked()
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
	Tag        string `json:"tag"`
	Protocol   string `json:"protocol"` // vless | vmess | trojan | shadowsocks
	ListenPort uint16 `json:"listen_port"`
	// ListenPorts is set only when one logical inbound serves several ports
	// (ListenPort is then the first of them, for a node that predates this
	// field). Each port becomes its own sing-box listener tagged
	// "<tag>#<port>", so a routing rule can tell them apart.
	ListenPorts []uint16   `json:"listen_ports,omitempty"`
	Users       []userSpec `json:"users"`
	TLS         *tlsSpec   `json:"tls,omitempty"`
}

func (in inboundSpec) ports() []uint16 {
	if len(in.ListenPorts) > 0 {
		return in.ListenPorts
	}
	return []uint16{in.ListenPort}
}

// derivedInboundTag names the sing-box listener for one port of a
// multi-port inbound. '#' cannot appear in a tag an admin would type, so it
// can never collide with a real inbound.
func derivedInboundTag(tag string, port uint16) string {
	return tag + "#" + strconv.Itoa(int(port))
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
	// DirectFallback keeps this outbound working when its BindInterface is
	// down: traffic is sent over a plain direct connection until the
	// interface is healthy again. Ignored without a BindInterface and for
	// block/selector/urltest.
	DirectFallback bool `json:"direct_fallback,omitempty"`
}

type routingRuleSpec struct {
	Inbound []string `json:"inbound,omitempty"`
	// InboundPort narrows Inbound to connections that arrived on these local
	// listen ports of a multi-port inbound.
	InboundPort   []int    `json:"inbound_port,omitempty"`
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

	if _, _, err := buildOptionsPlan(req); err != nil {
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

	if err := s.startNodeLocked(req); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"detail": "started"})
}

func (s *server) handleStop(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.node == nil {
		writeJSON(w, http.StatusOK, map[string]any{"detail": "already stopped"})
		return
	}
	if err := s.stopNodeLocked(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
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
	for _, listener := range s.plan.listenerTags(tag) {
		if err := s.node.UpdateVLESSUsers(listener, users); err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": err.Error()})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"detail": "updated"})
}

// nodePlan is what buildOptionsPlan learned while translating a config that
// the running node needs to remember afterwards.
type nodePlan struct {
	// derivedTags maps a multi-port inbound's own tag to the sing-box
	// listeners it expanded into. A single-port inbound is not in it - its
	// tag is the listener's tag.
	derivedTags map[string][]string
	// fallbacks are the outbounds that must fail over to a direct connection
	// when their WireGuard interface is down.
	fallbacks []nodecore.FallbackGroup
}

// listenerTags returns the sing-box inbound tags behind one logical inbound.
func (p nodePlan) listenerTags(tag string) []string {
	if tags, ok := p.derivedTags[tag]; ok {
		return tags
	}
	return []string{tag}
}

// watchedInterfaces is every interface a fallback group depends on.
func (p nodePlan) watchedInterfaces() []string {
	names := make([]string, 0, len(p.fallbacks))
	for _, g := range p.fallbacks {
		names = append(names, g.Interface)
	}
	return names
}

func buildOptions(req startRequest) (sbox.Options, error) {
	opts, _, err := buildOptionsPlan(req)
	return opts, err
}

func buildInboundTLS(in inboundSpec) *sbox.InboundTLSOptions {
	if in.TLS == nil {
		return nil
	}
	tlsOpts := &sbox.InboundTLSOptions{
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
	return tlsOpts
}

func buildOptionsPlan(req startRequest) (sbox.Options, nodePlan, error) {
	plan := nodePlan{derivedTags: make(map[string][]string)}
	portsByTag := make(map[string][]uint16, len(req.Inbounds))
	inbounds := make([]sbox.Inbound, 0, len(req.Inbounds))
	for _, in := range req.Inbounds {
		ports := in.ports()
		portsByTag[in.Tag] = ports
		for _, port := range ports {
			tag := in.Tag
			if len(ports) > 1 {
				tag = derivedInboundTag(in.Tag, port)
				plan.derivedTags[in.Tag] = append(plan.derivedTags[in.Tag], tag)
			}
			// Built per listener: sing-box takes ownership of the options it is
			// given, so two inbounds must not share one TLS block.
			tlsOpts := buildInboundTLS(in)

			listen := badoption.Addr(netip.IPv4Unspecified())
			listenOptions := sbox.ListenOptions{Listen: &listen, ListenPort: port}

			switch in.Protocol {
			case "vless":
				users := make([]sbox.VLESSUser, 0, len(in.Users))
				for _, u := range in.Users {
					users = append(users, sbox.VLESSUser{Name: u.Name, UUID: u.UUID, Flow: u.Flow})
				}
				inbounds = append(inbounds, sbox.Inbound{Type: "vless", Tag: tag, Options: &sbox.VLESSInboundOptions{
					ListenOptions:              listenOptions,
					Users:                      users,
					InboundTLSOptionsContainer: sbox.InboundTLSOptionsContainer{TLS: tlsOpts},
				}})
			case "vmess":
				users := make([]sbox.VMessUser, 0, len(in.Users))
				for _, u := range in.Users {
					users = append(users, sbox.VMessUser{Name: u.Name, UUID: u.UUID})
				}
				inbounds = append(inbounds, sbox.Inbound{Type: "vmess", Tag: tag, Options: &sbox.VMessInboundOptions{
					ListenOptions:              listenOptions,
					Users:                      users,
					InboundTLSOptionsContainer: sbox.InboundTLSOptionsContainer{TLS: tlsOpts},
				}})
			case "trojan":
				users := make([]sbox.TrojanUser, 0, len(in.Users))
				for _, u := range in.Users {
					users = append(users, sbox.TrojanUser{Name: u.Name, Password: u.Password})
				}
				inbounds = append(inbounds, sbox.Inbound{Type: "trojan", Tag: tag, Options: &sbox.TrojanInboundOptions{
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
				inbounds = append(inbounds, sbox.Inbound{Type: "shadowsocks", Tag: tag, Options: &sbox.ShadowsocksInboundOptions{
					ListenOptions: listenOptions,
					Method:        method,
					Users:         users,
				}})
			}
		}
	}

	outbounds, route, dns, log, fallbacks, err := buildCoreOptions(req.Core, portsByTag)
	if err != nil {
		return sbox.Options{}, nodePlan{}, err
	}
	plan.fallbacks = fallbacks
	return sbox.Options{
		Log:       log,
		DNS:       dns,
		Inbounds:  inbounds,
		Outbounds: outbounds,
		Route:     route,
	}, plan, nil
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

// isLeafOutbound reports whether an outbound type dials a destination itself
// (as opposed to picking among other outbounds, or refusing to dial).
func isLeafOutbound(t string) bool {
	switch t {
	case "block", "selector", "urltest":
		return false
	}
	return true
}

// leafOutboundOptions builds the sing-box options for every outbound type
// that dials by itself. bind is the network interface its sockets are pinned
// to - the sing-box equivalent of Xray's streamSettings.sockopt.interface,
// which the production fleet's per-location WireGuard exit selection depends
// on entirely; "" leaves the sockets on the default route. Passing it
// separately from ob is what lets one Core Config outbound be built twice:
// pinned, and unpinned as its fallback.
func leafOutboundOptions(ob outboundSpec, bind string) (any, error) {
	dialerOptions := sbox.DialerOptions{AbstractDialerOptions: sbox.AbstractDialerOptions{BindInterface: bind}}
	server := sbox.ServerOptions{Server: ob.Server, ServerPort: uint16(ob.ServerPort)}
	switch ob.Type {
	case "direct":
		return &sbox.DirectOutboundOptions{DialerOptions: dialerOptions}, nil
	case "socks":
		return &sbox.SOCKSOutboundOptions{DialerOptions: dialerOptions, ServerOptions: server, Username: ob.Username, Password: ob.Password}, nil
	case "http":
		return &sbox.HTTPOutboundOptions{DialerOptions: dialerOptions, ServerOptions: server, Username: ob.Username, Password: ob.Password}, nil
	case "shadowsocks":
		return &sbox.ShadowsocksOutboundOptions{DialerOptions: dialerOptions, ServerOptions: server, Method: ob.Method, Password: ob.Password}, nil
	case "vmess":
		return &sbox.VMessOutboundOptions{
			DialerOptions: dialerOptions, ServerOptions: server,
			UUID: ob.UUID, Security: ob.Security,
			OutboundTLSOptionsContainer: sbox.OutboundTLSOptionsContainer{TLS: buildOutboundTLS(ob, false)},
		}, nil
	case "trojan":
		return &sbox.TrojanOutboundOptions{
			DialerOptions: dialerOptions, ServerOptions: server, Password: ob.Password,
			OutboundTLSOptionsContainer: sbox.OutboundTLSOptionsContainer{TLS: buildOutboundTLS(ob, false)},
		}, nil
	case "vless":
		return &sbox.VLESSOutboundOptions{
			DialerOptions: dialerOptions, ServerOptions: server,
			UUID: ob.UUID, Flow: ob.Flow,
			OutboundTLSOptionsContainer: sbox.OutboundTLSOptionsContainer{TLS: buildOutboundTLS(ob, false)},
		}, nil
	case "hysteria2":
		// QUIC-based - TLS is mandatory at the transport level, not an
		// admin-toggleable option like the classic TCP protocols above.
		return &sbox.Hysteria2OutboundOptions{
			DialerOptions: dialerOptions, ServerOptions: server, Password: ob.Password,
			OutboundTLSOptionsContainer: sbox.OutboundTLSOptionsContainer{TLS: buildOutboundTLS(ob, true)},
		}, nil
	case "tuic":
		// Also QUIC-based - same mandatory-TLS reasoning as hysteria2.
		return &sbox.TUICOutboundOptions{
			DialerOptions: dialerOptions, ServerOptions: server,
			UUID: ob.UUID, Password: ob.Password, CongestionControl: ob.CongestionControl,
			OutboundTLSOptionsContainer: sbox.OutboundTLSOptionsContainer{TLS: buildOutboundTLS(ob, true)},
		}, nil
	}
	return nil, fmt.Errorf("unknown outbound type: %s", ob.Type)
}

// Suffixes of the two outbounds a direct_fallback outbound is built from.
// The outbound's own tag becomes the selector routing rules point at.
const (
	fallbackPrimarySuffix  = "~wg"
	fallbackFallbackSuffix = "~direct"
)

// inboundMatchTags turns a rule's inbound list (plus its optional port
// restriction) into the sing-box listener tags it must match. A multi-port
// inbound is matched through its per-port tags; an inbound this config does
// not define is passed through untouched (it simply matches nothing, as
// before). The result is empty only when the port restriction excludes
// every listener - the caller must then drop the rule, because a rule
// with no inbound criterion at all matches every connection.
func inboundMatchTags(inbound []string, ports []int, portsByTag map[string][]uint16) []string {
	allowed := func(p uint16) bool {
		if len(ports) == 0 {
			return true
		}
		for _, q := range ports {
			if q == int(p) {
				return true
			}
		}
		return false
	}
	var tags []string
	for _, tag := range inbound {
		listenPorts, known := portsByTag[tag]
		switch {
		case !known:
			tags = append(tags, tag)
		case len(listenPorts) <= 1:
			if len(listenPorts) == 0 || allowed(listenPorts[0]) {
				tags = append(tags, tag)
			}
		default:
			for _, p := range listenPorts {
				if allowed(p) {
					tags = append(tags, derivedInboundTag(tag, p))
				}
			}
		}
	}
	return tags
}

// buildCoreOptions translates Phase 7.4's Core Config (outbounds/routing
// rules/DNS/log level/sniffing default) into real sing-box option types -
// see internal/httpapi/coreconfig.go's validateCoreConfig for the
// server-side checks a core is guaranteed to have already passed before
// ever reaching a node (unknown outbound references, invalid enum values).
// core may be nil (the manual POST /start path predates Core Config and
// still works with none configured at all). portsByTag lists each inbound's
// listen ports, which routing rules that name an inbound need in order to
// address one port of a multi-port inbound.
func buildCoreOptions(core *coreSpec, portsByTag map[string][]uint16) ([]sbox.Outbound, *sbox.RouteOptions, *sbox.DNSOptions, *sbox.LogOptions, []nodecore.FallbackGroup, error) {
	outbounds := []sbox.Outbound{
		{Type: "direct", Tag: implicitOutboundTagDirect, Options: &sbox.DirectOutboundOptions{}},
		{Type: "block", Tag: implicitOutboundTagBlock, Options: &sbox.StubOptions{}},
	}
	route := &sbox.RouteOptions{Final: implicitOutboundTagDirect}
	if core == nil {
		return outbounds, route, nil, nil, nil, nil
	}

	var fallbacks []nodecore.FallbackGroup
	for _, ob := range core.Outbounds {
		switch ob.Type {
		case "block":
			outbounds = append(outbounds, sbox.Outbound{Type: ob.Type, Tag: ob.Tag, Options: &sbox.StubOptions{}})
		case "selector":
			outbounds = append(outbounds, sbox.Outbound{Type: ob.Type, Tag: ob.Tag, Options: &sbox.SelectorOutboundOptions{Outbounds: resolveOutboundTags(ob.Outbounds)}})
		case "urltest":
			outbounds = append(outbounds, sbox.Outbound{Type: ob.Type, Tag: ob.Tag, Options: &sbox.URLTestOutboundOptions{Outbounds: resolveOutboundTags(ob.Outbounds)}})
		default:
			opts, err := leafOutboundOptions(ob, ob.BindInterface)
			if err != nil {
				return nil, nil, nil, nil, nil, err
			}
			if !ob.DirectFallback || ob.BindInterface == "" {
				outbounds = append(outbounds, sbox.Outbound{Type: ob.Type, Tag: ob.Tag, Options: opts})
				continue
			}
			// Two dialers for one exit, and a selector standing in for the
			// admin's tag. Which member is live is decided at run time by the
			// fallback supervisor from the tunnel's health - see
			// nodecore.Supervisor.
			plainOpts, err := leafOutboundOptions(ob, "")
			if err != nil {
				return nil, nil, nil, nil, nil, err
			}
			primary, fallback := ob.Tag+fallbackPrimarySuffix, ob.Tag+fallbackFallbackSuffix
			outbounds = append(outbounds,
				sbox.Outbound{Type: ob.Type, Tag: primary, Options: opts},
				sbox.Outbound{Type: ob.Type, Tag: fallback, Options: plainOpts},
				sbox.Outbound{Type: "selector", Tag: ob.Tag, Options: &sbox.SelectorOutboundOptions{
					Outbounds: []string{primary, fallback},
					Default:   primary,
					// A tunnel that has just died leaves connections hanging on it;
					// closing them lets clients reconnect over the fallback at once
					// instead of waiting out their own timeouts.
					InterruptExistConnections: true,
				}},
			)
			fallbacks = append(fallbacks, nodecore.FallbackGroup{
				Group: ob.Tag, Primary: primary, Fallback: fallback, Interface: ob.BindInterface,
			})
		}
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
			tags := inboundMatchTags(r.Inbound, r.InboundPort, portsByTag)
			if len(tags) == 0 {
				continue
			}
			raw.Inbound = badoption.Listable[string](tags)
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
				return nil, nil, nil, nil, nil, fmt.Errorf("unknown dns server type: %s", s.Type)
			}
			servers = append(servers, sbox.DNSServerOptions{Type: s.Type, Tag: s.Tag, Options: opts})
		}
		dns = &sbox.DNSOptions{RawDNSOptions: sbox.RawDNSOptions{Servers: servers}}
	}

	var log *sbox.LogOptions
	if core.LogLevel != "" {
		log = &sbox.LogOptions{Level: core.LogLevel}
	}

	return outbounds, route, dns, log, fallbacks, nil
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
	sample.Tunnels = s.tunnelReport(sample.Tunnels)
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

// tunnelReport merges the probe results and the fallback state into the
// interface reading, so the panel is told what each tunnel is actually doing
// and not just that its network device exists.
func (s *server) tunnelReport(read []hostmetrics.Tunnel) []hostmetrics.Tunnel {
	if s.monitor == nil {
		return read
	}
	health := s.monitor.Health()
	s.mu.Lock()
	var active map[string]bool
	if s.supervisor != nil {
		active = s.supervisor.ActiveByInterface()
	}
	s.mu.Unlock()
	for name, h := range health {
		if active[name] {
			h.FallbackActive = true
			health[name] = h
		}
	}
	return hostmetrics.ApplyTunnelHealth(read, hostmetrics.ConfiguredTunnelNames(), health)
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
		for _, listener := range s.plan.listenerTags(tag) {
			if err := s.node.UpdateVLESSUsers(listener, users); err != nil {
				s.logger.Error("pull config: hot-apply users", "tag", listener, "error", err)
			}
		}
		s.logger.Info("pull config: hot-applied user list", "tag", tag, "users", len(users))
	}
	s.lastPulled = &pulled
}

// applyFullLocked stops whatever's currently running (if anything) and
// starts fresh from pulled - the same internal path POST /start already
// uses. Caller must hold s.mu.
func (s *server) applyFullLocked(pulled pulledConfig) {
	if err := s.stopNodeLocked(); err != nil {
		s.logger.Warn("pull config: close previous node before restart", "error", err)
	}
	if err := s.startNodeLocked(startRequest{Inbounds: pulled.Inbounds, Core: &pulled.Core}); err != nil {
		s.logger.Error("pull config: start node", "error", err)
		return
	}
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
