package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/legendary1205/rapido-go/internal/db/generated"
	"github.com/legendary1205/rapido-go/internal/proxysettings"
)

// The wire shapes below mirror cmd/node/main.go's inboundSpec/userSpec/
// tlsSpec/realitySpec/startRequest byte-for-byte (JSON field names) - the
// two processes communicate over HTTP, not shared Go types, so keeping
// these two definitions in sync by hand is the actual contract. This is
// the same JSON shape POST /start already accepts; the difference here is
// this handler computes it from the DB instead of an admin building it by
// hand.
type nodeConfigUserSpec struct {
	Name     string `json:"name"`
	UUID     string `json:"uuid,omitempty"`
	Password string `json:"password,omitempty"`
	Flow     string `json:"flow,omitempty"`
	Method   string `json:"method,omitempty"`
}

type nodeConfigRealitySpec struct {
	PrivateKey string   `json:"private_key"`
	ShortID    []string `json:"short_id"`
	Handshake  struct {
		ServerName string `json:"server_name"`
		ServerPort uint16 `json:"server_port"`
	} `json:"handshake"`
}

type nodeConfigTLSSpec struct {
	ServerName  string                 `json:"server_name"`
	Certificate string                 `json:"certificate"`
	Key         string                 `json:"key"`
	Reality     *nodeConfigRealitySpec `json:"reality,omitempty"`
}

type nodeConfigInboundSpec struct {
	Tag        string `json:"tag"`
	Protocol   string `json:"protocol"`
	ListenPort uint16 `json:"listen_port"`
	// ListenPorts is every distinct port of the inbound's enabled hosts,
	// ascending, and is only set when there is MORE than one - a single-port
	// inbound serializes exactly as it did before this field existed, and a
	// node that predates it keeps reading ListenPort.
	ListenPorts []uint16             `json:"listen_ports,omitempty"`
	Users       []nodeConfigUserSpec `json:"users"`
	TLS         *nodeConfigTLSSpec   `json:"tls,omitempty"`
}

// nodeConfigInboundWire is nodeConfigInboundSpec with the user list already
// encoded. Same fields in the same order, so it marshals to byte-identical
// JSON - the only difference is where the bytes come from.
//
// It exists because every inbound of a protocol carries that protocol's
// ENTIRE user list, and production runs 15 vless inbounds over ~9,900
// users: encoding the spec form re-serialises the same 10k-element array
// fifteen times, which profiling showed to be ~80% of this endpoint's CPU.
// Encoding that array once per protocol and splicing the result into each
// inbound turns fourteen of those fifteen passes into a memmove.
// TestNodeConfigBodyIsByteIdenticalToMarshallingThePayload is what keeps
// the two forms honest.
type nodeConfigInboundWire struct {
	Tag         string             `json:"tag"`
	Protocol    string             `json:"protocol"`
	ListenPort  uint16             `json:"listen_port"`
	ListenPorts []uint16           `json:"listen_ports,omitempty"`
	Users       json.RawMessage    `json:"users"`
	TLS         *nodeConfigTLSSpec `json:"tls,omitempty"`
}

// nodeConfigResponse is the full payload a node self-applies - see
// cmd/node/main.go's pull loop. Version is a hash of the payload's own
// content, so nodes whose profiles produce the same payload share it.
type nodeConfigResponse struct {
	Version  string                  `json:"version"`
	Inbounds []nodeConfigInboundSpec `json:"inbounds"`
	Core     coreConfigDTO           `json:"core"`
}

// validHostPorts keeps only real TCP/UDP port numbers. hosts.port is a bare
// INTEGER nothing range-checks on write, and the uint16 conversions below
// would otherwise wrap 65536 to 0 and silently collide two hosts' ports.
func validHostPorts(ports []int32) []int {
	out := make([]int, 0, len(ports))
	for _, p := range ports {
		if p >= 1 && p <= 65535 {
			out = append(out, int(p))
		}
	}
	return out
}

// multiListenPorts is the value of an inbound's `listen_ports`: nil unless
// the inbound really has more than one distinct port (see the field's doc).
func multiListenPorts(ports []int32) []uint16 {
	valid := validHostPorts(ports)
	if len(valid) < 2 {
		return nil
	}
	out := make([]uint16, len(valid))
	for i, p := range valid {
		out[i] = uint16(p)
	}
	return out
}

// nodeConfigSnapshot is everything in the payload that is the same whichever
// node asks: the fleet's inbounds with their user lists, each protocol's user
// list already encoded, and the fleet core config. It is built once per data
// version and rendered once per node profile, so several distinct profiles do
// not each re-read and re-marshal ~10k users.
type nodeConfigSnapshot struct {
	version   int64 // the data version read BEFORE the queries below ran
	builtAt   time.Time
	inbounds  []nodeConfigInboundSpec
	usersJSON map[string]json.RawMessage
	core      coreConfigDTO
}

