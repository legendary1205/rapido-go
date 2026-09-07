package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

// A small, synthetic mysqldump matching the legacy Marzban/Rapido schema
// signature (admins + proxies tables present) - not the real user-provided
// sample (never committed, see internal/legacyimport's own test files for
// why), but shaped identically to what a real one looks like.
const legacyMarzbanDumpFixture = "-- MySQL dump 10.13  Distrib 8.0.45, for Linux (aarch64)\n" +
	"--\n" +
	"-- Host: 127.0.0.1    Database: marzban\n" +
	"\n" +
	"CREATE TABLE `admins` (\n" +
	"  `id` int NOT NULL AUTO_INCREMENT,\n" +
	"  `username` varchar(34) DEFAULT NULL,\n" +
	"  `hashed_password` varchar(128) DEFAULT NULL,\n" +
	"  `created_at` datetime DEFAULT NULL,\n" +
	"  `is_sudo` tinyint(1) DEFAULT '0',\n" +
	"  `password_reset_at` datetime DEFAULT NULL,\n" +
	"  `telegram_id` bigint DEFAULT NULL,\n" +
	"  `discord_webhook` varchar(1024) DEFAULT NULL,\n" +
	"  `users_usage` bigint NOT NULL DEFAULT '0',\n" +
	"  PRIMARY KEY (`id`)\n" +
	") ENGINE=InnoDB;\n" +
	"INSERT INTO `admins` VALUES (1,'legacy_sudo','$2b$12$fakehash','2026-01-01 00:00:00',1,NULL,NULL,NULL,0);\n" +
	"\n" +
	"CREATE TABLE `inbounds` (\n" +
	"  `id` int NOT NULL AUTO_INCREMENT,\n" +
	"  `tag` varchar(256) NOT NULL,\n" +
	"  PRIMARY KEY (`id`)\n" +
	") ENGINE=InnoDB;\n" +
	"INSERT INTO `inbounds` VALUES (1,'Legacy VLESS TCP');\n" +
	"\n" +
	"CREATE TABLE `proxies` (\n" +
	"  `id` int NOT NULL AUTO_INCREMENT,\n" +
	"  `user_id` int DEFAULT NULL,\n" +
	"  `type` enum('VMess','VLESS','Trojan','Shadowsocks') NOT NULL,\n" +
	"  `settings` json NOT NULL,\n" +
	"  PRIMARY KEY (`id`)\n" +
	") ENGINE=InnoDB;\n" +
	"INSERT INTO `proxies` VALUES (1,1,'VLESS','{\\\"id\\\": \\\"11111111-1111-1111-1111-111111111111\\\", \\\"flow\\\": \\\"xtls-rprx-vision\\\"}');\n" +
	"\n" +
	"CREATE TABLE `users` (\n" +
	"  `id` int NOT NULL AUTO_INCREMENT,\n" +
	"  `username` varchar(34) DEFAULT NULL,\n" +
	"  `status` enum('on_hold','active','limited','expired','disabled') NOT NULL,\n" +
	"  `used_traffic` bigint DEFAULT NULL,\n" +
	"  `data_limit` bigint DEFAULT NULL,\n" +
	"  `expire` int DEFAULT NULL,\n" +
	"  `created_at` datetime DEFAULT NULL,\n" +
	"  `admin_id` int DEFAULT NULL,\n" +
	"  `data_limit_reset_strategy` enum('no_reset','day','week','month','year') NOT NULL DEFAULT 'no_reset',\n" +
	"  `sub_revoked_at` datetime DEFAULT NULL,\n" +
	"  `note` varchar(500) DEFAULT NULL,\n" +
	"  `sub_updated_at` datetime DEFAULT NULL,\n" +
	"  `sub_last_user_agent` varchar(512) DEFAULT NULL,\n" +
	"  `online_at` datetime DEFAULT NULL,\n" +
	"  `edit_at` datetime DEFAULT NULL,\n" +
	"  `on_hold_timeout` datetime DEFAULT NULL,\n" +
	"  `on_hold_expire_duration` bigint DEFAULT NULL,\n" +
	"  `auto_delete_in_days` int DEFAULT NULL,\n" +
	"  `last_status_change` datetime DEFAULT NULL,\n" +
	"  PRIMARY KEY (`id`)\n" +
	") ENGINE=InnoDB;\n" +
	"INSERT INTO `users` VALUES (1,'legacy_alice','active',123456,5000000000,1999999999,'2026-03-05 10:00:00',1,'no_reset',NULL,'imported from legacy',NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL);\n" +
	"\n" +
	"CREATE TABLE `hosts` (\n" +
	"  `id` int NOT NULL AUTO_INCREMENT,\n" +
	"  `remark` varchar(256) NOT NULL,\n" +
	"  `address` varchar(256) NOT NULL,\n" +
	"  `port` int DEFAULT NULL,\n" +
	"  `inbound_tag` varchar(256) NOT NULL,\n" +
	"  `sni` varchar(1000) DEFAULT NULL,\n" +
	"  `host` varchar(1000) DEFAULT NULL,\n" +
	"  `security` enum('inbound_default','none','tls') NOT NULL DEFAULT 'inbound_default',\n" +
	"  `alpn` enum('h3','h3,h2','h3,h2,http/1.1','none','h2','http/1.1','h2,http/1.1') NOT NULL,\n" +
	"  `fingerprint` enum('none','chrome','firefox','safari','ios','android','edge','360','qq','random','randomized') NOT NULL DEFAULT 'none',\n" +
	"  `allowinsecure` tinyint(1) DEFAULT NULL,\n" +
	"  `is_disabled` tinyint(1) DEFAULT NULL,\n" +
	"  `path` varchar(256) DEFAULT NULL,\n" +
	"  `mux_enable` tinyint(1) NOT NULL DEFAULT '0',\n" +
	"  `fragment_setting` varchar(100) DEFAULT NULL,\n" +
	"  `random_user_agent` tinyint(1) NOT NULL DEFAULT '0',\n" +
	"  `noise_setting` varchar(2000) DEFAULT NULL,\n" +
	"  `use_sni_as_host` tinyint(1) NOT NULL DEFAULT '0',\n" +
	"  PRIMARY KEY (`id`)\n" +
	") ENGINE=InnoDB;\n" +
	"INSERT INTO `hosts` VALUES (1,'{USERNAME} legacy host','legacy.example.com',443,'Legacy VLESS TCP',NULL,NULL,'inbound_default','none','none',NULL,0,NULL,0,NULL,0,NULL,0);\n"

