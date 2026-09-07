package httpapi

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/legendary1205/rapido-go/internal/db/generated"
	"github.com/legendary1205/rapido-go/internal/legacyimport"
	"github.com/legendary1205/rapido-go/internal/proxysettings"
)

// openMaybeGzipped opens path and, if its first two bytes are the gzip
// magic number, wraps it in a gzip.Reader - a legacy mysqldump upload is
// just as often a plain .sql file as a .sql.gz one (unlike this panel's
// own backups, which are always gzipped), so the restore-upload endpoint
// has to tolerate both rather than assuming one.
func openMaybeGzipped(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	br := bufio.NewReader(f)
	head, err := br.Peek(2)
	if err == nil && len(head) == 2 && head[0] == 0x1f && head[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			f.Close()
			return nil, err
		}
		return &gzipCloser{Reader: gz, gz: gz, f: f}, nil
	}
	return &plainCloser{Reader: br, f: f}, nil
}

type gzipCloser struct {
	io.Reader
	gz *gzip.Reader
	f  *os.File
}

func (g *gzipCloser) Close() error {
	g.gz.Close()
	return g.f.Close()
}

type plainCloser struct {
	io.Reader
	f *os.File
}

func (p *plainCloser) Close() error { return p.f.Close() }

// uploadFormat is what handleRestoreUpload decided an uploaded file
// actually is, after peeking at its (possibly gunzipped) content.
type uploadFormat int

const (
	uploadFormatUnknown uploadFormat = iota
	uploadFormatNativePostgres
	uploadFormatMarzbanMySQL
	uploadFormatHiddifyJSON
)

// hiddifyExportProbe checks just the three top-level keys that identify a
// real `hiddifypanel backup` export (see internal/legacyimport/hiddify.go)
// without decoding the (potentially large) admin_users/users/proxies
// arrays themselves.
type hiddifyExportProbe struct {
	AdminUsers json.RawMessage `json:"admin_users"`
	Users      json.RawMessage `json:"users"`
	Proxies    json.RawMessage `json:"proxies"`
}

// detectUploadFormat reads just enough of the file to classify it - a
// small header peek for the "this is definitely not X" cases, and a real
// (fast - proven well under a second even for a real ~28k-user, 13MB dump)
// full parse for the mysqldump and JSON cases, since which specific legacy
// panel a dump came from can only be told by which tables/keys it actually
// contains, not by a fixed byte offset (see the Phase 8.2 plan's note on
// table-signature-based detection). jsonRaw is only populated for a JSON
// format (uploadFormatHiddifyJSON today) - nil otherwise.
func detectUploadFormat(path string) (format uploadFormat, mysqlDump *legacyimport.MySQLDump, jsonRaw []byte, err error) {
	r, err := openMaybeGzipped(path)
	if err != nil {
		return uploadFormatUnknown, nil, nil, fmt.Errorf("could not open the uploaded file: %w", err)
	}
	defer r.Close()

	br := bufio.NewReaderSize(r, 4096)
	head, _ := br.Peek(512)

	switch {
	case containsBytes(head, pgDumpHeader):
		return uploadFormatNativePostgres, nil, nil, nil
	case containsBytes(head, "-- MySQL dump"):
		dump, err := legacyimport.ParseMySQLDump(br)
		if err != nil {
			return uploadFormatUnknown, nil, nil, fmt.Errorf("this looks like a MySQL dump but couldn't be parsed: %w", err)
		}
		if _, ok := dump.Tables["admins"]; ok {
			if _, ok := dump.Tables["proxies"]; ok {
				return uploadFormatMarzbanMySQL, dump, nil, nil
			}
		}
		return uploadFormatUnknown, nil, nil, nil
	case len(head) > 0 && (head[0] == '{' || head[0] == ' ' || head[0] == '\n' || head[0] == '\t'):
		raw, err := io.ReadAll(br)
		if err != nil {
			return uploadFormatUnknown, nil, nil, fmt.Errorf("could not read the uploaded file: %w", err)
		}
		var probe hiddifyExportProbe
		if err := json.Unmarshal(raw, &probe); err == nil &&
			probe.AdminUsers != nil && probe.Users != nil && probe.Proxies != nil {
			return uploadFormatHiddifyJSON, nil, raw, nil
		}
		return uploadFormatUnknown, nil, nil, nil
	default:
		return uploadFormatUnknown, nil, nil, nil
	}
}

func containsBytes(haystack []byte, needle string) bool {
	return len(haystack) >= len(needle) && indexOfBytes(haystack, needle) >= 0
}

func indexOfBytes(haystack []byte, needle string) int {
	n := len(needle)
	for i := 0; i+n <= len(haystack); i++ {
		if string(haystack[i:i+n]) == needle {
			return i
		}
	}
	return -1
}

type legacyImportResultDTO struct {
	SafetyBackup     backupInfo `json:"safety_backup"`
	AdminsImported   int        `json:"admins_imported"`
	UsersImported    int        `json:"users_imported"`
	HostsImported    int        `json:"hosts_imported"`
	InboundsImported int        `json:"inbounds_imported"`
	Warnings         []string   `json:"warnings"`
}

