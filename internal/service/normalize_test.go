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
	}

	for _, tt := range tests {
		got := NormalizeSQL(tt.input)
		if got != tt.want {
			t.Errorf("NormalizeSQL(%q)\n  got  %q\n  want %q", tt.input, got, tt.want)
		}
	}
}
