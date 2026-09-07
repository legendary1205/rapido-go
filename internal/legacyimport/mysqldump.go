// Package legacyimport parses backups from other panels (starting with
// legacy Marzban/Rapido MySQL dumps) into a common intermediate shape that
// internal/httpapi can load into this panel's real Postgres schema. Every
// format-specific parser here is deliberately dependency-free - no live
// source database server is ever needed, matching the design in the
// Phase 8.2 plan: a mysqldump's own text output is mechanical enough to
// parse directly.
package legacyimport

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// MySQLTable is one CREATE TABLE's parsed shape plus every row mysqldump's
// INSERT statements produced for it, in file order.
type MySQLTable struct {
	Name    string
	Columns []string
	Rows    [][]any // each row has len(Columns) values: nil, string, int64, or float64
}

// MySQLDump is every table found in one mysqldump text stream, keyed by
// table name. A table mysqldump's own CREATE TABLE never mentions (or
// whose CREATE TABLE exists but has zero data rows) is simply absent or
// present-with-no-rows - callers should treat both as "nothing to import
// for that table", not an error, since a real legacy panel's dump won't
// contain every table this codebase knows how to interpret.
type MySQLDump struct {
	Tables map[string]*MySQLTable
}

// Row returns table's parsed rows as column-name-keyed maps, or (nil,
// false) if the table wasn't present in the dump at all - the shape every
// per-panel interpreter (marzban.go and future ones) actually consumes.
func (d *MySQLDump) Row(table string) ([]map[string]any, bool) {
	t, ok := d.Tables[table]
	if !ok {
		return nil, false
	}
	rows := make([]map[string]any, 0, len(t.Rows))
	for _, r := range t.Rows {
		m := make(map[string]any, len(t.Columns))
		for i, col := range t.Columns {
			if i < len(r) {
				m[col] = r[i]
			}
		}
		rows = append(rows, m)
	}
	return rows, true
}

// ParseMySQLDump reads a real mysqldump text stream (the default
// --extended-insert output every one of this project's target legacy
// panels produces: a CREATE TABLE per table, each followed by one or more
// "INSERT INTO `t` VALUES (...),(...),...;" statements, normally one huge
// line per INSERT). Uses bufio.Reader.ReadString rather than bufio.Scanner
// specifically because Scanner has a fixed max token size unsuitable for
// an arbitrarily large single-line INSERT statement; ReadString has no
// such cap.
func ParseMySQLDump(r io.Reader) (*MySQLDump, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	dump := &MySQLDump{Tables: map[string]*MySQLTable{}}

	for {
		line, readErr := br.ReadString('\n')
		trimmed := strings.TrimSpace(line)

		switch {
		case strings.HasPrefix(trimmed, "CREATE TABLE `"):
			name, cols, err := parseCreateTable(trimmed, br)
			if err != nil {
				return nil, fmt.Errorf("CREATE TABLE: %w", err)
			}
			dump.Tables[name] = &MySQLTable{Name: name, Columns: cols}

		case strings.HasPrefix(trimmed, "INSERT INTO `"):
			name, rest, ok := parseInsertHeader(trimmed)
			if ok {
				tbl := dump.Tables[name]
				rows, err := parseValuesTuples(rest)
				if err != nil {
					return nil, fmt.Errorf("INSERT INTO `%s`: %w", name, err)
				}
				if tbl != nil {
					tbl.Rows = append(tbl.Rows, rows...)
				}
				// A table with no known CREATE TABLE (shouldn't happen in a
				// well-formed dump, but tolerated) just has its data
				// silently skipped rather than erroring the whole parse.
			}
		}

		if readErr != nil {
			if readErr == io.EOF {
				return dump, nil
			}
			return nil, readErr
		}
	}
}

// parseCreateTable reads column definition lines from br until the closing
// ") ENGINE=..." line, skipping PRIMARY KEY/UNIQUE KEY/KEY/CONSTRAINT
// lines - only `column_name` type... lines contribute a column, in the
// exact order mysqldump declared them (which is what a column-less
// "INSERT INTO t VALUES (...)" relies on positionally).
func parseCreateTable(firstLine string, br *bufio.Reader) (string, []string, error) {
	name, ok := extractBacktickName(firstLine, "CREATE TABLE `")
	if !ok {
		return "", nil, fmt.Errorf("malformed CREATE TABLE line: %q", firstLine)
	}

	var cols []string
	for {
		line, err := br.ReadString('\n')
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			if strings.HasPrefix(trimmed, ")") {
				return name, cols, nil
			}
			if strings.HasPrefix(trimmed, "`") {
				col, ok := extractBacktickName(trimmed, "`")
				if ok {
					cols = append(cols, col)
				}
			}
			// PRIMARY KEY/UNIQUE KEY/KEY/CONSTRAINT lines: not a column, skip.
		}
		if err != nil {
			if err == io.EOF {
				return "", nil, fmt.Errorf("unexpected EOF inside CREATE TABLE `%s`", name)
			}
			return "", nil, err
		}
	}
}

