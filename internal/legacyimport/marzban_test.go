package legacyimport

import (
	"os"
	"strings"
	"testing"
)

const marzbanFixture = "CREATE TABLE `admins` (\n" +
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
	"INSERT INTO `admins` VALUES (1,'root','$2b$12$hash','2026-01-01 00:00:00',1,NULL,NULL,NULL,0);\n" +
	"\n" +
	"CREATE TABLE `inbounds` (\n" +
	"  `id` int NOT NULL AUTO_INCREMENT,\n" +
	"  `tag` varchar(256) NOT NULL,\n" +
	"  PRIMARY KEY (`id`)\n" +
	") ENGINE=InnoDB;\n" +
	"INSERT INTO `inbounds` VALUES (1,'VLESS TCP'),(2,'VMess WS');\n" +
	"\n" +
	"CREATE TABLE `exclude_inbounds_association` (\n" +
	"  `proxy_id` int DEFAULT NULL,\n" +
	"  `inbound_tag` varchar(256) NOT NULL\n" +
	") ENGINE=InnoDB;\n" +
	"INSERT INTO `exclude_inbounds_association` VALUES (1,'Nonexistent Tag');\n" +
	"\n" +
	"CREATE TABLE `proxies` (\n" +
	"  `id` int NOT NULL AUTO_INCREMENT,\n" +
	"  `user_id` int DEFAULT NULL,\n" +
	"  `type` enum('VMess','VLESS','Trojan','Shadowsocks') NOT NULL,\n" +
	"  `settings` json NOT NULL,\n" +
	"  PRIMARY KEY (`id`)\n" +
	") ENGINE=InnoDB;\n" +
	"INSERT INTO `proxies` VALUES (1,1,'VLESS','{\\\"id\\\": \\\"uuid-1\\\", \\\"flow\\\": \\\"xtls-rprx-vision\\\"}'),(2,2,'Weird','{}');\n" +
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
	"INSERT INTO `users` VALUES (1,'alice','active',1000,5000000000,1999999999,'2026-01-01 00:00:00',1,'no_reset',NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL)," +
	"(2,'orphan','active',0,NULL,NULL,'2026-01-02 00:00:00',99,'no_reset',NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL);\n" +
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
	"INSERT INTO `hosts` VALUES (1,'{USERNAME}','example.com',443,'VLESS TCP',NULL,NULL,'inbound_default','none','none',NULL,0,NULL,0,NULL,0,NULL,0)," +
	"(2,'ghost','example.com',443,'Nonexistent Tag',NULL,NULL,'inbound_default','none','none',NULL,0,NULL,0,NULL,0,NULL,0);\n" +
	"\n" +
	"CREATE TABLE `user_templates` (\n" +
	"  `id` int NOT NULL AUTO_INCREMENT,\n" +
	"  `name` varchar(64) NOT NULL,\n" +
	"  `data_limit` bigint DEFAULT NULL,\n" +
	"  `expire_duration` bigint DEFAULT NULL,\n" +
	"  `username_prefix` varchar(20) DEFAULT NULL,\n" +
	"  `username_suffix` varchar(20) DEFAULT NULL,\n" +
	"  PRIMARY KEY (`id`)\n" +
	") ENGINE=InnoDB;\n" +
	"INSERT INTO `user_templates` VALUES (1,'basic',10000000000,2592000,NULL,NULL);\n" +
	"\n" +
	"CREATE TABLE `template_inbounds_association` (\n" +
	"  `user_template_id` int DEFAULT NULL,\n" +
	"  `inbound_tag` varchar(256) NOT NULL\n" +
	") ENGINE=InnoDB;\n" +
	"INSERT INTO `template_inbounds_association` VALUES (1,'VLESS TCP');\n" +
	"\n" +
	"CREATE TABLE `next_plans` (\n" +
	"  `id` int NOT NULL AUTO_INCREMENT,\n" +
	"  `user_id` int NOT NULL,\n" +
	"  `data_limit` bigint NOT NULL,\n" +
	"  `expire` int DEFAULT NULL,\n" +
	"  `add_remaining_traffic` tinyint(1) NOT NULL DEFAULT '0',\n" +
	"  `fire_on_either` tinyint(1) NOT NULL DEFAULT '0',\n" +
	"  PRIMARY KEY (`id`)\n" +
	") ENGINE=InnoDB;\n" +
	"INSERT INTO `next_plans` VALUES (1,1,20000000000,NULL,1,1);\n"

