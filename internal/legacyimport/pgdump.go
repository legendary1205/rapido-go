package legacyimport

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// PgTable is one public-schema table's rows from a plain-format pg_dump,
// in file order: the column list of its COPY header, then one entry per
// data line. A nil value is SQL NULL; everything else is the column's text
// form exactly as Postgres prints it ("t"/"f" booleans, timestamptz with its
// offset, JSON as text).
type PgTable struct {
	Name    string
	Columns []string
	Rows    [][]*string
}

// PgDump is what ParsePgDump read: the public tables the dump creates (its
// schema signature, whatever the data) and the rows of the tables the
// caller asked for.
type PgDump struct {
	Created map[string]bool
	Tables  map[string]*PgTable
}

// Rows returns table's rows as column-name-keyed maps, or (nil, false) when
// the dump carried no COPY block for it.
func (d *PgDump) Rows(table string) ([]map[string]*string, bool) {
	t, ok := d.Tables[table]
	if !ok {
		return nil, false
	}
	out := make([]map[string]*string, 0, len(t.Rows))
	for _, r := range t.Rows {
		m := make(map[string]*string, len(t.Columns))
		for i, col := range t.Columns {
			if i < len(r) {
				m[col] = r[i]
			}
		}
		out = append(out, m)
	}
	return out, true
}

