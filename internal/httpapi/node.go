package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/legendary1205/rapido-go/internal/certs"
	"github.com/legendary1205/rapido-go/internal/db/generated"
)

type nodeCreateRequest struct {
	Name             string   `json:"name" binding:"required"`
	Address          string   `json:"address" binding:"required"`
	Port             int32    `json:"port" binding:"required"`
	APIPort          int32    `json:"api_port" binding:"required"`
	UsageCoefficient *float64 `json:"usage_coefficient"`
}

type nodeDTO struct {
	ID               int32   `json:"id"`
	Name             string  `json:"name"`
	Address          string  `json:"address"`
	Port             int32   `json:"port"`
	APIPort          int32   `json:"api_port"`
	Status           string  `json:"status"`
	UsageCoefficient float64 `json:"usage_coefficient"`
}

func toNodeDTO(n generated.Node) nodeDTO {
	return nodeDTO{
		ID: n.ID, Name: n.Name, Address: n.Address, Port: n.Port, APIPort: n.ApiPort,
		Status: n.Status, UsageCoefficient: n.UsageCoefficient,
	}
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

	node, err := h.store.Queries.CreateNode(ctx, generated.CreateNodeParams{
		Name: req.Name, Address: req.Address, Port: req.Port, ApiPort: req.APIPort, UsageCoefficient: usageCoefficient,
	})
	if err != nil {
		if isUniqueViolation(err) {
			c.JSON(http.StatusConflict, gin.H{"detail": "A node with this name already exists"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not create node"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"node":           toNodeDTO(node),
		"certificate":    nodeCert.CertPEM,
		"key":            nodeCert.KeyPEM,
		"ca_certificate": ca.Certificate,
	})
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
