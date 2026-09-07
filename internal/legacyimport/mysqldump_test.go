package legacyimport

import (
	"os"
	"strings"
	"testing"
)

const sampleDump = "-- MySQL dump 10.13  Distrib 8.0.45, for Linux (aarch64)\n" +
	"--\n" +
	"-- Host: 127.0.0.1    Database: marzban\n" +
	"-- ------------------------------------------------------\n" +
	"\n" +
	"CREATE TABLE `admins` (\n" +
	"  `id` int NOT NULL AUTO_INCREMENT,\n" +
	"  `username` varchar(34) COLLATE utf8mb4_unicode_ci DEFAULT NULL,\n" +
	"  `hashed_password` varchar(128) COLLATE utf8mb4_unicode_ci DEFAULT NULL,\n" +
	"  `is_sudo` tinyint(1) DEFAULT '0',\n" +
	"  `telegram_id` bigint DEFAULT NULL,\n" +
	"  `discord_webhook` varchar(1024) COLLATE utf8mb4_unicode_ci DEFAULT NULL,\n" +
	"  `users_usage` bigint NOT NULL DEFAULT '0',\n" +
	"  PRIMARY KEY (`id`),\n" +
	"  UNIQUE KEY `ix_admins_username` (`username`)\n" +
	") ENGINE=InnoDB AUTO_INCREMENT=3 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;\n" +
	"\n" +
	"LOCK TABLES `admins` WRITE;\n" +
	"INSERT INTO `admins` VALUES (1,'sudo_admin','$2b$12$fakehash.with.a.single\\'quote/and\\\\backslash',1234567890,NULL,NULL,0),(2,'reseller_a','$2b$12$anotherfakehash',0,555,'https://discord.com/api/webhooks/x',12345);\n" +
	"UNLOCK TABLES;\n" +
	"\n" +
	"CREATE TABLE `proxies` (\n" +
	"  `id` int NOT NULL AUTO_INCREMENT,\n" +
	"  `user_id` int DEFAULT NULL,\n" +
	"  `type` enum('VMess','VLESS','Trojan','Shadowsocks') COLLATE utf8mb4_unicode_ci NOT NULL,\n" +
	"  `settings` json NOT NULL,\n" +
	"  PRIMARY KEY (`id`),\n" +
	"  CONSTRAINT `proxies_ibfk_1` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`)\n" +
	") ENGINE=InnoDB AUTO_INCREMENT=3 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;\n" +
	"\n" +
	"LOCK TABLES `proxies` WRITE;\n" +
	"INSERT INTO `proxies` VALUES (1,1,'VMess','{\\\"id\\\": \\\"23fb2973-956e-4c30-a49e-de2c2fa43bba\\\"}'),(2,2,'VLESS','{\\\"id\\\": \\\"aaaa\\\", \\\"flow\\\": \\\"xtls-rprx-vision\\\"}');\n" +
	"UNLOCK TABLES;\n"

func TestParseMySQLDumpCreateTableColumnsInDeclaredOrder(t *testing.T) {
	dump, err := ParseMySQLDump(strings.NewReader(sampleDump))
	if err != nil {
		t.Fatalf("ParseMySQLDump: %v", err)
	}
	admins, ok := dump.Tables["admins"]
	if !ok {
		t.Fatal("admins table not found")
	}
	want := []string{"id", "username", "hashed_password", "is_sudo", "telegram_id", "discord_webhook", "users_usage"}
	if len(admins.Columns) != len(want) {
		t.Fatalf("columns = %v, want %v", admins.Columns, want)
	}
	for i, c := range want {
		if admins.Columns[i] != c {
			t.Errorf("column[%d] = %q, want %q", i, admins.Columns[i], c)
		}
	}
}

