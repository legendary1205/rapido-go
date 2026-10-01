package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/legendary1205/rapido-go/internal/db/generated"
	"github.com/legendary1205/rapido-go/internal/subscription"
)

const pasarGuardImportSecret = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// pgCopy renders one pg_dump COPY block.
func pgCopy(table string, cols []string, rows ...[]string) string {
	var b strings.Builder
	b.WriteString("COPY public." + table + " (" + strings.Join(cols, ", ") + ") FROM stdin;\n")
	for _, r := range rows {
		b.WriteString(strings.Join(r, "\t") + "\n")
	}
	b.WriteString("\\.\n")
	return b.String()
}

// pasarGuardDumpFixture is a small synthetic PasarGuard 5.x pg_dump: an
// owner and an operator, three users in group "main" (whose only inbound is
// a VLESS one), ids with gaps the way a real panel's are.
func pasarGuardDumpFixture() string {
	var b strings.Builder
	b.WriteString("--\n-- PostgreSQL database dump\n--\n\n")
	for _, t := range []string{"admin_roles", "admins", "alembic_version", "core_configs", "groups", "hosts", "inbounds",
		"inbounds_groups_association", "jwt", "users", "users_groups_association"} {
		b.WriteString("CREATE TABLE public." + t + " (\n    id bigint\n);\n")
	}
	b.WriteString(pgCopy("admin_roles", []string{"id", "name", "is_owner"}, []string{"1", "owner", "t"}, []string{"3", "operator", "f"}))
	b.WriteString(pgCopy("admins", []string{"id", "username", "hashed_password", "created_at", "telegram_id", "used_traffic", "role_id", "status"},
		[]string{"1", "pg_owner", "$2b$12$ownerhash", "2026-06-28 18:59:53.044686+00", "45312485", "1000", "1", "active"},
		[]string{"2", "pg_seller", "$2b$12$sellerhash", "2026-08-08 10:03:00+00", `\N`, "50", "3", "active"}))
	b.WriteString(pgCopy("core_configs", []string{"id", "name", "config", "type"},
		[]string{"1", "Default", `{"inbounds": [{"tag": "VLESS un", "protocol": "vless"}]}`, "xray"}))
	b.WriteString(pgCopy("groups", []string{"id", "name", "is_disabled"}, []string{"1", "main", "f"}))
	b.WriteString(pgCopy("hosts", []string{"id", "remark", "address", "port", "inbound_tag", "is_disabled"},
		[]string{"3", "PG host", "ggv2.example.com", "443", "VLESS un", "f"}))
	b.WriteString(pgCopy("inbounds", []string{"id", "tag"}, []string{"429", "VLESS un"}))
	b.WriteString(pgCopy("inbounds_groups_association", []string{"inbound_id", "group_id"}, []string{"429", "1"}))
	b.WriteString(pgCopy("jwt", []string{"id", "secret_key"}, []string{"1", pasarGuardImportSecret}))
	b.WriteString(pgCopy("users", []string{"id", "username", "status", "used_traffic", "data_limit", "created_at", "admin_id", "data_limit_reset_strategy", "sub_revoked_at", "note", "expire", "proxy_settings"},
		[]string{"7", "Ali.shvip", "active", "47231285", "4294967296", "2026-09-29 16:11:08.91145+00", "1", "no_reset", `\N`, "vip", "2030-01-01 00:00:00+00",
			`{"vless": {"id": "688b1a71-fde2-4819-aa5d-b9d71dfa7780", "flow": ""}, "vmess": {"id": "201cfd36-1303-4245-ba33-42585670ed53"}}`},
		[]string{"1059", "Parima.Gozarban", "limited", "100", "100", "2026-09-01 00:00:00+00", "2", "no_reset", "2026-09-15 00:00:00+00", "", `\N`,
			`{"vless": {"id": "11111111-1111-1111-1111-111111111111", "flow": ""}}`},
		[]string{"1060", "Parima.gozarban", "active", "0", `\N`, "2026-09-02 00:00:00+00", "2", "no_reset", `\N`, `\N`, `\N`,
			`{"vless": {"id": "22222222-2222-2222-2222-222222222222", "flow": ""}}`}))
	b.WriteString(pgCopy("users_groups_association", []string{"user_id", "groups_id"}, []string{"7", "1"}, []string{"1059", "1"}, []string{"1060", "1"}))
	b.WriteString("\n--\n-- PostgreSQL database dump complete\n--\n")
	return b.String()
}

