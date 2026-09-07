package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/legendary1205/rapido-go/internal/db/generated"
	"github.com/legendary1205/rapido-go/internal/gatewayclient"
)

// randomGatewaySecret mirrors cmd/panel/main.go's ensureJWTSecret - 32
// random bytes, hex-independent (base64url here since this value travels
// in an HTTP Authorization header and gets copy-pasted by an admin, where
// URL-safe, no-padding characters are friendlier than raw hex is ugly but
// hex would work equally well; base64url is simply shorter to paste).
func randomGatewaySecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// ensureGatewaySettings returns this panel's own gateway_settings row,
// generating a fresh secret on first access if none exists yet - same
// get-or-create-at-first-use shape as cmd/panel/main.go's ensureJWTSecret,
// except done lazily here (on first admin GET/peer call) rather than at
// process boot, since unlike the JWT secret this one is never needed
// before the first Gateway-related request ever arrives.
func (h *Handler) ensureGatewaySettings(ctx context.Context) (generated.GatewaySetting, error) {
	existing, err := h.store.Queries.GetGatewaySecret(ctx)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return generated.GatewaySetting{}, err
	}
	secret, err := randomGatewaySecret()
	if err != nil {
		return generated.GatewaySetting{}, err
	}
	return h.store.Queries.CreateGatewaySecret(ctx, secret)
}

// requireGatewaySecret authenticates an incoming panel-to-panel call the
// same way requireNodeSecret authenticates a node's report push: a plain
// bearer token, checked by direct comparison against this panel's own
// gateway_settings.secret - not per-peer (see the Gateway migration's own
// doc comment on why this is asymmetric/per-installation), so any peer
// that was handed this panel's secret can call any /internal/gateway/*
// endpoint here.
func (h *Handler) requireGatewaySecret(c *gin.Context) {
	const prefix = "Bearer "
	header := c.GetHeader("Authorization")
	if !strings.HasPrefix(header, prefix) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"detail": "Missing gateway secret"})
		return
	}
	secret := strings.TrimPrefix(header, prefix)
	settings, err := h.ensureGatewaySettings(c.Request.Context())
	if err != nil || secret == "" || secret != settings.Secret {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"detail": "Invalid gateway secret"})
		return
	}
	c.Next()
}

// handleGatewayPing implements GET /api/internal/gateway/ping - the first
// thing a peer calls (and what the admin UI's "Test connection" button
// hits) to confirm the secret it was given is actually accepted here.
// Deliberately returns nothing beyond a human-recognizable label: a peer
// has no legitimate use for this panel's user counts, node list, or
// anything else at this endpoint - that's what the (separately gated,
// later sub-phase) status endpoint is for.
func (h *Handler) handleGatewayPing(c *gin.Context) {
	settings, err := h.ensureGatewaySettings(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "could not load gateway settings: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gatewayclient.PingResult{PanelName: settings.Name})
}

// --- admin-facing settings -------------------------------------------------

type gatewaySettingsDTO struct {
	Name   string `json:"name"`
	Secret string `json:"secret"`
}

// handleGetGatewaySettings implements GET /api/settings/gateway (sudo
// only) - unlike every masked secret in settings.go, this one is returned
// in full: its entire purpose is to be copy-pasted into a peer panel's
// "add peer" form, so masking it would defeat the point.
func (h *Handler) handleGetGatewaySettings(c *gin.Context) {
	settings, err := h.ensureGatewaySettings(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "could not load gateway settings: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gatewaySettingsDTO{Name: settings.Name, Secret: settings.Secret})
}

type updateGatewaySettingsRequest struct {
	Name         *string `json:"name"`
	RotateSecret bool    `json:"rotate_secret"`
}

// handleUpdateGatewaySettings implements PUT /api/settings/gateway (sudo
// only): renaming this panel and/or rotating its secret are independent
// actions in one request - rotating invalidates every peer that was
// configured with the old value until the admin updates them there too,
// so it's opt-in via an explicit flag rather than happening as a side
// effect of any other edit.
func (h *Handler) handleUpdateGatewaySettings(c *gin.Context) {
	var req updateGatewaySettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": err.Error()})
		return
	}
	settings, err := h.ensureGatewaySettings(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "could not load gateway settings: " + err.Error()})
		return
	}
	if req.Name != nil {
		settings, err = h.store.Queries.SetGatewayName(c.Request.Context(), *req.Name)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"detail": "could not save the name: " + err.Error()})
			return
		}
	}
	if req.RotateSecret {
		newSecret, err := randomGatewaySecret()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"detail": "could not generate a new secret: " + err.Error()})
			return
		}
		settings, err = h.store.Queries.RotateGatewaySecret(c.Request.Context(), newSecret)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"detail": "could not rotate the secret: " + err.Error()})
			return
		}
	}
	c.JSON(http.StatusOK, gatewaySettingsDTO{Name: settings.Name, Secret: settings.Secret})
}