func TestRestoreUploadImportsLegacyMarzbanDump(t *testing.T) {
	router, token, handler := newTestRouterAndHandler(t)
	// A safety backup is taken automatically as part of the import - use a
	// fake dump so that step doesn't need the real pg_dump binary.
	handler.dumpDatabase = fakeDump("-- pre-import state\n")

	resp := doMultipartRestoreUpload(t, router, token, true, "legacy_marzban.sql", []byte(legacyMarzbanDumpFixture))
	if resp.Code != http.StatusOK {
		t.Fatalf("legacy import upload: %d %s", resp.Code, resp.Raw)
	}
	var result legacyImportResultDTO
	if err := json.Unmarshal(resp.Raw, &result); err != nil {
		t.Fatalf("decode import result: %v", err)
	}
	if result.AdminsImported != 1 || result.UsersImported != 1 || result.HostsImported != 1 || result.InboundsImported != 1 {
		t.Fatalf("import counts = %+v, want 1 each", result)
	}
	if result.SafetyBackup.Filename == "" {
		t.Error("expected a safety backup to have been taken before the import")
	}
	foundPlaceholderWarning := false
	for _, w := range result.Warnings {
		if w == "" {
			continue
		}
		foundPlaceholderWarning = true
	}
	if !foundPlaceholderWarning {
		t.Error("expected at least the standard 'configure protocol by hand' inbound warning")
	}

	// Confirm the data actually landed in the real database, not just that
	// the handler reported success.
	adminsResp := doRequest(t, router, "GET", "/api/admins", token, nil)
	if adminsResp.Code != http.StatusOK {
		t.Fatalf("list admins: %d %s", adminsResp.Code, adminsResp.Raw)
	}
	var admins []map[string]any
	if err := json.Unmarshal(adminsResp.Raw, &admins); err != nil {
		t.Fatalf("decode admins: %v", err)
	}
	foundImportedAdmin := false
	for _, a := range admins {
		if a["username"] == "legacy_sudo" {
			foundImportedAdmin = true
			if a["is_sudo"] != true {
				t.Errorf("legacy_sudo.is_sudo = %v, want true", a["is_sudo"])
			}
		}
	}
	if !foundImportedAdmin {
		t.Errorf("legacy_sudo not found in admins list: %v", admins)
	}

	userResp := doRequest(t, router, "GET", "/api/user/legacy_alice", token, nil)
	if userResp.Code != http.StatusOK {
		t.Fatalf("get imported user: %d %s", userResp.Code, userResp.Raw)
	}
	if userResp.Body["status"] != "active" {
		t.Errorf("legacy_alice.status = %v, want active", userResp.Body["status"])
	}
	if userResp.Body["note"] != "imported from legacy" {
		t.Errorf("legacy_alice.note = %v, want the imported note (historical field preservation)", userResp.Body["note"])
	}
	proxies, _ := userResp.Body["proxies"].(map[string]any)
	if proxies == nil || proxies["vless"] == nil {
		t.Errorf("legacy_alice.proxies = %v, want a vless entry", userResp.Body["proxies"])
	}

	hostsResp := doRequest(t, router, "GET", "/api/hosts", token, nil)
	if hostsResp.Code != http.StatusOK {
		t.Fatalf("get hosts: %d %s", hostsResp.Code, hostsResp.Raw)
	}
	tagHosts, ok := hostsResp.Body["Legacy VLESS TCP"].([]any)
	if !ok || len(tagHosts) != 1 {
		t.Fatalf("hosts for imported inbound tag = %v, want exactly 1 - full hosts body: %s - import warnings: %v", hostsResp.Body["Legacy VLESS TCP"], hostsResp.Raw, result.Warnings)
	}
}

