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

	// SubscriptionURLPrefixes mirrors XRAY_SUBSCRIPTION_URL_PREFIXES: every
	// address a user's subscription can be reached on, in the order the
	// dashboard should list them (comma-separated). It only feeds the
	// dashboard's `subscription_urls` field - `subscription_url`, which
	// reseller bots read, keeps using SubscriptionURLPrefix alone. Empty
	// means the list is just that one address.
	SubscriptionURLPrefixes []string

	// SubSupportURL / SubProfileTitle / SubUpdateInterval mirror the real
	// panel's SUB_SUPPORT_URL / SUB_PROFILE_TITLE / SUB_UPDATE_INTERVAL,
	// sent as headers on every subscription fetch. Real VPN clients render
	// the support URL as an in-app button and the title as the profile's
	// name, so these are customer-visible, not internal settings.
	SubSupportURL     string
	SubProfileTitle   string
	SubUpdateInterval string

	// ClashTemplateFile / V2raySubscriptionTemplateFile mirror the current
	// Python system's own CLASH_SUBSCRIPTION_TEMPLATE / V2RAY_SUBSCRIPTION_TEMPLATE:
	// a real YAML/JSON file supplying everything beyond bare connectivity
	// (dns/tun/sniffer settings, rule-providers, a branded proxy-group
	// name, local inbounds/routing) for those two subscription formats.
	// Empty (the default) falls back to a minimal built-in shape - see
	// subscription.ClashConfig/V2rayJSONConfig's own doc comments.
	ClashTemplateFile             string
	V2raySubscriptionTemplateFile string

	// UseCustomJSON* mirror config.py's USE_CUSTOM_JSON_* family exactly,
	// including their shared default of false: each of v2rayN/v2rayNG/
	// Streisand/Happ/NPVTunnel(ktor-client) only moves off the universal
	// v2ray share-link format to v2ray-json when its own flag (or the
	// blanket Default) is on. Real deployments differ on this per client -
	// getting one wrong is not cosmetic (see internal/httpapi/subscription.go's
	// detectSubscriptionFormat doc comment for the real incident this
	// caused: v2rayN got a format its users should never have received).
	UseCustomJSONDefault      bool
	UseCustomJSONForV2RayN    bool
	UseCustomJSONForV2RayNG   bool
	UseCustomJSONForStreisand bool
	UseCustomJSONForHapp      bool
	UseCustomJSONForNPVTunnel bool

	// DashboardDir is where the built dashboard (web/dist, a Vite build
	// output) lives on disk - mirrors the current Python system serving its
	// own dashboard build as static files from the same process. Relative
	// to the binary's working directory by default, same convention as
	// CertsDir.
	DashboardDir string

	// BackupDir is where pg_dump output lands (internal/httpapi/backup.go) -
	// same relative-to-working-directory convention as CertsDir/DashboardDir.
	BackupDir string
	// BackupKeep mirrors DB_BACKUP_KEEP in the current Python system: how
	// many of the most recent backups survive automatic pruning after each
	// new one is created.
	BackupKeep int

	// --- Integration env defaults (overridable per-row via PUT
	// /api/settings/integrations - see internal/integrationsettings) ---
	ResellerApiSecret  string
	ResellerApiUrl     string
	ResellerApiLicense string

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
		Role:                          Role(getEnv("ROLE", string(RoleAPI))),
		HTTPHost:                      getEnv("UVICORN_HOST", "0.0.0.0"),
		DatabaseURL:                   getEnv("DATABASE_URL", ""),
		RedisAddr:                     getEnv("REDIS_ADDR", "127.0.0.1:6379"),
		RedisPass:                     getEnv("REDIS_PASSWORD", ""),
		SudoUsername:                  getEnv("SUDO_USERNAME", ""),
		SudoPassword:                  getEnv("SUDO_PASSWORD", ""),
		CertsDir:                      getEnv("CERTS_DIR", "./certs"),
		JWTAccessTTL:                  24 * time.Hour,
		AllowedOrigins:                strings.Split(getEnv("ALLOWED_ORIGINS", "*"), ","),
		PublicIP:                      getEnv("PUBLIC_IP", ""),
		SubscriptionURLPrefix:         getEnv("XRAY_SUBSCRIPTION_URL_PREFIX", ""),
		SubscriptionURLPrefixes:       splitNonEmpty(getEnv("XRAY_SUBSCRIPTION_URL_PREFIXES", "")),
		SubSupportURL:                 getEnv("SUB_SUPPORT_URL", "https://t.me/"),
		SubProfileTitle:               getEnv("SUB_PROFILE_TITLE", "Subscription"),
		SubUpdateInterval:             getEnv("SUB_UPDATE_INTERVAL", "12"),
		ClashTemplateFile:             getEnv("CLASH_SUBSCRIPTION_TEMPLATE", ""),
		V2raySubscriptionTemplateFile: getEnv("V2RAY_SUBSCRIPTION_TEMPLATE", ""),
		DashboardDir:                  getEnv("DASHBOARD_DIR", "./web/dist"),
		BackupDir:                     getEnv("BACKUP_DIR", "./db_backups"),

		ResellerApiSecret:  getEnv("RESELLER_API_SECRET", ""),
		ResellerApiUrl:     getEnv("RESELLER_API_URL", "http://127.0.0.1:8080"),
		ResellerApiLicense: getEnv("RESELLER_API_LICENSE", ""),

		TelegramAPIToken:         getEnv("TELEGRAM_API_TOKEN", ""),
		TelegramAdminIDs:         parseInt64CSV(getEnv("TELEGRAM_ADMIN_ID", "")),
		TelegramProxyURL:         getEnv("TELEGRAM_PROXY_URL", ""),
		TelegramDefaultVlessFlow: getEnv("TELEGRAM_DEFAULT_VLESS_FLOW", ""),

		WebhookAddresses:  splitNonEmpty(getEnv("WEBHOOK_ADDRESS", "")),
		WebhookSecret:     getEnv("WEBHOOK_SECRET", ""),
		DiscordWebhookURL: getEnv("DISCORD_WEBHOOK_URL", ""),

		LoginNotifyWhitelist: splitNonEmpty(getEnv("LOGIN_NOTIFY_WHITE_LIST", "")),
	}

	cfg.UseCustomJSONDefault = getBool("USE_CUSTOM_JSON_DEFAULT", false)
	cfg.UseCustomJSONForV2RayN = getBool("USE_CUSTOM_JSON_FOR_V2RAYN", false)
	cfg.UseCustomJSONForV2RayNG = getBool("USE_CUSTOM_JSON_FOR_V2RAYNG", false)
	cfg.UseCustomJSONForStreisand = getBool("USE_CUSTOM_JSON_FOR_STREISAND", false)
	cfg.UseCustomJSONForHapp = getBool("USE_CUSTOM_JSON_FOR_HAPP", false)
	cfg.UseCustomJSONForNPVTunnel = getBool("USE_CUSTOM_JSON_FOR_NPVTUNNEL", false)

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

	backupKeep, err := strconv.Atoi(getEnv("DB_BACKUP_KEEP", "5"))
	if err != nil {
		return nil, fmt.Errorf("config: invalid DB_BACKUP_KEEP: %w", err)
	}
	cfg.BackupKeep = backupKeep

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
