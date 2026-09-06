package httpapi

import (
	"context"
	_ "embed"
	"encoding/json"
	"html/template"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/legendary1205/rapido-go/internal/db/generated"
	"github.com/legendary1205/rapido-go/internal/subscription"
)

//go:embed templates/subscription_page.html
var subscriptionPageSource string

var subscriptionPageTemplate = template.Must(template.New("subscription_page").Parse(subscriptionPageSource))

// rapidoData is exactly the JSON shape both the HTML page's embedded
// window.__RAPIDO__ blob and GET /sub/:token/info return - the page's own
// 60s background poll re-fetches this same shape and merges it into its
// client-side state, so the two must never drift apart.
type rapidoData struct {
	Username      string   `json:"username"`
	Status        string   `json:"status"`
	Used          int64    `json:"used"`
	Lifetime      int64    `json:"lifetime"`
	Limit         *int64   `json:"limit"`
	Reset         string   `json:"reset"`
	Expire        *int64   `json:"expire"`
	OnlineAt      *string  `json:"onlineAt"`
	EmergencyUsed bool     `json:"emergencyUsed"`
	Links         []string `json:"links"`
	SubURL        string   `json:"subUrl"`
}

// buildRapidoData assembles rapidoData for one user - shared by the HTML
// page's initial load and its /info poll endpoint.
func (h *Handler) buildRapidoData(ctx context.Context, user generated.User) (rapidoData, error) {
	links, err := h.buildUserLinks(ctx, user)
	if err != nil {
		return rapidoData{}, err
	}
	data := rapidoData{
		Username: user.Username,
		Status:   user.Status,
		Used:     user.UsedTraffic,
		// lifetime_used_traffic (used_traffic + sum of user_usage_logs) isn't
		// tracked yet - same deferred-feature gap already flagged in
		// buildUserResponses (internal/httpapi/user.go). Falls back to the
		// current usage, matching that same choice.
		Lifetime:      user.UsedTraffic,
		Reset:         user.DataLimitResetStrategy,
		EmergencyUsed: user.EmergencyUsedAt.Valid,
		Links:         links,
	}
	if user.DataLimit.Valid {
		data.Limit = &user.DataLimit.Int64
	}
	if user.Expire.Valid {
		e := int64(user.Expire.Int32)
		data.Expire = &e
	}
	if user.OnlineAt.Valid {
		s := user.OnlineAt.Time.UTC().Format(time.RFC3339)
		data.OnlineAt = &s
	}
	data.SubURL = h.subURLPrefix + "/sub/" + subscription.CreateToken(user.Username, h.jwtSecret)
	return data, nil
}

// subscriptionPageHeaders mirrors app/routers/subscription.py's
// SUBSCRIPTION_PAGE_HEADERS exactly - a strict CSP with no external CDN
// allowed anywhere (every asset on this page is self-hosted/inline), since
// this page's own inline <script>/<style> tags need to survive it.
func setSubscriptionPageHeaders(c *gin.Context) {
	c.Header("Cache-Control", "no-store, max-age=0")
	c.Header("Pragma", "no-cache")
	c.Header("Content-Security-Policy",
		"default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; "+
			"img-src data:; font-src 'self'; connect-src 'self'; base-uri 'none'; "+
			"form-action 'none'; frame-ancestors 'none'")
	c.Header("X-Frame-Options", "DENY")
	c.Header("X-Content-Type-Options", "nosniff")
}

// handleSubscriptionPage renders the customer-facing HTML subscription
// page (Overview/Apps/Servers/Support tabs) - a vanilla HTML/CSS/JS page
// with no build step and no external dependency, matching Python's own
// app/templates/subscription/index.html almost verbatim (only the
// <title>/window.__RAPIDO__ interpolation points are backend-specific;
// every other line - CSS, the self-contained QR generator, the bilingual
// i18n dictionaries, the per-platform app data - is static content, not
// server logic, and is carried over unchanged). Does NOT call
// recordSubUserAgent - loading the browser page must not bump
// sub_updated_at/sub_last_user_agent, matching the Python HTML branch.
func (h *Handler) handleSubscriptionPage(c *gin.Context, user generated.User) {
	data, err := h.buildRapidoData(c.Request.Context(), user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read subscription info"})
		return
	}
	raw, err := json.Marshal(data)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read subscription info"})
		return
	}

	setSubscriptionPageHeaders(c)
	c.Status(http.StatusOK)
	c.Header("Content-Type", "text/html; charset=utf-8")
	_ = subscriptionPageTemplate.Execute(c.Writer, struct {
		Username       string
		RapidoDataJSON template.JS
	}{
		Username:       user.Username,
		RapidoDataJSON: template.JS(raw), //nolint:gosec // raw is our own json.Marshal output, not user input
	})
}
