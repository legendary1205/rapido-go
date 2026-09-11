package httpapi

import (
	"fmt"
	"net/http"
	"regexp"

	"github.com/gin-gonic/gin"

	"github.com/legendary1205/rapido-go/internal/db/generated"
)

var (
	// Matches app/models/proxy.py's FRAGMENT_PATTERN / NOISE_PATTERN.
	fragmentPattern = regexp.MustCompile(`^((\d{1,4}-\d{1,4})|(\d{1,4})),((\d{1,3}-\d{1,3})|(\d{1,3})),(tlshello|\d|\d\-\d)$`)
	noisePattern    = regexp.MustCompile(`^(rand:(\d{1,4}-\d{1,4}|\d{1,4})|str:.+|hex:.+|base64:.+)(,(\d{1,4}-\d{1,4}|\d{1,4}))?(&(rand:(\d{1,4}-\d{1,4}|\d{1,4})|str:.+|hex:.+|base64:.+)(,(\d{1,4}-\d{1,4}|\d{1,4}))?)*$`)

	validSecurity    = map[string]bool{"inbound_default": true, "none": true, "tls": true}
	validAlpn        = map[string]bool{"none": true, "h3": true, "h2": true, "http/1.1": true, "h3,h2,http/1.1": true, "h3,h2": true, "h2,http/1.1": true}
	validFingerprint = map[string]bool{"none": true, "chrome": true, "firefox": true, "safari": true, "ios": true, "android": true, "edge": true, "360": true, "qq": true, "random": true, "randomized": true}
)

type hostDTO struct {
	ID              int32   `json:"id"`
	Remark          string  `json:"remark"`
	Address         string  `json:"address"`
	Port            *int32  `json:"port"`
	Path            *string `json:"path"`
	SNI             *string `json:"sni"`
	Host            *string `json:"host"`
	Security        string  `json:"security"`
	ALPN            string  `json:"alpn"`
	Fingerprint     string  `json:"fingerprint"`
	AllowInsecure   *bool   `json:"allowinsecure"`
	IsDisabled      *bool   `json:"is_disabled"`
	MuxEnable       bool    `json:"mux_enable"`
	FragmentSetting *string `json:"fragment_setting"`
	NoiseSetting    *string `json:"noise_setting"`
	RandomUserAgent bool    `json:"random_user_agent"`
	UseSNIAsHost    bool    `json:"use_sni_as_host"`
	// Priority is a GLOBAL rank (see migration 00008), not scoped to this
	// host's own inbound_tag - it's what lets an admin interleave configs
	// from different inbound tags/nodes into one chosen sequence. Set
	// explicitly by the client (the frontend computes it from a flattened,
	// cross-tag view - see hostsReducers.ts's flattenSortedHosts), not
	// derived from this array's position within its own tag's slice.
	Priority int32 `json:"priority"`
}

func toHostDTO(h generated.Host) hostDTO {
	return hostDTO{
		ID: h.ID, Remark: h.Remark, Address: h.Address,
		Port: pgInt4ToPtr(h.Port), Path: textToPtr(h.Path), SNI: textToPtr(h.Sni), Host: textToPtr(h.Host),
		Security: h.Security, ALPN: h.Alpn, Fingerprint: h.Fingerprint,
		AllowInsecure: pgBoolToPtr(h.Allowinsecure), IsDisabled: pgBoolToPtr(h.IsDisabled), MuxEnable: h.MuxEnable,
		FragmentSetting: textToPtr(h.FragmentSetting), NoiseSetting: textToPtr(h.NoiseSetting),
		RandomUserAgent: h.RandomUserAgent, UseSNIAsHost: h.UseSniAsHost,
		Priority: h.Priority,
	}
}

// handleGetHosts implements GET /api/hosts (sudo only): every host grouped
// by inbound tag.
func (h *Handler) handleGetHosts(c *gin.Context) {
	rows, err := h.store.Queries.ListHosts(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not list hosts"})
		return
	}
	out := map[string][]hostDTO{}
	for _, r := range rows {
		out[r.InboundTag] = append(out[r.InboundTag], toHostDTO(r))
	}
	c.JSON(http.StatusOK, out)
}

