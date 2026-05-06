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
	i := 0
	n := len(sql)

	for i < n {
		ch := sql[i]

		// Single-quoted string literal → ?
		if ch == '\'' {
			i++
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
			continue
		}

		// Postgres $N parameter → ?
		if ch == '$' && i+1 < n && sql[i+1] >= '1' && sql[i+1] <= '9' {
			i++
			for i < n && sql[i] >= '0' && sql[i] <= '9' {
				i++
			}
			// Handle ::TYPE cast after parameter
			if i+1 < n && sql[i] == ':' && sql[i+1] == ':' {
				i += 2
				for i < n && (isIdentChar(sql[i])) {
					i++
				}
			}
			b.WriteByte('?')
			continue
		}

		// Numeric literal (not part of identifier) → ?
		if (ch >= '0' && ch <= '9') && (i == 0 || !isIdentChar(sql[i-1])) {
			for i < n && ((sql[i] >= '0' && sql[i] <= '9') || sql[i] == '.') {
				i++
			}
			b.WriteByte('?')
			continue
		}

		// Whitespace → single space
		if ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' {
			i++
			for i < n && (sql[i] == ' ' || sql[i] == '\t' || sql[i] == '\n' || sql[i] == '\r') {
				i++
			}
			b.WriteByte(' ')
			continue
		}

		// Double-quoted identifier — preserve as-is
		if ch == '"' {
			b.WriteByte(ch)
			i++
			for i < n && sql[i] != '"' {
				b.WriteByte(sql[i])
				i++
			}
			if i < n {
				b.WriteByte(sql[i])
				i++
			}
			continue
		}

		// Regular character — lowercase
		b.WriteRune(unicode.ToLower(rune(ch)))
		i++
	}

	return strings.TrimSpace(b.String())
}

func isIdentChar(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_' || (ch >= '0' && ch <= '9')
}
