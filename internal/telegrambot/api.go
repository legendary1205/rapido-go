package telegrambot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"time"
)

// apiResult is one response from the panel's own router.
type apiResult struct {
	Status int
	Body   []byte
}

func (r apiResult) ok() bool { return r.Status >= 200 && r.Status < 300 }

func (r apiResult) decode(v any) error { return json.Unmarshal(r.Body, v) }

// detail is the panel's own explanation of a refusal, shortened for a chat.
func (r apiResult) detail() string {
	var d struct {
		Detail json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(r.Body, &d) != nil || len(d.Detail) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(d.Detail, &s) == nil {
		return truncateRunes(s, 300)
	}
	return truncateRunes(string(d.Detail), 300)
}

var errNoAPI = errors.New("telegram console: panel router not attached")

// call runs one request against the panel's router with a token minted for who.
// The token is created per call and never leaves the process.
func (c *Console) call(ctx context.Context, who principal, method, path string, body any) (apiResult, error) {
	h := c.apiHandler()
	if h == nil {
		return apiResult{}, errNoAPI
	}
	if _, has := ctx.Deadline(); !has {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, apiTimeout)
		defer cancel()
	}
	token, err := c.d.Issuer.Issue(who.Username, who.IsSudo)
	if err != nil {
		return apiResult{}, err
	}
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return apiResult{}, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://panel.internal"+path, rd)
	if err != nil {
		return apiResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "rapido-telegram-console")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.RemoteAddr = "127.0.0.1:0"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return apiResult{Status: rec.Code, Body: rec.Body.Bytes()}, nil
}

// The DTOs below decode only what the console shows. Proxy settings (uuids,
// passwords) and per-protocol share links are deliberately absent: the
// subscription link is the one thing an admin is meant to hand out.

type adminRef struct {
	Username string `json:"username"`
}

type nextPlan struct {
	DataLimit           int64 `json:"data_limit"`
	Expire              int64 `json:"expire"`
	AddRemainingTraffic bool  `json:"add_remaining_traffic"`
	FireOnEither        bool  `json:"fire_on_either"`
}

type userDTO struct {
	Username             string     `json:"username"`
	Status               string     `json:"status"`
	UsedTraffic          int64      `json:"used_traffic"`
	DataLimit            *int64     `json:"data_limit"`
	Expire               *int64     `json:"expire"`
	Note                 *string    `json:"note"`
	OnHoldExpireDuration *int64     `json:"on_hold_expire_duration"`
	OnlineAt             *time.Time `json:"online_at"`
	Admin                *adminRef  `json:"admin"`
	SyncedFromPanelName  *string    `json:"synced_from_panel_name"`
	NextPlan             *nextPlan  `json:"next_plan"`
	SubscriptionURL      string     `json:"subscription_url"`
	SubscriptionURLs     []string   `json:"subscription_urls"`
}

// links is every address the API lists, falling back to the single legacy one.
func (u userDTO) links() []string {
	if len(u.SubscriptionURLs) > 0 {
		return u.SubscriptionURLs
	}
	if u.SubscriptionURL != "" {
		return []string{u.SubscriptionURL}
	}
	return nil
}

type usersPage struct {
	Users []userDTO `json:"users"`
	Total int       `json:"total"`
}

type systemStats struct {
	MemTotal               int64   `json:"mem_total"`
	MemUsed                int64   `json:"mem_used"`
	CPUUsage               float64 `json:"cpu_usage"`
	TotalUser              int64   `json:"total_user"`
	OnlineUsers            int64   `json:"online_users"`
	UsersActive            int64   `json:"users_active"`
	UsersOnHold            int64   `json:"users_on_hold"`
	UsersDisabled          int64   `json:"users_disabled"`
	UsersExpired           int64   `json:"users_expired"`
	UsersLimited           int64   `json:"users_limited"`
	IncomingBandwidth      int64   `json:"incoming_bandwidth"`
	OutgoingBandwidth      int64   `json:"outgoing_bandwidth"`
	IncomingBandwidthSpeed int64   `json:"incoming_bandwidth_speed"`
	OutgoingBandwidthSpeed int64   `json:"outgoing_bandwidth_speed"`
}

type nodeDTO struct {
	ID          int32   `json:"id"`
	Name        string  `json:"name"`
	Address     string  `json:"address"`
	Status      string  `json:"status"`
	Message     *string `json:"message"`
	XrayVersion *string `json:"xray_version"`
}

type tunnelDTO struct {
	Name                string   `json:"name"`
	Up                  bool     `json:"up"`
	Present             bool     `json:"present"`
	HandshakeAgeSeconds *float64 `json:"handshake_age_seconds"`
	Error               string   `json:"error"`
}

type hostDTO struct {
	NodeID      *int32      `json:"node_id"`
	Name        string      `json:"name"`
	Reachable   bool        `json:"reachable"`
	Stale       bool        `json:"stale"`
	CPUPercent  *float64    `json:"cpu_percent"`
	MemPercent  *float64    `json:"mem_percent"`
	DiskPercent *float64    `json:"disk_percent"`
	RxRate      *int64      `json:"rx_rate"`
	TxRate      *int64      `json:"tx_rate"`
	Uptime      *float64    `json:"uptime"`
	HasMetrics  bool        `json:"has_metrics"`
	Tunnels     []tunnelDTO `json:"tunnels"`
}

type monitoringDTO struct {
	Hosts []hostDTO `json:"hosts"`
}

type templateDTO struct {
	ID             int32               `json:"id"`
	Name           string              `json:"name"`
	DataLimit      int64               `json:"data_limit"`
	ExpireDuration int64               `json:"expire_duration"`
	UsernamePrefix *string             `json:"username_prefix"`
	UsernameSuffix *string             `json:"username_suffix"`
	Inbounds       map[string][]string `json:"inbounds"`
}

type backupInfo struct {
	Filename  string `json:"filename"`
	SizeBytes int64  `json:"size_bytes"`
}

func userPath(username string) string { return "/api/user/" + url.PathEscape(username) }

func (c *Console) getUser(ctx context.Context, who principal, username string) (userDTO, apiResult, error) {
	res, err := c.call(ctx, who, http.MethodGet, userPath(username), nil)
	if err != nil || !res.ok() {
		return userDTO{}, res, err
	}
	var u userDTO
	return u, res, res.decode(&u)
}

func (c *Console) listUsers(ctx context.Context, who principal, status, search string, page int) (usersPage, apiResult, error) {
	q := url.Values{}
	if status != "" {
		q.Set("status", status)
	}
	if search != "" {
		q.Set("search", search)
	}
	q.Set("sort", "-created_at")
	q.Set("limit", strconv.Itoa(c.pageSize))
	q.Set("offset", strconv.Itoa(page*c.pageSize))
	res, err := c.call(ctx, who, http.MethodGet, "/api/users?"+q.Encode(), nil)
	if err != nil || !res.ok() {
		return usersPage{}, res, err
	}
	var p usersPage
	return p, res, res.decode(&p)
}

// modifyUser sends a partial update. The panel treats an absent next_plan as
// "remove it", so a plan the user already has is passed back untouched.
func (c *Console) modifyUser(ctx context.Context, who principal, cur userDTO, fields map[string]any) (userDTO, apiResult, error) {
	if cur.NextPlan != nil {
		fields["next_plan"] = cur.NextPlan
	}
	res, err := c.call(ctx, who, http.MethodPut, userPath(cur.Username), fields)
	if err != nil || !res.ok() {
		return userDTO{}, res, err
	}
	var u userDTO
	return u, res, res.decode(&u)
}
