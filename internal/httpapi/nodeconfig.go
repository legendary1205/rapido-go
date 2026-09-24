package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/legendary1205/rapido-go/internal/cache"
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
// cmd/node/main.go's pull loop. Every node in the fleet is served the
// identical payload (this schema has no per-node inbound assignment, same
// as the current Python system), so Version is a single fleet-wide hash,
// not per-node.
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

// buildNodeConfigPayload is the Go equivalent of
// XRayConfig.include_db_users(): groups every active/on_hold user's proxy
// credentials by protocol, and stuffs them into every auto-sync-eligible
// inbound of that protocol. An inbound is one row per tag however many
// ports its hosts use - those go out as listen_ports.
//
// It returns the canonical bytes it hashed to produce Version alongside the
// payload, because those bytes are the response body bar its first field -
// see buildNodeConfigBody. Marshalling this structure is expensive enough
// (~14 MB in production) that handing the result back is worth the slightly
// wider signature; the alternative was marshalling it a second time purely
// to reproduce what this function already had in hand.
func (h *Handler) buildNodeConfigPayload(ctx context.Context) (nodeConfigResponse, []byte, error) {
	inboundRows, err := h.store.Queries.ListAutoSyncInbounds(ctx)
	if err != nil {
		return nodeConfigResponse{}, nil, err
	}
	proxyRows, err := h.store.Queries.ListActiveUserProxiesForNodeConfig(ctx)
	if err != nil {
		return nodeConfigResponse{}, nil, err
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

	coreRow, err := h.store.CachedGetCoreConfig(ctx)
	if err != nil {
		return nodeConfigResponse{}, nil, err
	}
	core := toCoreConfigDTO(coreRow)

	payload := nodeConfigResponse{Inbounds: inbounds, Core: core}

	// One encode per protocol, reused by every inbound of that protocol -
	// see nodeConfigInboundWire for why. emptyUsers matches what the spec
	// form emits for the `if spec.Users == nil` case set above.
	emptyUsers := json.RawMessage("[]")
	usersJSON := make(map[string]json.RawMessage, len(usersByProtocol))
	for proto, list := range usersByProtocol {
		raw, mErr := json.Marshal(list)
		if mErr != nil {
			return nodeConfigResponse{}, nil, mErr
		}
		usersJSON[proto] = raw
	}
	wire := make([]nodeConfigInboundWire, 0, len(inbounds))
	for i := range inbounds {
		in := &inbounds[i]
		users, ok := usersJSON[in.Protocol]
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

// buildNodeConfigBody returns the exact bytes GET /api/internal/node-config
// answers with - identical JSON to marshalling buildNodeConfigPayload's
// result, produced with ONE marshal instead of three.
//
// Why that matters: this body is ~14 MB on the production fleet (every
// active user's credentials, repeated once per inbound of their protocol),
// and four nodes re-pull it every few seconds. The previous shape marshalled
// it three times per rebuild - once for the version hash, once for the cache
// entry, once for the response - and on a cache HIT still paid a full
// unmarshal into Go structs plus a fresh marshal out, because the cache
// stored a typed value rather than the bytes. Measured on production, that
// made this single endpoint 662 ms per call and the panel's largest CPU
// consumer by far (79 s of CPU per 5 minutes, ~4x the entire user-list
// traffic of every reseller bot combined).
//
// The splice is exact rather than clever: the canonical struct always
// marshals both fields, so it always begins `{"inbounds":` - putting
// `"version":"..."` directly after the opening brace yields byte-for-byte
// what marshalling nodeConfigResponse produces, since Version is its first
// field. The guard covers the impossible case rather than trusting it.
func (h *Handler) buildNodeConfigBody(ctx context.Context) (string, error) {
	payload, canonical, err := h.buildNodeConfigPayload(ctx)
	if err != nil {
		return "", err
	}
	if len(canonical) < 2 || canonical[0] != '{' || canonical[1] != '"' {
		full, mErr := json.Marshal(payload)
		if mErr != nil {
			return "", mErr
		}
		return string(full), nil
	}
	var b strings.Builder
	b.Grow(len(canonical) + len(payload.Version) + 14)
	b.WriteString(`{"version":"`)
	b.WriteString(payload.Version)
	b.WriteString(`",`)
	b.Write(canonical[1:])
	return b.String(), nil
}

// handleGetNodeConfig implements GET /api/internal/node-config - a node's
// pull half of the config-sync mechanism (see cmd/node/main.go's pull
// loop). Authenticated the same way as POST /api/internal/node-report
// (requireNodeSecret), but every node receives the identical payload -
// there's no per-node inbound assignment in this architecture, so the
// secret only proves "this is a real node," not "which one."
//
// The cache holds the finished response body, so a hit writes bytes
// straight to the wire - no decode, no re-encode. See buildNodeConfigBody.
func (h *Handler) handleGetNodeConfig(c *gin.Context) {
	ctx := c.Request.Context()
	body, err := cache.GetOrSetString(ctx, h.store.Cache, cache.NodeConfigKey(), nodeConfigCacheTTL,
		h.buildNodeConfigBody)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not build node config"})
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", []byte(body))
}