// ParsePgDump reads a plain-format (pg_dump -Fp, the default) dump without a
// live Postgres: every "CREATE TABLE public.<name>" is noted, and each
// "COPY public.<name> (...) FROM stdin;" block is decoded from Postgres's
// COPY text format (tab-separated, \N for NULL, backslash escapes) for the
// tables want accepts - want == nil keeps them all. Tables outside the
// public schema (TimescaleDB's _timescaledb_catalog.*, for one) are skipped,
// as is the rest of the SQL. bufio.Reader.ReadString rather than a Scanner,
// for the same reason as ParseMySQLDump: one row can be an arbitrarily long
// line (a core config carrying inline certificates).
func ParsePgDump(r io.Reader, want func(table string) bool) (*PgDump, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	dump := &PgDump{Created: map[string]bool{}, Tables: map[string]*PgTable{}}

	var copying *PgTable // non-nil inside a COPY block we keep
	skipping := false    // inside a COPY block we don't keep
	lineNo := 0
	for {
		line, readErr := br.ReadString('\n')
		if line != "" {
			lineNo++
			body := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")

			switch {
			case copying != nil || skipping:
				if body == `\.` {
					copying, skipping = nil, false
					break
				}
				if copying != nil {
					row, err := parseCopyRow(body, len(copying.Columns))
					if err != nil {
						return nil, fmt.Errorf("line %d (COPY %s): %w", lineNo, copying.Name, err)
					}
					copying.Rows = append(copying.Rows, row)
				}

			case strings.HasPrefix(body, "CREATE TABLE "):
				if name, ok := publicTableName(strings.TrimPrefix(body, "CREATE TABLE ")); ok {
					dump.Created[name] = true
				}

			case strings.HasPrefix(body, "COPY ") && strings.HasSuffix(body, " FROM stdin;"):
				name, cols, ok := parseCopyHeader(body)
				if !ok || (want != nil && !want(name)) {
					skipping = true
					break
				}
				t := &PgTable{Name: name, Columns: cols}
				dump.Tables[name] = t
				copying = t
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	if copying != nil || skipping {
		return nil, fmt.Errorf("the dump ends inside a COPY block - it is truncated")
	}
	return dump, nil
}

// publicTableName extracts <name> from "public.<name> ..." (either part may
// be double-quoted), and reports false for any other schema.
func publicTableName(s string) (string, bool) {
	schema, rest, ok := readIdent(s)
	if !ok || schema != "public" || !strings.HasPrefix(rest, ".") {
		return "", false
	}
	name, _, ok := readIdent(rest[1:])
	return name, ok
}

// parseCopyHeader parses `COPY public.users (id, username, ...) FROM stdin;`.
func parseCopyHeader(line string) (string, []string, bool) {
	rest := strings.TrimPrefix(line, "COPY ")
	schema, rest, ok := readIdent(rest)
	if !ok || schema != "public" || !strings.HasPrefix(rest, ".") {
		return "", nil, false
	}
	name, rest, ok := readIdent(rest[1:])
	if !ok {
		return "", nil, false
	}
	rest = strings.TrimSpace(rest)
	open, end := strings.IndexByte(rest, '('), strings.LastIndexByte(rest, ')')
	if open != 0 || end < 0 || !strings.HasSuffix(rest, "FROM stdin;") {
		return "", nil, false
	}
	var cols []string
	list := rest[1:end]
	for list != "" {
		list = strings.TrimLeft(list, " ")
		col, after, ok := readIdent(list)
		if !ok {
			return "", nil, false
		}
		cols = append(cols, col)
		list = strings.TrimPrefix(strings.TrimLeft(after, " "), ",")
	}
	return name, cols, true
}

// readIdent reads one SQL identifier off the front of s - bare, or
// double-quoted with "" as an escaped quote - and returns the rest.
func readIdent(s string) (string, string, bool) {
	if strings.HasPrefix(s, `"`) {
		var b strings.Builder
		for i := 1; i < len(s); i++ {
			if s[i] == '"' {
				if i+1 < len(s) && s[i+1] == '"' {
					b.WriteByte('"')
					i++
					continue
				}
				return b.String(), s[i+1:], true
			}
			b.WriteByte(s[i])
		}
		return "", "", false
	}
	end := 0
	for end < len(s) {
		c := s[end]
		if c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80 {
			end++
			continue
		}
		break
	}
	if end == 0 {
		return "", "", false
	}
	return s[:end], s[end:], true
}

// parseCopyRow splits one COPY text-format data line into its fields.
func parseCopyRow(line string, want int) ([]*string, error) {
	fields := strings.Split(line, "\t")
	if len(fields) != want {
		return nil, fmt.Errorf("%d fields, the header lists %d columns", len(fields), want)
	}
	row := make([]*string, len(fields))
	for i, f := range fields {
		if f == `\N` {
			continue
		}
		v, err := unescapeCopyField(f)
		if err != nil {
			return nil, err
		}
		row[i] = &v
	}
	return row, nil
}

// unescapeCopyField decodes COPY's backslash escapes: \b \f \n \r \t \v,
// \NNN (1-3 octal digits), \xHH (1-2 hex digits), and a backslash before
// any other character standing for that character itself (\\ is one \).
func unescapeCopyField(f string) (string, error) {
	if !strings.Contains(f, `\`) {
		return f, nil
	}
	var b strings.Builder
	b.Grow(len(f))
	for i := 0; i < len(f); i++ {
		c := f[i]
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		i++
		if i >= len(f) {
			return "", fmt.Errorf("field ends in a lone backslash")
		}
		switch e := f[i]; {
		case e == 'b':
			b.WriteByte('\b')
		case e == 'f':
			b.WriteByte('\f')
		case e == 'n':
			b.WriteByte('\n')
		case e == 'r':
			b.WriteByte('\r')
		case e == 't':
			b.WriteByte('\t')
		case e == 'v':
			b.WriteByte('\v')
		case e >= '0' && e <= '7':
			j := i
			for j < len(f) && j < i+3 && f[j] >= '0' && f[j] <= '7' {
				j++
			}
			v, _ := strconv.ParseUint(f[i:j], 8, 16)
			b.WriteByte(byte(v))
			i = j - 1
		case e == 'x' && i+1 < len(f) && isHexDigit(f[i+1]):
			j := i + 1
			for j < len(f) && j < i+3 && isHexDigit(f[j]) {
				j++
			}
			v, _ := strconv.ParseUint(f[i+1:j], 16, 8)
			b.WriteByte(byte(v))
			i = j - 1
		default:
			b.WriteByte(e)
		}
	}
	return b.String(), nil
}

func isHexDigit(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}