// loadLegacyImport is legacy-migration's counterpart to restoreFromReader:
// same maintenance-mode + mandatory-safety-backup-first shape, but
// TRUNCATE-and-repopulate instead of a full schema drop+replay, since a
// legacy import isn't byte-compatible with this panel's own schema - see
// TruncateForLegacyImport's own doc comment for exactly which tables this
// touches (and, just as importantly, which it deliberately never does:
// tls/jwt_secrets/integration_settings/core_config, this panel's own
// identity, never crosses over from an imported source).
//
// A failure importing any single row is recorded as a warning and that
// row is skipped, not a fatal abort of the whole import - a partially
// messy legacy dump (an orphaned admin_id, a proxy with unparseable
// settings) shouldn't block importing everything else that IS clean, and
// the caller already has a fresh safety backup to fall back to if the
// result isn't good enough.
func (h *Handler) loadLegacyImport(ctx context.Context, data legacyimport.ImportedData) (legacyImportResultDTO, error) {
	if err := h.store.Cache.SetMaintenanceMode(ctx, true); err != nil {
		return legacyImportResultDTO{}, fmt.Errorf("could not enter maintenance mode: %w", err)
	}
	defer h.store.Cache.SetMaintenanceMode(context.Background(), false)

	safety, err := h.createBackupNow(ctx)
	if err != nil {
		return legacyImportResultDTO{}, fmt.Errorf("aborted before touching the database - could not take a safety backup first: %w", err)
	}

	if err := h.store.Queries.TruncateForLegacyImport(ctx); err != nil {
		return legacyImportResultDTO{}, fmt.Errorf("import failed after a safety backup (%s) was already taken - restore that backup to recover: %w", safety.Filename, err)
	}

	warnings := append([]string{}, data.Warnings...)

	adminIDMap := make(map[int64]int32, len(data.Admins))
	for _, a := range data.Admins {
		created, err := h.store.Queries.ImportAdmin(ctx, generated.ImportAdminParams{
			Username:        a.Username,
			HashedPassword:  a.HashedPassword,
			CreatedAt:       timestamptzFromTime(a.CreatedAt),
			IsSudo:          a.IsSudo,
			PasswordResetAt: timestamptzFromPtr(a.PasswordResetAt),
			TelegramID:      int8FromPtr(a.TelegramID),
			DiscordWebhook:  textFromPtr(a.DiscordWebhook),
			UsersUsage:      a.UsersUsage,
		})
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("admin %q: %v", a.Username, err))
			continue
		}
		adminIDMap[a.SourceID] = created.ID
	}

	inboundsImported := 0
	for _, ib := range data.Inbounds {
		// Protocol is a real, unavoidable guess here: this legacy schema
		// never stored it at the DB level (see ImportedData.Inbounds's own
		// doc comment), and inbounds.protocol has a NOT NULL CHECK
		// constraint (vmess/vless/trojan/shadowsocks only - no empty-string
		// placeholder is legal). "vless" is this codebase's own de facto
		// default everywhere else a default proxy type is needed; the
		// warning already appended above is what actually tells the admin
		// this needs a real value.
		if _, err := h.store.Queries.UpsertInbound(ctx, generated.UpsertInboundParams{
			Tag:      ib.Tag,
			Protocol: "vless",
			Network:  "tcp",
			Security: "none",
		}); err != nil {
			warnings = append(warnings, fmt.Sprintf("inbound %q: %v", ib.Tag, err))
			continue
		}
		inboundsImported++
	}

	userIDMap := make(map[int64]int32, len(data.Users))
	for _, u := range data.Users {
		var adminID pgtype.Int4
		if u.SourceAdminID != nil {
			if newID, ok := adminIDMap[*u.SourceAdminID]; ok {
				adminID = pgtype.Int4{Int32: newID, Valid: true}
			}
		}
		created, err := h.store.Queries.ImportUser(ctx, generated.ImportUserParams{
			Username:               u.Username,
			Status:                 u.Status,
			UsedTraffic:            u.UsedTraffic,
			DataLimit:              int8FromPtr(u.DataLimit),
			Expire:                 pgInt4FromPtr(u.Expire),
			CreatedAt:              timestamptzFromTime(u.CreatedAt),
			AdminID:                adminID,
			DataLimitResetStrategy: u.DataLimitResetStrategy,
			SubRevokedAt:           timestamptzFromPtr(u.SubRevokedAt),
			Note:                   textFromPtr(u.Note),
			SubUpdatedAt:           timestamptzFromPtr(u.SubUpdatedAt),
			SubLastUserAgent:       textFromPtr(u.SubLastUserAgent),
			OnlineAt:               timestamptzFromPtr(u.OnlineAt),
			EditAt:                 timestamptzFromPtr(u.EditAt),
			OnHoldTimeout:          timestamptzFromPtr(u.OnHoldTimeout),
			OnHoldExpireDuration:   int8FromPtr(u.OnHoldExpireDuration),
			AutoDeleteInDays:       pgInt4FromPtr(u.AutoDeleteInDays),
			LastStatusChange:       timestamptzFromPtr(u.LastStatusChange),
		})
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("user %q: %v", u.Username, err))
			continue
		}
		userIDMap[u.SourceID] = created.ID

		for _, p := range u.Proxies {
			if _, err := proxysettings.FromStored(proxysettings.ProxyType(p.Type), p.Settings); err != nil {
				warnings = append(warnings, fmt.Sprintf("user %q: proxy (%s) has invalid settings, skipped: %v", u.Username, p.Type, err))
				continue
			}
			createdProxy, err := h.store.Queries.CreateProxy(ctx, generated.CreateProxyParams{
				UserID:   pgtype.Int4{Int32: created.ID, Valid: true},
				Type:     p.Type,
				Settings: p.Settings,
			})
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("user %q: proxy (%s): %v", u.Username, p.Type, err))
				continue
			}
			if len(p.ExcludedInbounds) > 0 {
				if err := h.store.Queries.ReplaceExcludedInbounds(ctx, generated.ReplaceExcludedInboundsParams{
					ProxyID: createdProxy.ID,
					Column2: p.ExcludedInbounds,
				}); err != nil {
					warnings = append(warnings, fmt.Sprintf("user %q: proxy exclusions: %v", u.Username, err))
				}
			}
		}
	}

	hostsImported := 0
	for _, hst := range data.Hosts {
		if _, err := h.store.Queries.CreateHost(ctx, generated.CreateHostParams{
			Remark:          hst.Remark,
			Address:         hst.Address,
			Port:            pgInt4FromPtr(hst.Port),
			Path:            textFromPtr(hst.Path),
			Sni:             textFromPtr(hst.SNI),
			Host:            textFromPtr(hst.HostHeader),
			Security:        hst.Security,
			Alpn:            hst.ALPN,
			Fingerprint:     hst.Fingerprint,
			InboundTag:      hst.InboundTag,
			Allowinsecure:   pgBoolFromPtr(hst.AllowInsecure),
			IsDisabled:      pgBoolFromPtr(hst.IsDisabled),
			MuxEnable:       hst.MuxEnable,
			FragmentSetting: textFromPtr(hst.FragmentSetting),
			NoiseSetting:    textFromPtr(hst.NoiseSetting),
			RandomUserAgent: hst.RandomUserAgent,
			UseSniAsHost:    hst.UseSNIAsHost,
		}); err != nil {
			warnings = append(warnings, fmt.Sprintf("host %q: %v", hst.Remark, err))
			continue
		}
		hostsImported++
	}

	for _, ut := range data.UserTemplates {
		created, err := h.store.Queries.CreateUserTemplate(ctx, generated.CreateUserTemplateParams{
			Name:           ut.Name,
			DataLimit:      int8FromPtr(ut.DataLimit),
			ExpireDuration: int8FromPtr(ut.ExpireDuration),
			UsernamePrefix: textFromPtr(ut.UsernamePrefix),
			UsernameSuffix: textFromPtr(ut.UsernameSuffix),
		})
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("user template %q: %v", ut.Name, err))
			continue
		}
		if len(ut.InboundTags) > 0 {
			if err := h.store.Queries.ReplaceTemplateInbounds(ctx, generated.ReplaceTemplateInboundsParams{
				UserTemplateID: created.ID,
				Column2:        ut.InboundTags,
			}); err != nil {
				warnings = append(warnings, fmt.Sprintf("user template %q inbounds: %v", ut.Name, err))
			}
		}
	}

	for _, np := range data.NextPlans {
		newUserID, ok := userIDMap[np.SourceUserID]
		if !ok {
			warnings = append(warnings, fmt.Sprintf("a next-plan referenced source user id %d, which wasn't imported, skipped", np.SourceUserID))
			continue
		}
		if _, err := h.store.Queries.UpsertNextPlan(ctx, generated.UpsertNextPlanParams{
			UserID:              newUserID,
			DataLimit:           np.DataLimit,
			Expire:              pgInt4FromPtr(np.Expire),
			AddRemainingTraffic: np.AddRemainingTraffic,
			FireOnEither:        np.FireOnEither,
		}); err != nil {
			warnings = append(warnings, fmt.Sprintf("next-plan for user id %d: %v", newUserID, err))
		}
	}

	if err := h.store.Cache.FlushAll(ctx); err != nil {
		warnings = append(warnings, fmt.Sprintf("import completed but the cache could not be flushed - restart the panel process: %v", err))
	}

	return legacyImportResultDTO{
		SafetyBackup:     safety,
		AdminsImported:   len(adminIDMap),
		UsersImported:    len(userIDMap),
		HostsImported:    hostsImported,
		InboundsImported: inboundsImported,
		Warnings:         warnings,
	}, nil
}
