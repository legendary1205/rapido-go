// Package report is the central dispatcher for admin/user lifecycle
// notifications, mirroring app/utils/report.py: which events fire, which
// NOTIFY_* flag gates each one, and who the recipients are. The wire
// format and low-level fan-out mechanics live in internal/telegram and
// internal/discord - this package owns policy, not transport.
package report

import (
	"context"
	"log/slog"

	"github.com/legendary1205/rapido-go/internal/discord"
	"github.com/legendary1205/rapido-go/internal/integrationsettings"
	"github.com/legendary1205/rapido-go/internal/telegram"
)

// NotifyFlags mirrors the 7 static NOTIFY_* env booleans - config.py never
// makes these DB-overridable, so they're plain config, not part of
// integrationsettings.Values.
type NotifyFlags struct {
	StatusChange      bool
	UserCreated       bool
	UserUpdated       bool
	UserDeleted       bool
	UserDataUsedReset bool // gates both UserDataUsageReset and UserDataResetByNext, matching Python's single NOTIFY_USER_DATA_USED_RESET
	UserSubRevoked    bool
	Login             bool
}

// SettingsFunc resolves the current dynamic settings, called fresh on every
// dispatch - never cached inside this package, since internal/httpapi's
// Store.CachedGetIntegrationSettings already caches the underlying Postgres
// read. This is what makes a sudo admin's PUT /api/settings/integrations
// take effect immediately, matching every integration in Python except its
// Telegram bot object (a restart-only limitation that doesn't exist here,
// since this phase only sends messages and holds no persistent connection).
type SettingsFunc func(ctx context.Context) (integrationsettings.Values, error)

// AdminRef and UserSummary are deliberately narrow - no db/generated
// dependency - matching internal/subscription/vars.go's UserInfo
// convention, so this package stays a pure policy layer.
type AdminRef struct {
	Username       string
	TelegramID     *int64
	DiscordWebhook *string
}

type UserSummary struct {
	Username               string
	DataLimit              *int64
	Expire                 *int64
	Proxies                []string // protocol names present, e.g. ["vless", "vmess"]
	HasNextPlan            bool
	DataLimitResetStrategy string
}

type Dispatcher struct {
	flags      NotifyFlags
	settingsFn SettingsFunc
	telegram   *telegram.Sender
	discord    *discord.Sender
	logger     *slog.Logger
}

func New(flags NotifyFlags, settingsFn SettingsFunc, tg *telegram.Sender, dc *discord.Sender, logger *slog.Logger) *Dispatcher {
	return &Dispatcher{flags: flags, settingsFn: settingsFn, telegram: tg, discord: dc, logger: logger}
}

// resolve fetches the current settings and builds the telegram.Config -
// shared setup for every dispatch method below. Returns ok=false (nothing
// to send) on a settings-read failure, logging the error - the one path
// that can't be per-recipient like telegram.Sender/discord.Sender's own
// failure isolation, since without settings there's no recipient to try.
func (d *Dispatcher) resolve(ctx context.Context) (integrationsettings.Values, telegram.Config, bool) {
	vals, err := d.settingsFn(ctx)
	if err != nil {
		d.logger.Warn("report: could not resolve integration settings, dropping notification", "error", err)
		return integrationsettings.Values{}, telegram.Config{}, false
	}
	cfg := telegram.Config{
		APIToken:        vals.TelegramAPIToken,
		AdminIDs:        vals.TelegramAdminIDs,
		LoggerChannelID: vals.TelegramLoggerChannelID,
		LoggerTopicID:   vals.TelegramLoggerTopicID,
		ProxyURL:        vals.TelegramProxyURL,
	}
	return vals, cfg, true
}

func dmChatID(userAdmin *AdminRef) *int64 {
	if userAdmin != nil {
		return userAdmin.TelegramID
	}
	return nil
}

func adminWebhook(userAdmin *AdminRef) *string {
	if userAdmin != nil {
		return userAdmin.DiscordWebhook
	}
	return nil
}

func belongsTo(userAdmin *AdminRef) *string {
	if userAdmin == nil {
		return nil
	}
	return &userAdmin.Username
}

func (d *Dispatcher) StatusChange(ctx context.Context, username, status string, userAdmin *AdminRef) {
	if !d.flags.StatusChange {
		return
	}
	vals, tgCfg, ok := d.resolve(ctx)
	if !ok {
		return
	}
	text := telegram.StatusChangeMessage(username, status, belongsTo(userAdmin))
	d.telegram.Report(ctx, tgCfg, text, dmChatID(userAdmin), d.logger)

	payload := discord.StatusChangePayload(username, status, belongsTo(userAdmin))
	d.discord.Send(ctx, payload, vals.DiscordWebhookURL, adminWebhook(userAdmin), d.logger)
}

