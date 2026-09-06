// Package config loads panel configuration from the process environment.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Role string

const (
	RoleAPI     Role = "api"
	RoleBackend Role = "backend"
)

type Config struct {
	Role Role

	HTTPHost string
	HTTPPort int

	DatabaseURL string
	RedisAddr   string
	RedisPass   string
	RedisDB     int

	JWTAccessTTL time.Duration

	SudoUsername string
	SudoPassword string

	CertsDir string

	// AllowedOrigins mirrors ALLOWED_ORIGINS in the current config.py
	// (comma-separated, defaulting to "*").
	AllowedOrigins []string

	// PublicIP feeds the {SERVER_IP} placeholder in subscription remark
	// templates - a plain config value here rather than the current
	// system's auto-detect-via-external-service-at-import-time, since a
	// panel's public IP is infrastructure configuration, not something to
	// discover via a network call to a third party at every boot.
	PublicIP string

	// SubscriptionURLPrefix mirrors XRAY_SUBSCRIPTION_URL_PREFIX: prepended
	// to "/sub/<token>" when building a user's subscription_url. Empty
	// (the default) yields a relative path.
	SubscriptionURLPrefix string

	// --- Integration env defaults (overridable per-row via PUT
	// /api/settings/integrations - see internal/integrationsettings) ---
	KirbotSecret  string
	KirbotURL     string
	KirbotLicense string

	TelegramAPIToken         string
	TelegramAdminIDs         []int64
	TelegramProxyURL         string
	TelegramLoggerChannelID  int64
	TelegramLoggerTopicID    int64
	TelegramDefaultVlessFlow string

	WebhookAddresses  []string
	WebhookSecret     string
	DiscordWebhookURL string

	// LoginNotifyWhitelist mirrors LOGIN_NOTIFY_WHITE_LIST: client IPs that
	// never get a "login succeeded" report (a failed login is always
	// reported regardless). Static/env-only, like the Notify* flags below -
	// not part of integration_settings in the current Python system either.
	LoginNotifyWhitelist []string

	// --- NOTIFY_* gates (static; config.py never makes these DB-overridable) ---
	NotifyStatusChange      bool
	NotifyUserCreated       bool
	NotifyUserUpdated       bool
	NotifyUserDeleted       bool
	NotifyUserDataUsedReset bool
	NotifyUserSubRevoked    bool
	NotifyLogin             bool
}

func Load() (*Config, error) {
	cfg := &Config{
		Role:                  Role(getEnv("ROLE", string(RoleAPI))),
		HTTPHost:              getEnv("UVICORN_HOST", "0.0.0.0"),
		DatabaseURL:           getEnv("DATABASE_URL", ""),
		RedisAddr:             getEnv("REDIS_ADDR", "127.0.0.1:6379"),
		RedisPass:             getEnv("REDIS_PASSWORD", ""),
		SudoUsername:          getEnv("SUDO_USERNAME", ""),
		SudoPassword:          getEnv("SUDO_PASSWORD", ""),
		CertsDir:              getEnv("CERTS_DIR", "./certs"),
		JWTAccessTTL:          24 * time.Hour,
		AllowedOrigins:        strings.Split(getEnv("ALLOWED_ORIGINS", "*"), ","),
		PublicIP:              getEnv("PUBLIC_IP", ""),
		SubscriptionURLPrefix: getEnv("XRAY_SUBSCRIPTION_URL_PREFIX", ""),

		KirbotSecret:  getEnv("KIRBOT_SECRET", ""),
		KirbotURL:     getEnv("KIRBOT_URL", "http://127.0.0.1:8080"),
		KirbotLicense: getEnv("KIRBOT_LICENSE", ""),

		TelegramAPIToken:         getEnv("TELEGRAM_API_TOKEN", ""),
		TelegramAdminIDs:         parseInt64CSV(getEnv("TELEGRAM_ADMIN_ID", "")),
		TelegramProxyURL:         getEnv("TELEGRAM_PROXY_URL", ""),
		TelegramDefaultVlessFlow: getEnv("TELEGRAM_DEFAULT_VLESS_FLOW", ""),

		WebhookAddresses:  splitNonEmpty(getEnv("WEBHOOK_ADDRESS", "")),
		WebhookSecret:     getEnv("WEBHOOK_SECRET", ""),
		DiscordWebhookURL: getEnv("DISCORD_WEBHOOK_URL", ""),

		LoginNotifyWhitelist: splitNonEmpty(getEnv("LOGIN_NOTIFY_WHITE_LIST", "")),
	}

	cfg.NotifyStatusChange = getBool("NOTIFY_STATUS_CHANGE", true)
	cfg.NotifyUserCreated = getBool("NOTIFY_USER_CREATED", true)
	cfg.NotifyUserUpdated = getBool("NOTIFY_USER_UPDATED", true)
	cfg.NotifyUserDeleted = getBool("NOTIFY_USER_DELETED", true)
	cfg.NotifyUserDataUsedReset = getBool("NOTIFY_USER_DATA_USED_RESET", true)
	cfg.NotifyUserSubRevoked = getBool("NOTIFY_USER_SUB_REVOKED", true)
	cfg.NotifyLogin = getBool("NOTIFY_LOGIN", true)

	if n, err := strconv.ParseInt(getEnv("TELEGRAM_LOGGER_CHANNEL_ID", "0"), 10, 64); err == nil {
		cfg.TelegramLoggerChannelID = n
	}
	if n, err := strconv.ParseInt(getEnv("TELEGRAM_LOGGER_TOPIC_ID", "0"), 10, 64); err == nil {
		cfg.TelegramLoggerTopicID = n
	}

	if cfg.Role != RoleAPI && cfg.Role != RoleBackend {
		return nil, fmt.Errorf("config: invalid ROLE %q, expected %q or %q", cfg.Role, RoleAPI, RoleBackend)
	}

	port, err := strconv.Atoi(getEnv("UVICORN_PORT", "8000"))
	if err != nil {
		return nil, fmt.Errorf("config: invalid UVICORN_PORT: %w", err)
	}
	cfg.HTTPPort = port

	redisDB, err := strconv.Atoi(getEnv("REDIS_DB", "0"))
	if err != nil {
		return nil, fmt.Errorf("config: invalid REDIS_DB: %w", err)
	}
	cfg.RedisDB = redisDB

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("config: DATABASE_URL is required")
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func getBool(key string, fallback bool) bool {
	v, err := strconv.ParseBool(getEnv(key, strconv.FormatBool(fallback)))
	if err != nil {
		return fallback
	}
	return v
}

// splitNonEmpty splits a comma-separated env value, trimming whitespace and
// dropping empty tokens - matches LOGIN_NOTIFY_WHITE_LIST/WEBHOOK_ADDRESS's
// exact `[s.strip() for s in v.split(",") if s.strip()]` parsing in config.py.
func splitNonEmpty(raw string) []string {
	if raw == "" {
		return nil
	}
	var out []string
	for _, s := range strings.Split(raw, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// parseInt64CSV mirrors TELEGRAM_ADMIN_ID's parsing: comma-separated,
// non-numeric tokens silently dropped (config.py's
// `filter(str.isdigit, ...)`), not a hard error.
func parseInt64CSV(raw string) []int64 {
	if raw == "" {
		return nil
	}
	var out []int64
	for _, s := range strings.Split(raw, ",") {
		s = strings.TrimSpace(s)
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			out = append(out, n)
		}
	}
	return out
}
