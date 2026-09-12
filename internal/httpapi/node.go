package httpapi

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/legendary1205/rapido-go/internal/certs"
	"github.com/legendary1205/rapido-go/internal/db/generated"
)

// generateReportSecret mirrors cmd/panel/main.go's ensureJWTSecret pattern
// (crypto/rand + hex) for the bearer token a node presents on every push -
// see migration 00006's doc comment.
func generateReportSecret() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// defaultNodePort/defaultNodeAPIPort mirror NodeCreate's own defaults on
// the real panel - a client that posts only {name, address} (the
// documented minimal body) must succeed there, so port/api_port cannot be
// required here either.
const (
	defaultNodePort    = 62050
	defaultNodeAPIPort = 62051
)

type nodeCreateRequest struct {
	Name             string   `json:"name" binding:"required"`
	Address          string   `json:"address" binding:"required"`
	Port             int32    `json:"port"`
	APIPort          int32    `json:"api_port"`
	UsageCoefficient *float64 `json:"usage_coefficient"`
	// PanelURL is optional and used for nothing but embedding in the setup
	// blob below - the admin already knows how this node should reach the
	// panel (an internal IP, a specific scheme/port), so there's no reason
	// to guess it. Left empty, the blob just omits panel_url and the node
	// falls back to its own PANEL_URL env var, same as before this existed.
	PanelURL string `json:"panel_url"`
}

// nodeSetupBlob is everything cmd/node needs to trust and reach the panel,
// bundled into one value - see handleCreateNode's own doc comment on why
// this replaced four separately-copied fields. Field names are deliberately
// short (not "certificate"/"report_secret") since this is the wire shape a
// human pastes as one blob, not a browsable API response - keeping it small
// keeps the copy-pasted text itself shorter.
type nodeSetupBlob struct {
	Cert     string `json:"cert"`
	Key      string `json:"key"`
	CA       string `json:"ca"`
	Secret   string `json:"secret"`
	PanelURL string `json:"panel_url,omitempty"`
}