// pasarGuardV3Token mints a link exactly the way PasarGuard 5.x does
// (create_subscription_token): HMAC-SHA256 over base64url("v3,<id>,<ts>"),
// keyed by the signing key's hex text.
func pasarGuardV3Token(secretHex string, userID int64, ts int64) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("v3,%d,%d", userID, ts)))
	mac := hmac.New(sha256.New, []byte(secretHex))
	mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestRestoreUploadImportsPasarGuardDumpKeepingThisPanelsInbounds(t *testing.T) {
	router, token, handler := newTestRouterAndHandler(t)
	handler.dumpDatabase = fakeDump("-- pre-import state\n")
	ctx := context.Background()
	q := handler.store.Queries

	// This panel's own setup before the import: an inbound with its host
	// (must survive - a PasarGuard import replaces users only) and a local
	// user (must be replaced).
	if _, err := q.UpsertInbound(ctx, generated.UpsertInboundParams{Tag: "main", Protocol: "vless", Network: "tcp", Security: "none"}); err != nil {
		t.Fatalf("seed inbound: %v", err)
	}
	if _, err := q.CreateHost(ctx, generated.CreateHostParams{Remark: "main host", Address: "panel.example.com", InboundTag: "main", Security: "inbound_default", Alpn: "none", Fingerprint: "none"}); err != nil {
		t.Fatalf("seed host: %v", err)
	}
	if resp := doRequest(t, router, "POST", "/api/user", token, map[string]any{"username": "local_test", "proxies": map[string]any{"vless": map[string]any{}}}); resp.Code != http.StatusOK && resp.Code != http.StatusCreated {
		t.Fatalf("seed user: %d %s", resp.Code, resp.Raw)
	}

	resp := doMultipartRestoreUpload(t, router, token, true, "pasarguard.sql", []byte(pasarGuardDumpFixture()))
	if resp.Code != http.StatusOK {
		t.Fatalf("pasarguard import upload: %d %s", resp.Code, resp.Raw)
	}
	var result legacyImportResultDTO
	if err := json.Unmarshal(resp.Raw, &result); err != nil {
		t.Fatalf("decode import result: %v", err)
	}
	if result.AdminsImported != 2 || result.UsersImported != 3 || result.HostsImported != 0 || result.InboundsImported != 0 {
		t.Fatalf("import counts = %+v, want 2 admins, 3 users, no hosts/inbounds", result)
	}

	// Every user kept its PasarGuard id - that is what its v3 links name.
	for id, want := range map[int32]string{7: "Ali.shvip", 1059: "Parima.Gozarban", 1060: "Parima.gozarban"} {
		u, err := q.GetUserByID(ctx, id)
		if err != nil || u.Username != want {
			t.Errorf("user id %d = %q (%v), want %q", id, u.Username, err, want)
		}
	}
	if _, err := q.GetUserByUsername(ctx, "local_test"); err == nil {
		t.Error("the pre-import local user survived a full user replacement")
	}

	// The id sequence moved past the imported ids.
	created := doRequest(t, router, "POST", "/api/user", token, map[string]any{"username": "after_import", "proxies": map[string]any{"vless": map[string]any{}}})
	if created.Code != http.StatusOK && created.Code != http.StatusCreated {
		t.Fatalf("create a user after the import: %d %s", created.Code, created.Raw)
	}
	if u, err := q.GetUserByUsername(ctx, "after_import"); err != nil || u.ID != 1061 {
		t.Errorf("first user after the import got id %d (%v), want 1061", u.ID, err)
	}

	// This panel's inbound and host are untouched; PasarGuard's were not added.
	inbounds, err := q.ListInbounds(ctx)
	if err != nil {
		t.Fatalf("list inbounds: %v", err)
	}
	if len(inbounds) != 1 || inbounds[0].Tag != "main" {
		t.Errorf("inbounds after import = %+v, want only this panel's \"main\"", inbounds)
	}
	hosts, err := q.ListHosts(ctx)
	if err != nil {
		t.Fatalf("list hosts: %v", err)
	}
	if len(hosts) != 1 || hosts[0].Address != "panel.example.com" {
		t.Errorf("hosts after import = %+v, want only this panel's own", hosts)
	}

	// Owner + operator mapping, same password hash.
	owner, err := q.GetAdminByUsername(ctx, "pg_owner")
	if err != nil || !owner.IsSudo || !owner.IsOwner || owner.HashedPassword != "$2b$12$ownerhash" {
		t.Errorf("pg_owner = %+v (%v), want sudo+owner with the original hash", owner, err)
	}
	seller, err := q.GetAdminByUsername(ctx, "pg_seller")
	if err != nil || seller.IsSudo || seller.IsOwner {
		t.Errorf("pg_seller = %+v (%v), want a regular admin", seller, err)
	}
	if u, _ := q.GetUserByID(ctx, 1059); !u.AdminID.Valid || u.AdminID.Int32 != seller.ID {
		t.Errorf("Parima.Gozarban admin = %+v, want pg_seller (%d)", u.AdminID, seller.ID)
	}

	// Credentials: the original VLESS UUID, and only the protocol group
	// "main" grants (the vmess entry in proxy_settings is not imported).
	proxies, err := q.ListProxiesByUserID(ctx, pgInt4FromInt(7))
	if err != nil || len(proxies) != 1 || proxies[0].Type != "vless" || !strings.Contains(string(proxies[0].Settings), "688b1a71-fde2-4819-aa5d-b9d71dfa7780") {
		t.Errorf("Ali.shvip proxies = %+v (%v), want one vless with the original UUID", proxies, err)
	}

	// The signing key was adopted (takes effect on the next restart).
	stored, err := q.GetJWTSecret(ctx)
	if err != nil || stored.SecretKey != pasarGuardImportSecret {
		t.Errorf("jwt secret = %q (%v), want the PasarGuard key", stored.SecretKey, err)
	}

	// Links. The handler still signs with its startup key (a restart loads
	// the adopted one), so mint against that key: a PasarGuard v3 link
	// resolves its user by id; a link minted before the user's
	// sub_revoked_at does not; this panel's own username links still work.
	secretHex := hex.EncodeToString(handler.jwtSecret)
	now := time.Now().Unix()
	info := doRequest(t, router, "GET", "/sub/"+pasarGuardV3Token(secretHex, 7, now)+"/info", "", nil)
	if info.Code != http.StatusOK || info.Body["username"] != "Ali.shvip" {
		t.Errorf("v3 link for id 7: %d %s, want Ali.shvip", info.Code, info.Raw)
	}
	revoked := doRequest(t, router, "GET", "/sub/"+pasarGuardV3Token(secretHex, 1059, time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC).Unix())+"/info", "", nil)
	if revoked.Code != http.StatusNotFound {
		t.Errorf("v3 link minted before sub_revoked_at: %d, want 404", revoked.Code)
	}
	if missing := doRequest(t, router, "GET", "/sub/"+pasarGuardV3Token(secretHex, 999, now)+"/info", "", nil); missing.Code != http.StatusNotFound {
		t.Errorf("v3 link for a nonexistent id: %d, want 404", missing.Code)
	}
	own := doRequest(t, router, "GET", "/sub/"+subscription.CreateToken("Parima.gozarban", handler.jwtSecret)+"/info", "", nil)
	if own.Code != http.StatusOK || own.Body["username"] != "Parima.gozarban" {
		t.Errorf("username link for Parima.gozarban: %d %s", own.Code, own.Raw)
	}
}

func TestDetectUploadFormatTellsPasarGuardFromANativeDump(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	native := write("native.sql", "--\n-- PostgreSQL database dump\n--\n\nCREATE TABLE public.goose_db_version (\n    id integer\n);\nCREATE TABLE public.users (\n    id integer\n);\nCOPY public.users (id) FROM stdin;\n1\n\\.\n")
	pg := write("pasarguard.sql", pasarGuardDumpFixture())

	if d, err := detectUploadFormat(native); err != nil || d.format != uploadFormatNativePostgres {
		t.Errorf("native dump detected as %v (%v), want native Postgres", d.format, err)
	}
	d, err := detectUploadFormat(pg)
	if err != nil || d.format != uploadFormatPasarGuardPostgres || d.pgDump == nil {
		t.Fatalf("PasarGuard dump detected as %v (%v), want PasarGuard with a parsed dump", d.format, err)
	}
	if rows, _ := d.pgDump.Rows("users"); len(rows) != 3 {
		t.Errorf("parsed users = %d, want 3", len(rows))
	}
}