// loadNodeConfigSnapshot is the Go equivalent of
// XRayConfig.include_db_users(): groups every active/on_hold user's proxy
// credentials by protocol, and stuffs them into every auto-sync-eligible
// inbound of that protocol. An inbound is one row per tag however many
// ports its hosts use - those go out as listen_ports.
//
// The core config is read straight from the table, not through its Redis
// cache: this snapshot is stored under a data version that changes whenever
// core_config does, and a stale cached row would defeat exactly that.
func (h *Handler) loadNodeConfigSnapshot(ctx context.Context, version int64) (*nodeConfigSnapshot, error) {
	h.nodeConfig.snapshotLoads.Add(1)
	inboundRows, err := h.store.Queries.ListAutoSyncInbounds(ctx)
	if err != nil {
		return nil, err
	}
	proxyRows, err := h.store.Queries.ListActiveUserProxiesForNodeConfig(ctx)
	if err != nil {
		return nil, err
	}
	coreRow, err := h.store.Queries.GetCoreConfig(ctx)
	if err != nil {
		return nil, err
	}

	usersByProtocol := make(map[string][]nodeConfigUserSpec)
	for _, p := range proxyRows {
		settings, err := proxysettings.FromStored(proxysettings.ProxyType(p.Type), p.Settings)
		if err != nil {
			continue // a malformed row shouldn't take down every other user's config
		}
		spec := nodeConfigUserSpec{Name: p.Username}
		switch p.Type {
		case "vmess":
			spec.UUID = settings.VMess.ID
		case "vless":
			spec.UUID = settings.VLESS.ID
			spec.Flow = string(settings.VLESS.Flow)
		case "trojan":
			spec.Password = settings.Trojan.Password
			spec.Flow = string(settings.Trojan.Flow)
		case "shadowsocks":
			spec.Password = settings.Shadowsocks.Password
			spec.Method = string(settings.Shadowsocks.Method)
		default:
			continue
		}
		usersByProtocol[p.Type] = append(usersByProtocol[p.Type], spec)
	}

	inbounds := make([]nodeConfigInboundSpec, 0, len(inboundRows))
	for _, in := range inboundRows {
		spec := nodeConfigInboundSpec{
			Tag: in.Tag, Protocol: in.Protocol, ListenPort: uint16(in.Port.Int32),
			ListenPorts: multiListenPorts(in.Ports),
			Users:       usersByProtocol[in.Protocol],
		}
		switch in.Security {
		case "reality":
			tls := &nodeConfigTLSSpec{
				Reality: &nodeConfigRealitySpec{
					PrivateKey: in.RealityPrivateKey.String,
					ShortID:    in.RealityShortIds,
				},
			}
			tls.Reality.Handshake.ServerName = in.RealityServerName.String
			tls.Reality.Handshake.ServerPort = uint16(in.RealityServerPort.Int32)
			spec.TLS = tls
		case "tls":
			// ListAutoSyncInbounds only returns a 'tls' row once both are
			// non-empty (see that query's own doc comment) - Certificate/Key
			// are never blank here.
			spec.TLS = &nodeConfigTLSSpec{
				ServerName:  in.TlsServerName.String,
				Certificate: in.TlsCertificate.String,
				Key:         in.TlsKey.String,
			}
		}
		if spec.Users == nil {
			spec.Users = []nodeConfigUserSpec{}
		}
		inbounds = append(inbounds, spec)
	}

	// One encode per protocol, reused by every inbound of that protocol - see
	// nodeConfigInboundWire for why.
	usersJSON := make(map[string]json.RawMessage, len(usersByProtocol))
	for proto, list := range usersByProtocol {
		raw, mErr := json.Marshal(list)
		if mErr != nil {
			return nil, mErr
		}
		usersJSON[proto] = raw
	}

	return &nodeConfigSnapshot{
		version: version, builtAt: time.Now(),
		inbounds: inbounds, usersJSON: usersJSON, core: toCoreConfigDTO(coreRow),
	}, nil
}

// render produces one node profile's payload from the snapshot, together with
// the canonical bytes it hashed to produce Version - those bytes are the
// response body bar its first field, see nodeConfigBodyBytes. Marshalling this
// structure is expensive enough (~14 MB in production) that handing the
// result back is worth the slightly wider signature; the alternative was
// marshalling it a second time purely to reproduce what this already had in
// hand.
func (s *nodeConfigSnapshot) render(p nodeProfile) (nodeConfigResponse, []byte, error) {
	inbounds := p.filterInbounds(s.inbounds)
	core := p.CoreOverrides.apply(s.core)
	payload := nodeConfigResponse{Inbounds: inbounds, Core: core}

	// emptyUsers matches what the spec form emits for an inbound with no users.
	emptyUsers := json.RawMessage("[]")
	wire := make([]nodeConfigInboundWire, 0, len(inbounds))
	for i := range inbounds {
		in := &inbounds[i]
		users, ok := s.usersJSON[in.Protocol]
		if !ok {
			users = emptyUsers
		}
		wire = append(wire, nodeConfigInboundWire{
			Tag: in.Tag, Protocol: in.Protocol, ListenPort: in.ListenPort, ListenPorts: in.ListenPorts,
			Users: users, TLS: in.TLS,
		})
	}

	canonical, err := json.Marshal(struct {
		Inbounds []nodeConfigInboundWire `json:"inbounds"`
		Core     coreConfigDTO           `json:"core"`
	}{wire, core})
	if err != nil {
		return nodeConfigResponse{}, nil, err
	}
	sum := sha256.Sum256(canonical)
	payload.Version = hex.EncodeToString(sum[:])
	return payload, canonical, nil
}

