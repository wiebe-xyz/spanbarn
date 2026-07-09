package service

import (
	"strings"
	"unicode"
)

// NormalizeSQL strips parameter values from a SQL statement, producing a
// canonical query pattern suitable for grouping. It replaces:
//   - Postgres $N parameters with ?
//   - Single-quoted string literals with ?
//   - Numeric literals in value positions with ?
//   - Collapses whitespace
func NormalizeSQL(sql string) string {
	if sql == "" {
		return ""
	}

	var b strings.Builder
	b.Grow(len(sql))
	i, n := 0, len(sql)

	for i < n {
		switch ch := sql[i]; {
		case ch == '\'':
			i = scanStringLiteral(sql, i, &b)
		case ch == '$' && i+1 < n && sql[i+1] >= '1' && sql[i+1] <= '9':
			i = scanPgParam(sql, i, &b)
		case ch >= '0' && ch <= '9' && (i == 0 || !isIdentChar(sql[i-1])):
			i = scanNumber(sql, i, &b)
		case isSpace(ch):
			i = scanWhitespace(sql, i, &b)
		case ch == '"':
			i = scanQuotedIdent(sql, i, &b)
		default:
			b.WriteRune(unicode.ToLower(rune(ch)))
			i++
		}
	}

	return strings.TrimSpace(b.String())
}

// Each scanXxx consumes one token starting at i, writes its canonical form to b,
// and returns the index just past the token.

// scanStringLiteral collapses a single-quoted string literal (with ” escapes) to ?.
func scanStringLiteral(sql string, i int, b *strings.Builder) int {
	n := len(sql)
	i++ // opening quote
	for i < n {
		if sql[i] == '\'' {
			i++
			if i < n && sql[i] == '\'' {
				i++ // escaped quote ''
				continue
			}
			break
		}
		i++
	}
	b.WriteByte('?')
	return i
}

// scanPgParam collapses a Postgres $N parameter (with optional ::TYPE cast) to ?.
func scanPgParam(sql string, i int, b *strings.Builder) int {
	n := len(sql)
	i++ // '$'
	for i < n && sql[i] >= '0' && sql[i] <= '9' {
		i++
	}
	if i+1 < n && sql[i] == ':' && sql[i+1] == ':' {
		i += 2
		for i < n && isIdentChar(sql[i]) {
			i++
		}
	}
	b.WriteByte('?')
	return i
}

// scanNumber collapses a numeric literal in value position to ?.
func scanNumber(sql string, i int, b *strings.Builder) int {
	n := len(sql)
	for i < n && ((sql[i] >= '0' && sql[i] <= '9') || sql[i] == '.') {
		i++
	}
	b.WriteByte('?')
	return i
}

// scanWhitespace collapses a whitespace run to a single space.
func scanWhitespace(sql string, i int, b *strings.Builder) int {
	n := len(sql)
	for i < n && isSpace(sql[i]) {
		i++
	}
	b.WriteByte(' ')
	return i
}

// scanQuotedIdent preserves a double-quoted identifier verbatim.
func scanQuotedIdent(sql string, i int, b *strings.Builder) int {
	n := len(sql)
	b.WriteByte(sql[i]) // opening quote
	i++
	for i < n && sql[i] != '"' {
		b.WriteByte(sql[i])
		i++
	}
	if i < n {
		b.WriteByte(sql[i]) // closing quote
		i++
	}
	return i
}

func isSpace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r'
}

func isIdentChar(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_' || (ch >= '0' && ch <= '9')
}