func TestFromMarzbanMySQLDump(t *testing.T) {
	dump, err := ParseMySQLDump(strings.NewReader(marzbanFixture))
	if err != nil {
		t.Fatalf("ParseMySQLDump: %v", err)
	}
	data := FromMarzbanMySQLDump(dump)

	if len(data.Admins) != 1 || data.Admins[0].Username != "root" || !data.Admins[0].IsSudo {
		t.Fatalf("Admins = %+v", data.Admins)
	}
	if len(data.Inbounds) != 2 {
		t.Fatalf("Inbounds = %+v", data.Inbounds)
	}

	if len(data.Users) != 2 {
		t.Fatalf("Users = %+v", data.Users)
	}
	alice := data.Users[0]
	if alice.Username != "alice" || alice.SourceAdminID == nil || *alice.SourceAdminID != 1 {
		t.Errorf("alice = %+v, want admin_id resolved to 1", alice)
	}
	if len(alice.Proxies) != 1 || alice.Proxies[0].Type != "vless" {
		t.Fatalf("alice.Proxies = %+v, want one vless proxy (VLESS -> vless)", alice.Proxies)
	}
	if got := string(alice.Proxies[0].Settings); !strings.Contains(got, "xtls-rprx-vision") {
		t.Errorf("alice proxy settings = %q, want the raw JSON preserved", got)
	}
	if excl := alice.Proxies[0].ExcludedInbounds; len(excl) != 1 || excl[0] != "Nonexistent Tag" {
		t.Errorf("alice.Proxies[0].ExcludedInbounds = %v, want [\"Nonexistent Tag\"]", excl)
	}

	orphan := data.Users[1]
	if orphan.SourceAdminID != nil {
		t.Errorf("orphan.SourceAdminID = %v, want nil (admin_id 99 doesn't exist)", *orphan.SourceAdminID)
	}
	if len(orphan.Proxies) != 0 {
		t.Errorf("orphan.Proxies = %+v, want none (its proxy has an unrecognized type and should be dropped with a warning)", orphan.Proxies)
	}

	// The second proxy (user 2, type 'Weird') should be dropped with a
	// warning, not silently imported as an empty-string type that would
	// fail the new schema's CHECK constraint at insert time.
	foundUnrecognizedTypeWarning := false
	foundOrphanAdminWarning := false
	foundGhostHostWarning := false
	foundGhostExcludeWarning := false
	foundInboundPlaceholderWarning := false
	for _, w := range data.Warnings {
		switch {
		case strings.Contains(w, "unrecognized type"):
			foundUnrecognizedTypeWarning = true
		case strings.Contains(w, "orphan") && strings.Contains(w, "admin_id 99"):
			foundOrphanAdminWarning = true
		case strings.Contains(w, "ghost") && strings.Contains(w, "unknown inbound tag"):
			foundGhostHostWarning = true
		case strings.Contains(w, "excludes unknown inbound tag"):
			foundGhostExcludeWarning = true
		case strings.Contains(w, "placeholder protocol"):
			foundInboundPlaceholderWarning = true
		}
	}
	if !foundUnrecognizedTypeWarning {
		t.Error("expected a warning about the unrecognized proxy type 'Weird'")
	}
	if !foundOrphanAdminWarning {
		t.Error("expected a warning about user 'orphan' referencing a nonexistent admin_id")
	}
	if !foundGhostHostWarning {
		t.Error("expected a warning about host 'ghost' targeting a nonexistent inbound tag")
	}
	if !foundGhostExcludeWarning {
		t.Error("expected a warning about an exclusion referencing an unknown inbound tag")
	}
	if !foundInboundPlaceholderWarning {
		t.Error("expected a warning that imported inbounds need protocol configured by hand")
	}

	if len(data.Hosts) != 2 || data.Hosts[0].Remark != "{USERNAME}" {
		t.Fatalf("Hosts = %+v", data.Hosts)
	}

	if len(data.UserTemplates) != 1 || len(data.UserTemplates[0].InboundTags) != 1 {
		t.Fatalf("UserTemplates = %+v", data.UserTemplates)
	}

	if len(data.NextPlans) != 1 || data.NextPlans[0].SourceUserID != 1 || !data.NextPlans[0].FireOnEither {
		t.Fatalf("NextPlans = %+v", data.NextPlans)
	}
}

// TestFromMarzbanMySQLDumpRealSample runs the full parse+interpret
// pipeline against a real sample dump if LEGACY_SAMPLE_MYSQL_DUMP is set
// (see mysqldump_test.go's TestParseRealSampleDump for why this isn't a
// committed fixture) - proves the interpreter doesn't choke on real,
// large-scale, messy production data, without asserting on or logging any
// actual real value.
func TestFromMarzbanMySQLDumpRealSample(t *testing.T) {
	path := os.Getenv("LEGACY_SAMPLE_MYSQL_DUMP")
	if path == "" {
		t.Skip("LEGACY_SAMPLE_MYSQL_DUMP not set, skipping real-sample interpret test")
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
	data := FromMarzbanMySQLDump(dump)

	t.Logf("admins=%d users=%d hosts=%d inbounds=%d templates=%d nextPlans=%d warnings=%d",
		len(data.Admins), len(data.Users), len(data.Hosts), len(data.Inbounds),
		len(data.UserTemplates), len(data.NextPlans), len(data.Warnings))

	if len(data.Admins) == 0 || len(data.Users) == 0 {
		t.Fatal("expected a real sample to produce at least one admin and one user")
	}
	seenUsernames := map[string]bool{}
	for _, u := range data.Users {
		if u.Username == "" {
			t.Error("a user was imported with an empty username")
		}
		if seenUsernames[u.Username] {
			t.Errorf("duplicate username in imported data: %q", u.Username)
		}
		seenUsernames[u.Username] = true
		switch u.Status {
		case "on_hold", "active", "limited", "expired", "disabled":
		default:
			t.Errorf("user %q has unexpected status %q", u.Username, u.Status)
		}
		for _, p := range u.Proxies {
			switch p.Type {
			case "vmess", "vless", "trojan", "shadowsocks":
			default:
				t.Errorf("user %q has proxy with unexpected type %q", u.Username, p.Type)
			}
		}
	}
	for _, w := range data.Warnings {
		t.Logf("warning: %s", w)
	}
}
