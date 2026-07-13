package service

import "testing"

func TestNormalizeSQL(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", ""},
		{
			"SELECT * FROM users WHERE id = $1 AND name = 'John'",
			"select * from users where id = ? and name = ?",
		},
		{
			"SELECT * FROM users WHERE id = $1::INTEGER",
			"select * from users where id = ?",
		},
		{
			"INSERT INTO logs (msg) VALUES (?)",
			"insert into logs (msg) values (?)",
		},
		{
			"UPDATE t SET x = 42, y = 'hello'",
			"update t set x = ?, y = ?",
		},
		{
			"SELECT * FROM t WHERE x = 'it''s escaped'",
			"select * from t where x = ?",
		},
		{
			"SELECT  col1,\n\tcol2  FROM  t",
			"select col1, col2 from t",
		},
		{
			`SELECT "Column" FROM "Table" WHERE id = $1`,
			`select "Column" from "Table" where id = ?`,
		},
		{
			"SELECT * FROM t WHERE val = 3.14",
			"select * from t where val = ?",
		},
		{
			"SELECT col1 FROM table_2 WHERE id = $1",
			"select col1 from table_2 where id = ?",
		},
		{
			"SELECT $1::VARCHAR, $2::INTEGER",
			"select ?, ?",
		},
		{
			// Variable-length IN lists collapse regardless of placeholder count.
			"DELETE FROM t WHERE id IN (?,?,?)",
			"delete from t where id in (?, …)",
		},
		{
			"DELETE FROM t WHERE id IN (?,?,?,?,?,?,?)",
			"delete from t where id in (?, …)",
		},
		{
			"SELECT * FROM t WHERE id IN ($1, $2, $3)",
			"select * from t where id in (?, …)",
		},
	}

	for _, tt := range tests {
		got := NormalizeSQL(tt.input)
		if got != tt.want {
			t.Errorf("NormalizeSQL(%q)\n  got  %q\n  want %q", tt.input, got, tt.want)
		}
	}
}

func TestCollapseParamLists(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", ""},
		{
			// The reported case: parameterized IN lists of differing length must
			// collapse to the same canonical form, with case preserved.
			"DELETE FROM spans_staging WHERE trace_id IN (?,?,?)",
			"DELETE FROM spans_staging WHERE trace_id IN (?, …)",
		},
		{
			"DELETE FROM spans_staging WHERE trace_id IN (?,?,?,?,?,?,?,?,?)",
			"DELETE FROM spans_staging WHERE trace_id IN (?, …)",
		},
		{
			// Postgres-style numbered placeholders collapse too.
			"SELECT * FROM t WHERE id IN ($1, $2, $3)",
			"SELECT * FROM t WHERE id IN (?, …)",
		},
		{
			// A lone placeholder is left untouched.
			"SELECT * FROM t WHERE id = ?",
			"SELECT * FROM t WHERE id = ?",
		},
		{
			"SELECT * FROM t WHERE id IN (?)",
			"SELECT * FROM t WHERE id IN (?)",
		},
		{
			// Bare projection list (not inside parentheses) keeps its exact arity.
			"SELECT ?, ? FROM t",
			"SELECT ?, ? FROM t",
		},
		{
			// Non-SQL text is preserved verbatim, case intact.
			"GET /api/v1/users/:id",
			"GET /api/v1/users/:id",
		},
		{
			// Multiple lists each collapse independently.
			"SELECT * FROM a WHERE x IN (?,?) AND y IN (?,?,?)",
			"SELECT * FROM a WHERE x IN (?, …) AND y IN (?, …)",
		},
	}

	for _, tt := range tests {
		if got := CollapseParamLists(tt.input); got != tt.want {
			t.Errorf("CollapseParamLists(%q)\n  got  %q\n  want %q", tt.input, got, tt.want)
		}
	}
}