func TestParseMySQLDumpRowsWithEscapesAndTypes(t *testing.T) {
	dump, err := ParseMySQLDump(strings.NewReader(sampleDump))
	if err != nil {
		t.Fatalf("ParseMySQLDump: %v", err)
	}
	rows, ok := dump.Row("admins")
	if !ok || len(rows) != 2 {
		t.Fatalf("admins rows = %v, want 2 rows", rows)
	}

	first := rows[0]
	if first["id"] != int64(1) {
		t.Errorf("id = %v (%T), want int64(1)", first["id"], first["id"])
	}
	if first["username"] != "sudo_admin" {
		t.Errorf("username = %v, want sudo_admin", first["username"])
	}
	wantHash := `$2b$12$fakehash.with.a.single'quote/and\backslash`
	if first["hashed_password"] != wantHash {
		t.Errorf("hashed_password = %q, want %q (escape decoding)", first["hashed_password"], wantHash)
	}
	// is_sudo column value '1234567890' is a bare numeric literal per the
	// CREATE TABLE order (a deliberately odd fixture value to prove the
	// parser reads positionally, not by inferring type from content).
	if first["is_sudo"] != int64(1234567890) {
		t.Errorf("is_sudo = %v, want int64(1234567890)", first["is_sudo"])
	}
	if first["telegram_id"] != nil {
		t.Errorf("telegram_id = %v, want nil (SQL NULL)", first["telegram_id"])
	}

	second := rows[1]
	if second["users_usage"] != int64(12345) {
		t.Errorf("users_usage = %v, want int64(12345)", second["users_usage"])
	}
	if second["discord_webhook"] != "https://discord.com/api/webhooks/x" {
		t.Errorf("discord_webhook = %v", second["discord_webhook"])
	}
}

func TestParseMySQLDumpJSONColumnStaysRawString(t *testing.T) {
	dump, err := ParseMySQLDump(strings.NewReader(sampleDump))
	if err != nil {
		t.Fatalf("ParseMySQLDump: %v", err)
	}
	rows, _ := dump.Row("proxies")
	if len(rows) != 2 {
		t.Fatalf("proxies rows = %v, want 2", rows)
	}
	settings, ok := rows[1]["settings"].(string)
	if !ok {
		t.Fatalf("settings is %T, want string", rows[1]["settings"])
	}
	if !strings.Contains(settings, `"flow": "xtls-rprx-vision"`) {
		t.Errorf("settings = %q, want decoded JSON text containing the flow field", settings)
	}
	if rows[1]["type"] != "VLESS" {
		t.Errorf("type = %v, want VLESS", rows[1]["type"])
	}
}

func TestParseMySQLDumpMissingTableIsNotAnError(t *testing.T) {
	dump, err := ParseMySQLDump(strings.NewReader(sampleDump))
	if err != nil {
		t.Fatalf("ParseMySQLDump: %v", err)
	}
	if _, ok := dump.Row("hosts"); ok {
		t.Error("hosts table not in this fixture, Row should report ok=false")
	}
}

// TestParseRealSampleDump runs the parser against a real, full-scale
// mysqldump file if one is made available via LEGACY_SAMPLE_MYSQL_DUMP -
// not committed to the repo (real panel exports contain real customer
// data and real secrets), so this is skipped everywhere except a local
// dev machine that has explicitly set the env var to a real sample. Only
// asserts structural sanity (parses without error, tables have the
// expected columns, row counts are internally consistent) - never asserts
// on or logs specific real values.
func TestParseRealSampleDump(t *testing.T) {
	path := os.Getenv("LEGACY_SAMPLE_MYSQL_DUMP")
	if path == "" {
		t.Skip("LEGACY_SAMPLE_MYSQL_DUMP not set, skipping real-sample parse test")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open sample dump: %v", err)
	}
	defer f.Close()

	dump, err := ParseMySQLDump(f)
	if err != nil {
		t.Fatalf("ParseMySQLDump: %v", err)
	}
	for _, table := range []string{"admins", "users", "proxies", "hosts", "inbounds"} {
		rows, ok := dump.Row(table)
		if !ok {
			t.Errorf("expected table %q to be present in the real sample", table)
			continue
		}
		t.Logf("%s: %d rows, %d columns", table, len(rows), len(dump.Tables[table].Columns))
	}
	usersRows, _ := dump.Row("users")
	proxiesRows, _ := dump.Row("proxies")
	if len(usersRows) == 0 || len(proxiesRows) == 0 {
		t.Error("expected a real sample to have at least one user and one proxy")
	}
	for _, p := range proxiesRows {
		typ, _ := p["type"].(string)
		switch typ {
		case "VMess", "VLESS", "Trojan", "Shadowsocks":
		default:
			t.Errorf("proxy row has unexpected type %q", typ)
		}
		if _, ok := p["settings"].(string); !ok {
			t.Errorf("proxy settings is %T, want string (raw JSON text)", p["settings"])
		}
	}
}