// extractBacktickName pulls the backtick-quoted identifier immediately
// after prefix out of s, e.g. extractBacktickName("`username` varchar...",
// "`") -> "username", true.
func extractBacktickName(s, prefix string) (string, bool) {
	idx := strings.Index(s, prefix)
	if idx == -1 {
		return "", false
	}
	rest := s[idx+len(prefix):]
	end := strings.IndexByte(rest, '`')
	if end == -1 {
		return "", false
	}
	return rest[:end], true
}

// parseInsertHeader splits "INSERT INTO `table` VALUES (...)...;" into the
// table name and the remainder starting right after "VALUES ".
func parseInsertHeader(line string) (table, rest string, ok bool) {
	const p = "INSERT INTO `"
	if !strings.HasPrefix(line, p) {
		return "", "", false
	}
	afterP := line[len(p):]
	end := strings.IndexByte(afterP, '`')
	if end == -1 {
		return "", "", false
	}
	table = afterP[:end]
	tail := afterP[end+1:]
	valuesIdx := strings.Index(tail, "VALUES")
	if valuesIdx == -1 {
		return "", "", false
	}
	return table, tail[valuesIdx+len("VALUES"):], true
}

// parseValuesTuples parses mysqldump's "(v1,v2,'v3'),(v4,v5,'v6');" value
// list into rows of Go values (nil for SQL NULL, string for a quoted
// value with backslash escapes resolved, int64/float64 for a bare numeric
// literal). This is a small hand-rolled scanner, not a general SQL parser -
// it only needs to understand exactly what mysqldump itself ever emits
// here.
func parseValuesTuples(s string) ([][]any, error) {
	var tuples [][]any
	i, n := 0, len(s)
	skipSpace := func() {
		for i < n && (s[i] == ' ' || s[i] == '\t' || s[i] == '\r' || s[i] == '\n') {
			i++
		}
	}

	for {
		skipSpace()
		if i >= n || s[i] == ';' {
			return tuples, nil
		}
		if s[i] != '(' {
			return nil, fmt.Errorf("expected '(' at offset %d, found %q", i, s[i])
		}
		i++ // consume '('

		var row []any
		for {
			skipSpace()
			if i >= n {
				return nil, fmt.Errorf("unexpected end of input inside a value tuple")
			}
			if s[i] == ')' {
				i++
				break
			}
			if s[i] == '\'' {
				val, newI, err := parseQuotedString(s, i)
				if err != nil {
					return nil, err
				}
				row = append(row, val)
				i = newI
			} else {
				start := i
				for i < n && s[i] != ',' && s[i] != ')' {
					i++
				}
				row = append(row, parseBareLiteral(strings.TrimSpace(s[start:i])))
			}
			skipSpace()
			if i < n && s[i] == ',' {
				i++
				continue
			}
			if i < n && s[i] == ')' {
				i++
				break
			}
		}
		tuples = append(tuples, row)

		skipSpace()
		if i < n && s[i] == ',' {
			i++
			continue
		}
		if i < n && s[i] == ';' {
			i++
			return tuples, nil
		}
		if i >= n {
			return tuples, nil
		}
	}
}

// parseQuotedString parses a MySQL single-quoted string literal starting
// at s[start] (which must be '\''), handling both backslash escapes
// (mysqldump's default) and the doubled-quote '' escape, and returns the
// unescaped value plus the offset right after the closing quote.
func parseQuotedString(s string, start int) (string, int, error) {
	i, n := start+1, len(s)
	var sb strings.Builder
	for i < n {
		c := s[i]
		if c == '\\' && i+1 < n {
			switch s[i+1] {
			case '\'':
				sb.WriteByte('\'')
			case '"':
				sb.WriteByte('"')
			case '\\':
				sb.WriteByte('\\')
			case 'n':
				sb.WriteByte('\n')
			case 'r':
				sb.WriteByte('\r')
			case 't':
				sb.WriteByte('\t')
			case '0':
				sb.WriteByte(0)
			case 'Z':
				sb.WriteByte(26)
			default:
				sb.WriteByte(s[i+1])
			}
			i += 2
			continue
		}
		if c == '\'' {
			if i+1 < n && s[i+1] == '\'' {
				sb.WriteByte('\'')
				i += 2
				continue
			}
			return sb.String(), i + 1, nil
		}
		sb.WriteByte(c)
		i++
	}
	return "", 0, fmt.Errorf("unterminated quoted string starting at offset %d", start)
}

// parseBareLiteral interprets a non-quoted value token: SQL NULL, an
// integer, a float, or (defensively, should not occur in a well-formed
// dump) the raw token text unchanged.
func parseBareLiteral(tok string) any {
	if tok == "NULL" {
		return nil
	}
	if iv, err := strconv.ParseInt(tok, 10, 64); err == nil {
		return iv
	}
	if fv, err := strconv.ParseFloat(tok, 64); err == nil {
		return fv
	}
	return tok
}
