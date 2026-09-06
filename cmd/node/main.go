// Command node is the Rapido Go node agent: an HTTP control plane over
// mTLS, driving a sing-box instance (internal/nodecore) with a locally
// forked VLESS inbound that supports hot user add/remove with no listener
// restart - see internal/nodecore/vless's doc comment for why that fork
// exists.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	sbox "github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"

	"github.com/legendary1205/rapido-go/internal/nodecore"
)

type config struct {
	ListenAddr string // control-plane HTTP address, e.g. "0.0.0.0:62051"
	CertFile   string
	KeyFile    string
	CAFile     string // the Rapido CA cert - only clients presenting a cert signed by this CA are accepted
}

func loadConfig() config {
	return config{
		ListenAddr: getEnv("NODE_LISTEN_ADDR", "0.0.0.0:62051"),
		CertFile:   getEnv("NODE_CERT_FILE", "/etc/rapido-node/cert.pem"),
		KeyFile:    getEnv("NODE_KEY_FILE", "/etc/rapido-node/key.pem"),
		CAFile:     getEnv("NODE_CA_FILE", "/etc/rapido-node/ca.pem"),
	}
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
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg := loadConfig()

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

	srv := &server{logger: logger, ctx: ctx}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", srv.handleHealth)
	mux.HandleFunc("POST /start", srv.handleStart)
	mux.HandleFunc("POST /stop", srv.handleStop)
	mux.HandleFunc("PUT /inbounds/{tag}/users", srv.handleUpdateUsers)

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

type startRequest struct {
	Inbounds []inboundSpec `json:"inbounds"`
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

	node, err := nodecore.New(s.ctx, opts)
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

	return sbox.Options{
		Inbounds:  inbounds,
		Outbounds: []sbox.Outbound{{Type: "direct", Tag: "direct-out", Options: &sbox.DirectOutboundOptions{}}},
		Route:     &sbox.RouteOptions{Final: "direct-out"},
	}, nil
}