// --- admin-facing peer CRUD -------------------------------------------------

type gatewayPeerDTO struct {
	ID      int32  `json:"id"`
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	Secret  string `json:"secret"`
	Enabled bool   `json:"enabled"`
}

func toGatewayPeerDTO(p generated.GatewayPeer) gatewayPeerDTO {
	return gatewayPeerDTO{ID: p.ID, Name: p.Name, BaseURL: p.BaseUrl, Secret: p.Secret, Enabled: p.Enabled}
}

func (h *Handler) handleListGatewayPeers(c *gin.Context) {
	rows, err := h.store.Queries.ListGatewayPeers(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": err.Error()})
		return
	}
	out := make([]gatewayPeerDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, toGatewayPeerDTO(r))
	}
	c.JSON(http.StatusOK, out)
}

type gatewayPeerRequest struct {
	Name    string `json:"name" binding:"required"`
	BaseURL string `json:"base_url" binding:"required"`
	Secret  string `json:"secret" binding:"required"`
	Enabled *bool  `json:"enabled"`
}

// handleCreateGatewayPeer implements POST /api/settings/gateway/peers
// (sudo only). base_url is stored exactly as given, trailing slash and
// all - gatewayclient.Ping (and every later sub-phase's peer calls)
// concatenates "/api/internal/gateway/..." directly onto it, so a
// trailing slash would silently produce a double slash. Trimmed here once
// rather than defended against in every caller.
func (h *Handler) handleCreateGatewayPeer(c *gin.Context) {
	var req gatewayPeerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": err.Error()})
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	created, err := h.store.Queries.CreateGatewayPeer(c.Request.Context(), generated.CreateGatewayPeerParams{
		Name: req.Name, BaseUrl: strings.TrimRight(req.BaseURL, "/"), Secret: req.Secret, Enabled: enabled,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, toGatewayPeerDTO(created))
}

func (h *Handler) handleUpdateGatewayPeer(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "invalid peer id"})
		return
	}
	var req gatewayPeerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": err.Error()})
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	updated, err := h.store.Queries.UpdateGatewayPeer(c.Request.Context(), generated.UpdateGatewayPeerParams{
		ID: int32(id), Name: req.Name, BaseUrl: strings.TrimRight(req.BaseURL, "/"), Secret: req.Secret, Enabled: enabled,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"detail": "peer not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, toGatewayPeerDTO(updated))
}

func (h *Handler) handleDeleteGatewayPeer(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "invalid peer id"})
		return
	}
	if err := h.store.Queries.DeleteGatewayPeer(c.Request.Context(), int32(id)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

// handleTestGatewayPeer implements POST /api/settings/gateway/peers/:id/test
// (sudo only) - a real outbound call to the peer's own ping endpoint, not
// just a syntax check of the stored URL/secret, so "connection OK" in the
// UI actually means the peer accepted this exact secret.
func (h *Handler) handleTestGatewayPeer(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "invalid peer id"})
		return
	}
	peer, err := h.store.Queries.GetGatewayPeer(c.Request.Context(), int32(id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"detail": "peer not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"detail": err.Error()})
		return
	}
	result, err := gatewayclient.Ping(c.Request.Context(), peer.BaseUrl, peer.Secret)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "panel_name": result.PanelName})
}