// buildNodeSetupBlob base64-encodes a compact JSON envelope of everything a
// node needs to bootstrap itself - see cmd/node/main.go's NODE_SETUP_BLOB
// consumer, the other half of this pair.
func buildNodeSetupBlob(cert, key, ca, secret, panelURL string) (string, error) {
	raw, err := json.Marshal(nodeSetupBlob{Cert: cert, Key: key, CA: ca, Secret: secret, PanelURL: panelURL})
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// nodeDTO mirrors app/models/node.py's NodeResponse. xray_version and
// message are nullable there but always present - a monitoring client
// reads message to find out WHY a node isn't connected, and omitting the
// key entirely turns that into a KeyError instead of a null.
type nodeDTO struct {
	ID               int32   `json:"id"`
	Name             string  `json:"name"`
	Address          string  `json:"address"`
	Port             int32   `json:"port"`
	APIPort          int32   `json:"api_port"`
	XrayVersion      *string `json:"xray_version"`
	Status           string  `json:"status"`
	Message          *string `json:"message"`
	UsageCoefficient float64 `json:"usage_coefficient"`
}

func toNodeDTO(n generated.Node) nodeDTO {
	dto := nodeDTO{
		ID: n.ID, Name: n.Name, Address: n.Address, Port: n.Port, APIPort: n.ApiPort,
		Status: n.Status, UsageCoefficient: n.UsageCoefficient,
	}
	if n.XrayVersion.Valid {
		v := n.XrayVersion.String
		dto.XrayVersion = &v
	}
	if n.Message.Valid {
		m := n.Message.String
		dto.Message = &m
	}
	return dto
}

// handleCreateNode implements POST /api/node (sudo only). Unlike the
// current Python system - where the admin manually pastes the panel's
// certificate into the node's config, and the node's own certificate is
// never verified, only TOFU'd on every reconnect (see the Phase 3 research
// note in memory) - this issues the new node a real leaf certificate
// signed by the panel's own CA (the same self-signed CN="Rapido" cert
// Phase 1 already generates into the `tls` table, which doubles as this
// CA since it was generated with IsCA:true). The response carries
// everything needed to bring the node online: its own cert+key and the CA
// cert, so the node can require and verify the panel's client certificate
// too, instead of trusting whatever connects.
//
// Provisioning used to mean copying four separate values (cert, key, ca,
// report_secret) into three files plus two env vars on the node server -
// the response now also bundles all of it into one base64 blob
// (setup_blob) the admin pastes as a single NODE_SETUP_BLOB value; the node
// writes its own files out and starts. Port/listen-address settings are
// deliberately NOT part of the blob - those are ordinary node-local config
// the admin still sets separately (NODE_LISTEN_ADDR etc.), not identity or
// trust material.
func (h *Handler) handleCreateNode(c *gin.Context) {
	var req nodeCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": err.Error()})
		return
	}
	usageCoefficient := 1.0
	if req.UsageCoefficient != nil {
		usageCoefficient = *req.UsageCoefficient
	}
	if req.Port == 0 {
		req.Port = defaultNodePort
	}
	if req.APIPort == 0 {
		req.APIPort = defaultNodeAPIPort
	}

	ctx := c.Request.Context()
	ca, err := h.store.Queries.GetTLS(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not load the Rapido CA"})
		return
	}
	caKey, err := certs.ParseRSAPrivateKeyPEM(ca.Key)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read the Rapido CA key"})
		return
	}
	nodeCert, err := certs.SignNodeCert(req.Name, ca.Certificate, caKey)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not issue a certificate for this node"})
		return
	}
	reportSecret, err := generateReportSecret()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not generate a report secret for this node"})
		return
	}

	node, err := h.store.Queries.CreateNode(ctx, generated.CreateNodeParams{
		Name: req.Name, Address: req.Address, Port: req.Port, ApiPort: req.APIPort, UsageCoefficient: usageCoefficient,
		ReportSecret: pgtype.Text{String: reportSecret, Valid: true},
	})
	if err != nil {
		if isUniqueViolation(err) {
			c.JSON(http.StatusConflict, gin.H{"detail": "A node with this name already exists"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not create node"})
		return
	}

	setupBlob, err := buildNodeSetupBlob(nodeCert.CertPEM, nodeCert.KeyPEM, ca.Certificate, reportSecret, req.PanelURL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not build the setup blob"})
		return
	}

	// Every value below - the blob and its four raw components alike - is
	// returned here and only here, never retrievable again through any
	// later GET. setup_blob is what the dashboard's reveal panel shows by
	// default (paste once into NODE_SETUP_BLOB, see cmd/node/main.go); the
	// four raw fields stay in the response for a manual/scripted setup or
	// for inspecting what's actually inside the blob, not because the
	// dashboard still shows them as the primary flow.
	//
	// The node's own fields are emitted at the TOP level, matching the real
	// panel's NodeResponse - a client doing resp["id"] right after creating
	// a node must find it there. The nested "node" key is kept as well so
	// this panel's own dashboard keeps working; extra keys break nobody.
	merged, err := mergeJSONObjects(toNodeDTO(node), gin.H{
		"node":           toNodeDTO(node),
		"setup_blob":     setupBlob,
		"certificate":    nodeCert.CertPEM,
		"key":            nodeCert.KeyPEM,
		"ca_certificate": ca.Certificate,
		"report_secret":  reportSecret,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not build the node response"})
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", merged)
}

// nodeUpdateRequest is fully optional, field by field: the real panel's
// NodeModify has no required fields at all, so PUT {"usage_coefficient":2}
// or PUT {"status":"disabled"} - the documented way to disable a node - has
// to work without resending the whole object.
type nodeUpdateRequest struct {
	Name             *string  `json:"name"`
	Address          *string  `json:"address"`
	Port             *int32   `json:"port"`
	APIPort          *int32   `json:"api_port"`
	UsageCoefficient *float64 `json:"usage_coefficient"`
	// Status is the real panel's own spelling ("disabled" switches a node
	// off, anything else puts it back to connecting); Disabled is this
	// panel's older boolean, kept working for its own dashboard.
	Status   *string `json:"status"`
	Disabled *bool   `json:"disabled"`
}

// handleUpdateNode implements PUT /api/node/:id (sudo only). A full-field
// update - the dashboard's edit form always sends the whole node back, not
// a partial patch - except usage_coefficient/disabled, which stay
// unchanged when omitted (see UpdateNode's own doc comment).
func (h *Handler) handleUpdateNode(c *gin.Context) {
	id, err := parseIDParam(c, "id")
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "invalid node id"})
		return
	}
	var req nodeUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": err.Error()})
		return
	}

	ctx := c.Request.Context()
	// Every field is optional, so the stored row is the base and only what
	// the caller actually sent is overlaid onto it.
	current, err := h.store.Queries.GetNodeByID(ctx, id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"detail": "Node not found"})
		return
	}
	name, address, port, apiPort := current.Name, current.Address, current.Port, current.ApiPort
	if req.Name != nil {
		name = *req.Name
	}
	if req.Address != nil {
		address = *req.Address
	}
	if req.Port != nil {
		port = *req.Port
	}
	if req.APIPort != nil {
		apiPort = *req.APIPort
	}

	var usageCoefficient pgtype.Float8
	if req.UsageCoefficient != nil {
		usageCoefficient = pgtype.Float8{Float64: *req.UsageCoefficient, Valid: true}
	}
	var disabled pgtype.Bool
	if req.Disabled != nil {
		disabled = pgtype.Bool{Bool: *req.Disabled, Valid: true}
	}
	// status wins over disabled when both are sent - it's the documented
	// field, and the two can't disagree without the caller contradicting
	// itself.
	if req.Status != nil {
		disabled = pgtype.Bool{Bool: *req.Status == "disabled", Valid: true}
	}

	node, err := h.store.Queries.UpdateNode(ctx, generated.UpdateNodeParams{
		ID: id, Name: name, Address: address, Port: port, ApiPort: apiPort,
		UsageCoefficient: usageCoefficient, Disabled: disabled,
	})
	if err != nil {
		if isUniqueViolation(err) {
			c.JSON(http.StatusConflict, gin.H{"detail": "A node with this name already exists"})
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"detail": "Node not found"})
		return
	}
	c.JSON(http.StatusOK, toNodeDTO(node))
}