// handlePutHosts implements PUT /api/hosts (sudo only): full replace per
// tag, matching crud.update_hosts's plain-replace (not diff/merge)
// semantics.
func (h *Handler) handlePutHosts(c *gin.Context) {
	var body map[string][]hostDTO
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": err.Error()})
		return
	}

	for tag, hosts := range body {
		if _, err := h.store.CachedGetInboundByTag(c.Request.Context(), tag); err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "Inbound " + tag + " doesn't exist"})
			return
		}
		for i, host := range hosts {
			// Matches the Pydantic ProxyHost model's field defaults
			// (security=inbound_default, alpn=none, fingerprint=none) - an
			// omitted value here isn't "invalid", it's "use the default",
			// same as a client never sending the field at all.
			if host.Security == "" {
				host.Security = "inbound_default"
			}
			if host.ALPN == "" {
				host.ALPN = "none"
			}
			if host.Fingerprint == "" {
				host.Fingerprint = "none"
			}
			hosts[i] = host
			if err := validateHost(host); err != nil {
				c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": err.Error()})
				return
			}
		}
		body[tag] = hosts
	}

	ctx := c.Request.Context()
	out := map[string][]hostDTO{}
	for tag, hosts := range body {
		if err := h.store.Queries.DeleteHostsByInboundTag(ctx, tag); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not replace hosts for " + tag})
			return
		}
		for _, host := range hosts {
			created, err := h.store.Queries.CreateHost(ctx, generated.CreateHostParams{
				Remark: host.Remark, Address: host.Address, Port: pgInt4FromPtr(host.Port),
				Path: textFromPtr(host.Path), Sni: textFromPtr(host.SNI), Host: textFromPtr(host.Host),
				Security: host.Security, Alpn: host.ALPN, Fingerprint: host.Fingerprint,
				InboundTag: tag, Allowinsecure: pgBoolFromPtr(host.AllowInsecure), IsDisabled: pgBoolFromPtr(host.IsDisabled),
				MuxEnable: host.MuxEnable, FragmentSetting: textFromPtr(host.FragmentSetting), NoiseSetting: textFromPtr(host.NoiseSetting),
				RandomUserAgent: host.RandomUserAgent, UseSniAsHost: host.UseSNIAsHost,
				Priority: host.Priority,
			})
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not create host for " + tag})
				return
			}
			out[tag] = append(out[tag], toHostDTO(created))
		}
	}
	if err := h.store.InvalidateNodeConfigPayload(ctx); err != nil {
		h.logger.Warn("invalidate node config cache", "error", err)
	}
	c.JSON(http.StatusOK, out)
}

// validateHost mirrors app/models/proxy.py's ProxyHost validators: balanced
// {VAR} placeholders in remark/address, and the fragment/noise regexes.
func validateHost(host hostDTO) error {
	if !balancedBraces(host.Remark) {
		return fmt.Errorf("remark has unbalanced curly braces")
	}
	if !balancedBraces(host.Address) {
		return fmt.Errorf("address has unbalanced curly braces")
	}
	if host.Security != "" && !validSecurity[host.Security] {
		return fmt.Errorf("invalid security value: %s", host.Security)
	}
	if host.ALPN != "" && !validAlpn[host.ALPN] {
		return fmt.Errorf("invalid alpn value: %s", host.ALPN)
	}
	if host.Fingerprint != "" && !validFingerprint[host.Fingerprint] {
		return fmt.Errorf("invalid fingerprint value: %s", host.Fingerprint)
	}
	if host.FragmentSetting != nil && *host.FragmentSetting != "" && !fragmentPattern.MatchString(*host.FragmentSetting) {
		return fmt.Errorf("invalid fragment_setting format")
	}
	if host.NoiseSetting != nil {
		if len(*host.NoiseSetting) > 2000 {
			return fmt.Errorf("noise_setting can be a maximum of 2000 characters")
		}
		if *host.NoiseSetting != "" && !noisePattern.MatchString(*host.NoiseSetting) {
			return fmt.Errorf("invalid noise_setting format")
		}
	}
	return nil
}

// balancedBraces mirrors Python's str.format_map safety check: every "{"
// has a matching "}" and vice versa, so {VAR}-style placeholders can be
// substituted later without a format error.
func balancedBraces(s string) bool {
	depth := 0
	for _, r := range s {
		switch r {
		case '{':
			depth++
		case '}':
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return depth == 0
}