func (d *Dispatcher) UserCreated(ctx context.Context, user UserSummary, byUsername string, userAdmin *AdminRef) {
	if !d.flags.UserCreated {
		return
	}
	vals, tgCfg, ok := d.resolve(ctx)
	if !ok {
		return
	}
	text := telegram.NewUserMessage(user.Username, byUsername, user.DataLimit, user.Expire, user.Proxies, user.HasNextPlan, user.DataLimitResetStrategy, belongsTo(userAdmin))
	d.telegram.Report(ctx, tgCfg, text, dmChatID(userAdmin), d.logger)

	payload := discord.NewUserPayload(user.Username, byUsername, user.DataLimit, user.Expire, user.Proxies, user.HasNextPlan, user.DataLimitResetStrategy, belongsTo(userAdmin))
	d.discord.Send(ctx, payload, vals.DiscordWebhookURL, adminWebhook(userAdmin), d.logger)
}

func (d *Dispatcher) UserUpdated(ctx context.Context, user UserSummary, byUsername string, userAdmin *AdminRef) {
	if !d.flags.UserUpdated {
		return
	}
	vals, tgCfg, ok := d.resolve(ctx)
	if !ok {
		return
	}
	text := telegram.ModifiedMessage(user.Username, byUsername, user.DataLimit, user.Expire, user.Proxies, user.HasNextPlan, user.DataLimitResetStrategy, belongsTo(userAdmin))
	d.telegram.Report(ctx, tgCfg, text, dmChatID(userAdmin), d.logger)

	payload := discord.ModifiedPayload(user.Username, byUsername, user.DataLimit, user.Expire, user.Proxies, user.HasNextPlan, user.DataLimitResetStrategy, belongsTo(userAdmin))
	d.discord.Send(ctx, payload, vals.DiscordWebhookURL, adminWebhook(userAdmin), d.logger)
}

func (d *Dispatcher) UserDeleted(ctx context.Context, username, byUsername string, userAdmin *AdminRef) {
	if !d.flags.UserDeleted {
		return
	}
	vals, tgCfg, ok := d.resolve(ctx)
	if !ok {
		return
	}
	text := telegram.DeletedMessage(username, byUsername, belongsTo(userAdmin))
	d.telegram.Report(ctx, tgCfg, text, dmChatID(userAdmin), d.logger)

	payload := discord.DeletedPayload(username, byUsername, belongsTo(userAdmin))
	d.discord.Send(ctx, payload, vals.DiscordWebhookURL, adminWebhook(userAdmin), d.logger)
}

func (d *Dispatcher) UserDataUsageReset(ctx context.Context, username, byUsername string, userAdmin *AdminRef) {
	if !d.flags.UserDataUsedReset {
		return
	}
	vals, tgCfg, ok := d.resolve(ctx)
	if !ok {
		return
	}
	text := telegram.UsageResetMessage(username, byUsername, belongsTo(userAdmin))
	d.telegram.Report(ctx, tgCfg, text, dmChatID(userAdmin), d.logger)

	payload := discord.UsageResetPayload(username, byUsername, belongsTo(userAdmin))
	d.discord.Send(ctx, payload, vals.DiscordWebhookURL, adminWebhook(userAdmin), d.logger)
}

func (d *Dispatcher) UserDataResetByNext(ctx context.Context, user UserSummary, userAdmin *AdminRef) {
	if !d.flags.UserDataUsedReset {
		return
	}
	vals, tgCfg, ok := d.resolve(ctx)
	if !ok {
		return
	}
	text := telegram.DataResetByNextMessage(user.Username, user.DataLimit, user.Expire)
	d.telegram.Report(ctx, tgCfg, text, dmChatID(userAdmin), d.logger)

	payload := discord.DataResetByNextPayload(user.Username, user.DataLimit, user.Expire)
	d.discord.Send(ctx, payload, vals.DiscordWebhookURL, adminWebhook(userAdmin), d.logger)
}

func (d *Dispatcher) UserSubscriptionRevoked(ctx context.Context, username, byUsername string, userAdmin *AdminRef) {
	if !d.flags.UserSubRevoked {
		return
	}
	vals, tgCfg, ok := d.resolve(ctx)
	if !ok {
		return
	}
	text := telegram.SubscriptionRevokedMessage(username, byUsername, belongsTo(userAdmin))
	d.telegram.Report(ctx, tgCfg, text, dmChatID(userAdmin), d.logger)

	payload := discord.SubscriptionRevokedPayload(username, byUsername, belongsTo(userAdmin))
	d.discord.Send(ctx, payload, vals.DiscordWebhookURL, adminWebhook(userAdmin), d.logger)
}

// Login has no owning-admin concept - matches Python's report_login, which
// always sends with admin_webhook=None and no DM.
func (d *Dispatcher) Login(ctx context.Context, username, clientIP, status string) {
	if !d.flags.Login {
		return
	}
	vals, tgCfg, ok := d.resolve(ctx)
	if !ok {
		return
	}
	text := telegram.LoginMessage(username, clientIP, status)
	d.telegram.Report(ctx, tgCfg, text, nil, d.logger)

	payload := discord.LoginPayload(username, clientIP, status)
	d.discord.Send(ctx, payload, vals.DiscordWebhookURL, nil, d.logger)
}