// A small, synthetic Hiddify Panel export - real top-level keys and field
// names (see internal/legacyimport/hiddify.go's own doc comment for how
// those were confirmed against a live instance), fictional values.
const hiddifyExportFixture = `{
  "admin_users": [
    {"uuid": "owner-uuid", "name": "hiddify_owner", "mode": "super_admin", "parent_admin_uuid": "owner-uuid"}
  ],
  "users": [
    {
      "uuid": "22222222-2222-2222-2222-222222222222", "name": "hiddify_bob",
      "added_by_uuid": "owner-uuid", "enable": true,
      "current_usage_GB": 1, "usage_limit_GB": 20,
      "package_days": 30, "start_date": "2030-01-01", "mode": "no_reset"
    },
    {
      "uuid": "33333333-3333-3333-3333-333333333333", "name": "hiddify_carol",
      "added_by_uuid": "owner-uuid", "enable": true,
      "current_usage_GB": 2, "usage_limit_GB": 20,
      "package_days": 30, "start_date": "2030-01-01", "mode": "no_reset"
    }
  ],
  "domains": [],
  "proxies": [
    {"enable": true, "proto": "vless", "transport": "tcp", "l3": "tls"}
  ]
}`

func TestRestoreUploadImportsHiddifyExport(t *testing.T) {
	router, token, handler := newTestRouterAndHandler(t)
	handler.dumpDatabase = fakeDump("-- pre-import state\n")

	resp := doMultipartRestoreUpload(t, router, token, true, "hiddify_backup.json", []byte(hiddifyExportFixture))
	if resp.Code != http.StatusOK {
		t.Fatalf("hiddify import upload: %d %s", resp.Code, resp.Raw)
	}
	var result legacyImportResultDTO
	if err := json.Unmarshal(resp.Raw, &result); err != nil {
		t.Fatalf("decode import result: %v", err)
	}
	// UsersImported specifically exercises a real bug this exact test
	// caught: every Hiddify user shared the same (zero-value) SourceID
	// before hiddify.go assigned each one a real synthetic id, so
	// loadLegacyImport's userIDMap[u.SourceID] = created.ID collided down
	// to a single key - both users were actually written to the database
	// correctly, but the reported count silently undercounted to 1. A
	// single-user fixture can never catch this class of bug.
	if result.AdminsImported != 1 || result.UsersImported != 2 || result.InboundsImported != 1 {
		t.Fatalf("import counts = %+v, want 1 admin, 2 users, 1 inbound", result)
	}

	adminsResp := doRequest(t, router, "GET", "/api/admins", token, nil)
	var admins []map[string]any
	if err := json.Unmarshal(adminsResp.Raw, &admins); err != nil {
		t.Fatalf("decode admins: %v", err)
	}
	foundImportedAdmin := false
	for _, a := range admins {
		if a["username"] == "hiddify_owner" {
			foundImportedAdmin = true
			if a["is_sudo"] != true {
				t.Errorf("hiddify_owner.is_sudo = %v, want true", a["is_sudo"])
			}
		}
	}
	if !foundImportedAdmin {
		t.Errorf("hiddify_owner not found in admins list: %v", admins)
	}

	userResp := doRequest(t, router, "GET", "/api/user/hiddify_bob", token, nil)
	if userResp.Code != http.StatusOK {
		t.Fatalf("get imported user: %d %s", userResp.Code, userResp.Raw)
	}
	if userResp.Body["status"] != "active" {
		t.Errorf("hiddify_bob.status = %v, want active", userResp.Body["status"])
	}
	proxies, _ := userResp.Body["proxies"].(map[string]any)
	if proxies == nil || proxies["vless"] == nil {
		t.Errorf("hiddify_bob.proxies = %v, want a vless entry keyed on their Hiddify uuid", userResp.Body["proxies"])
	}
}

func TestRestoreUploadRejectsUnrecognizedMySQLDump(t *testing.T) {
	router, token, _ := newTestRouterAndHandler(t)
	unrelated := "-- MySQL dump 10.13\nCREATE TABLE `some_other_app_table` (\n  `id` int NOT NULL,\n  PRIMARY KEY (`id`)\n) ENGINE=InnoDB;\n"
	resp := doMultipartRestoreUpload(t, router, token, true, "unrelated.sql", []byte(unrelated))
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("upload of an unrecognized MySQL dump: %d %s, want 400", resp.Code, resp.Raw)
	}
}
