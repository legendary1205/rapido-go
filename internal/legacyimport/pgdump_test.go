package legacyimport

import (
	"strings"
	"testing"
)

// copyBlock renders one COPY ... FROM stdin; block the way pg_dump writes it.
func copyBlock(table string, cols []string, rows ...[]string) string {
	var b strings.Builder
	b.WriteString("COPY " + table + " (" + strings.Join(cols, ", ") + ") FROM stdin;\n")
	for _, r := range rows {
		b.WriteString(strings.Join(r, "\t") + "\n")
	}
	b.WriteString("\\.\n")
	return b.String()
}

func TestParsePgDumpDecodesCopyTextFormat(t *testing.T) {
	dump := "--\n-- PostgreSQL database dump\n--\n\n" +
		"CREATE TABLE public.users (\n    id bigint NOT NULL\n);\n" +
		"CREATE TABLE public.\"Odd Name\" (\n    id bigint\n);\n" +
		"CREATE TABLE _timescaledb_catalog.chunk (\n    id integer\n);\n" +
		copyBlock("_timescaledb_catalog.metadata", []string{"key", "value"}, []string{"uuid", "x"}) +
		copyBlock("public.users", []string{"id", "note", `"user"`},
			[]string{"1", `tab\there`, `back\\slash`},
			[]string{"2", `\N`, `line\nbreak`},
			[]string{"3", `oct\101 hex\x42`, ``},
		) +
		copyBlock("public.\"Odd Name\"", []string{"id"}, []string{"9"}) +
		"ALTER TABLE ONLY public.users ADD CONSTRAINT pk_users PRIMARY KEY (id);\n"

	d, err := ParsePgDump(strings.NewReader(dump), nil)
	if err != nil {
		t.Fatalf("ParsePgDump: %v", err)
	}
	if !d.Created["users"] || !d.Created["Odd Name"] || d.Created["chunk"] {
		t.Errorf("created tables = %v, want users and \"Odd Name\" only (public schema)", d.Created)
	}
	if _, ok := d.Tables["metadata"]; ok {
		t.Error("a non-public COPY block was kept")
	}
	users := d.Tables["users"]
	if users == nil || len(users.Rows) != 3 {
		t.Fatalf("users = %+v, want 3 rows", users)
	}
	if got := users.Columns; strings.Join(got, ",") != "id,note,user" {
		t.Errorf("columns = %v, want id,note,user (quotes stripped)", got)
	}
	want := [][]string{
		{"1", "tab\there", `back\slash`},
		{"2", "<NULL>", "line\nbreak"},
		{"3", "octA hexB", ""},
	}
	for i, row := range users.Rows {
		for j, v := range row {
			got := "<NULL>"
			if v != nil {
				got = *v
			}
			if got != want[i][j] {
				t.Errorf("row %d col %d = %q, want %q", i, j, got, want[i][j])
			}
		}
	}
	if odd := d.Tables["Odd Name"]; odd == nil || len(odd.Rows) != 1 {
		t.Errorf("quoted table name not read: %+v", odd)
	}
}

func TestParsePgDumpWantFilterSkipsOtherTables(t *testing.T) {
	dump := copyBlock("public.node_user_usages", []string{"id"}, []string{"1"}, []string{"2"}) +
		copyBlock("public.users", []string{"id"}, []string{"5"})
	d, err := ParsePgDump(strings.NewReader(dump), func(t string) bool { return t == "users" })
	if err != nil {
		t.Fatalf("ParsePgDump: %v", err)
	}
	if _, ok := d.Tables["node_user_usages"]; ok {
		t.Error("unwanted table kept")
	}
	if len(d.Tables["users"].Rows) != 1 {
		t.Errorf("users rows = %d, want 1", len(d.Tables["users"].Rows))
	}
}

func TestParsePgDumpRejectsBrokenInput(t *testing.T) {
	truncated := "COPY public.users (id, username) FROM stdin;\n1\talice\n"
	if _, err := ParsePgDump(strings.NewReader(truncated), nil); err == nil {
		t.Error("a dump ending inside a COPY block was accepted")
	}
	mismatch := copyBlock("public.users", []string{"id", "username"}, []string{"1"})
	if _, err := ParsePgDump(strings.NewReader(mismatch), nil); err == nil {
		t.Error("a row with fewer fields than columns was accepted")
	}
}