// handleListNodes implements GET /api/nodes (sudo only).
func (h *Handler) handleListNodes(c *gin.Context) {
	nodes, err := h.store.Queries.ListNodes(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not list nodes"})
		return
	}
	out := make([]nodeDTO, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, toNodeDTO(n))
	}
	c.JSON(http.StatusOK, out)
}

// handleGetNode implements GET /api/node/:id (sudo only).
func (h *Handler) handleGetNode(c *gin.Context) {
	id, err := parseIDParam(c, "id")
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "invalid node id"})
		return
	}
	node, err := h.store.Queries.GetNodeByID(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"detail": "Node not found"})
		return
	}
	c.JSON(http.StatusOK, toNodeDTO(node))
}

// handleDeleteNode implements DELETE /api/node/:id (sudo only).
func (h *Handler) handleDeleteNode(c *gin.Context) {
	id, err := parseIDParam(c, "id")
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "invalid node id"})
		return
	}
	if err := h.store.Queries.DeleteNode(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not delete node"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"detail": "Node removed successfully"})
}

// nodeUsageDTO mirrors app/models/node.py's NodeUsageResponse - node_id is
// nullable there because the list always leads with a "Master" row for the
// panel's own core.
type nodeUsageDTO struct {
	NodeID   *int32 `json:"node_id"`
	NodeName string `json:"node_name"`
	Uplink   int64  `json:"uplink"`
	Downlink int64  `json:"downlink"`
}

// handleGetNodesUsage implements GET /api/nodes/usage?start=&end= (sudo
// only) - RFC3339 bounds, defaulting to the trailing 30 days when omitted.
// Every node gets a row even with zero traffic in the window (see
// GetNodesUsage's own doc comment).
func (h *Handler) handleGetNodesUsage(c *gin.Context) {
	// Same ?start=/?end= contract as every other usage endpoint: several
	// ISO spellings accepted, an inverted range rejected outright rather
	// than silently answered with the 30-day default.
	start, end, ok := usageWindow(c)
	if !ok {
		return
	}

	rows, err := h.store.Queries.GetNodesUsage(c.Request.Context(), generated.GetNodesUsageParams{
		CreatedAt: timestamptzFromTime(start), CreatedAt_2: timestamptzFromTime(end),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read node usage"})
		return
	}
	// The real panel's list always leads with a "Master" row for the
	// panel's own core. There is no local core here (this rewrite's node
	// agent is always a separate process), so its totals are genuinely
	// zero - but the row itself has to exist, because clients locate the
	// series with usages[0] or by matching node_id === null.
	out := make([]nodeUsageDTO, 0, len(rows)+1)
	out = append(out, nodeUsageDTO{NodeID: nil, NodeName: "Master"})
	for _, r := range rows {
		id := r.NodeID
		out = append(out, nodeUsageDTO{NodeID: &id, NodeName: r.NodeName, Uplink: r.Uplink, Downlink: r.Downlink})
	}
	c.JSON(http.StatusOK, gin.H{"usages": out})
}