// buildNodeConfigPayload reads the database and renders one profile's
// payload without touching the cache - the reference the cached path is
// tested against.
func (h *Handler) buildNodeConfigPayload(ctx context.Context, p nodeProfile) (nodeConfigResponse, []byte, error) {
	snap, err := h.loadNodeConfigSnapshot(ctx, 0)
	if err != nil {
		return nodeConfigResponse{}, nil, err
	}
	return snap.render(p)
}

// nodeConfigBodyBytes returns the exact bytes GET /api/internal/node-config
// answers with - identical JSON to marshalling the payload, produced with ONE
// marshal instead of three.
//
// Why that matters: this body is ~14 MB on the production fleet (every
// active user's credentials, repeated once per inbound of their protocol),
// and four nodes re-pull it every few seconds. Marshalling it once for the
// version hash, once for the cache entry and once for the response made this
// single endpoint 662 ms per call and the panel's largest CPU consumer by
// far (79 s of CPU per 5 minutes, ~4x the entire user-list traffic of every
// reseller bot combined).
//
// The splice is exact rather than clever: the canonical struct always
// marshals both fields, so it always begins `{"inbounds":` - putting
// `"version":"..."` directly after the opening brace yields byte-for-byte
// what marshalling nodeConfigResponse produces, since Version is its first
// field. The guard covers the impossible case rather than trusting it.
func nodeConfigBodyBytes(payload nodeConfigResponse, canonical []byte) ([]byte, error) {
	if len(canonical) < 2 || canonical[0] != '{' || canonical[1] != '"' {
		return json.Marshal(payload)
	}
	b := make([]byte, 0, len(canonical)+len(payload.Version)+14)
	b = append(b, `{"version":"`...)
	b = append(b, payload.Version...)
	b = append(b, `",`...)
	b = append(b, canonical[1:]...)
	return b, nil
}

// buildNodeConfigBody is buildNodeConfigPayload plus nodeConfigBodyBytes.
func (h *Handler) buildNodeConfigBody(ctx context.Context, p nodeProfile) (string, error) {
	payload, canonical, err := h.buildNodeConfigPayload(ctx, p)
	if err != nil {
		return "", err
	}
	body, err := nodeConfigBodyBytes(payload, canonical)
	return string(body), err
}

const nodeRowContextKey = "node.row"

// nodeFromContext is the calling node's row: requireNodeSecret already read
// it to authenticate, so the profile costs no second query.
func (h *Handler) nodeFromContext(c *gin.Context) (generated.Node, error) {
	if v, ok := c.Get(nodeRowContextKey); ok {
		if n, ok := v.(generated.Node); ok {
			return n, nil
		}
	}
	return h.store.Queries.GetNodeByID(c.Request.Context(), c.MustGet(nodeIDContextKey).(int32))
}

// etagMatches implements If-None-Match's list, weak-tag and "*" forms.
func etagMatches(header, etag string) bool {
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "*" || strings.TrimPrefix(part, "W/") == etag {
			return true
		}
	}
	return false
}

// handleGetNodeConfig implements GET /api/internal/node-config - a node's
// pull half of the config-sync mechanism (see cmd/node/main.go's pull
// loop). Authenticated the same way as POST /api/internal/node-report
// (requireNodeSecret), which also tells it WHICH node is asking: each node
// is served the payload of its own profile (see nodeProfile), and nodes with
// equal profiles share one cached body.
//
// A poll whose data version is unchanged is answered from memory - see
// cachedNodeConfig - and a node that echoes the ETag back in If-None-Match
// gets an empty 304 instead of the whole body again.
func (h *Handler) handleGetNodeConfig(c *gin.Context) {
	ctx := c.Request.Context()
	node, err := h.nodeFromContext(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not load the node"})
		return
	}
	profile, err := profileFromNode(node)
	if err != nil {
		// Failing closed keeps the node on the config it already runs;
		// falling back to the default would make it serve every inbound.
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Node profile is invalid: " + err.Error()})
		return
	}
	entry, err := h.cachedNodeConfig(ctx, profile)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not build node config"})
		return
	}
	c.Header("ETag", entry.etag)
	if etagMatches(c.GetHeader("If-None-Match"), entry.etag) {
		c.AbortWithStatus(http.StatusNotModified)
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", entry.body)
}
