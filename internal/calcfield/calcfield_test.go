package calcfield

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// testOperand reads columns as themselves and every other key as a bound
// attribute path, like the filter package does.
func testOperand(key string) (string, []any, error) {
	switch key {
	case "duration_us", "name", "http_status":
		return key, nil, nil
	}
	return `(CASE WHEN json_valid(attributes) THEN json_extract(attributes, ?) END)`, []any{`$."` + key + `"`}, nil
}

func isBuiltin(key string) bool {
	return key == "duration_us" || key == "name" || strings.HasPrefix(key, "attributes.")
}

func evalDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE spans (name TEXT, duration_us INTEGER, http_status INTEGER, attributes TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO spans VALUES ('GET /a', 2500, 200, '{"http.route":"/a/{id}","n":4,"s":"X","user-id":"u1"}'),
		('GET /b', 100, 500, '{"n":0}')`); err != nil {
		t.Fatal(err)
	}
	return db
}

// evalAll compiles src and returns the value per span, ordered by duration desc.
func evalAll(t *testing.T, db *sql.DB, src string) []any {
	t.Helper()
	q, args, err := Compile(src, testOperand)
	if err != nil {
		t.Fatalf("compile %q: %v", src, err)
	}
	rows, err := db.Query("SELECT "+q+" FROM spans ORDER BY duration_us DESC", args...)
	if err != nil {
		t.Fatalf("query %q (%s): %v", src, q, err)
	}
	defer rows.Close()
	var out []any
	for rows.Next() {
		var v any
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	return out
}

func TestEvaluation(t *testing.T) {
	db := evalDB(t)
	tests := []struct {
		src  string
		want []any
	}{
		{"duration_us / 1000", []any{2.5, 0.1}},
		{"duration_us * 2 + 1", []any{int64(5001), int64(201)}},
		{"duration_us % 1000", []any{int64(500), int64(100)}},
		{"-duration_us", []any{int64(-2500), int64(-100)}},
		{"coalesce(http.route, name)", []any{"/a/{id}", "GET /b"}},
		{"lower(s)", []any{"x", nil}},
		{"concat(name, ' ', n)", []any{"GET /a 4", "GET /b 0"}},
		{"if(duration_us > 1000, 'slow', 'fast')", []any{"slow", "fast"}},
		{"if(http_status >= 500 or n = 4, 1, 0)", []any{int64(1), int64(1)}},
		{"if(not (n = 0), 'nz', 'z')", []any{"nz", "z"}},
		{"duration_us / n", []any{625.0, nil}},
		{"`user-id`", []any{"u1", nil}},
		{"n <> 4 and n != 5", []any{int64(0), int64(1)}},
		{"(1 + 2) * 3", []any{int64(9), int64(9)}},
		{"1.5 + 1", []any{2.5, 2.5}},
	}
	for _, tc := range tests {
		t.Run(tc.src, func(t *testing.T) {
			got := evalAll(t, db, tc.src)
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	tests := map[string]string{
		"empty":          "  ",
		"unknown func":   "sum(n)",
		"too few args":   "if(n, 1)",
		"too many args":  "lower(a, b)",
		"dangling op":    "n +",
		"unclosed paren": "(n + 1",
		"extra paren":    "n + 1)",
		"reserved":       "and + 1",
		"bad char":       "n ; 1",
		"double quote":   `"x"`,
		"unterminated":   "'abc",
		"backslash":      "`a\\b`",
		"empty ident":    "``",
		"number suffix":  "12abc",
		"two compares":   "a = b = c",
		"nul":            "'a\x00b'",
	}
	for name, src := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(src)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("Parse(%q) = %v, want ErrInvalid", src, err)
			}
		})
	}
}

func TestCaps(t *testing.T) {
	if _, err := Parse(strings.Repeat("1+", MaxLength) + "1"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("over length: %v", err)
	}
	deep := strings.Repeat("(", MaxDepth+1) + "1" + strings.Repeat(")", MaxDepth+1)
	if _, err := Parse(deep); !errors.Is(err, ErrInvalid) {
		t.Fatalf("over depth: %v", err)
	}
	ok := strings.Repeat("(", MaxDepth-2) + "1" + strings.Repeat(")", MaxDepth-2)
	if _, err := Parse(ok); err != nil {
		t.Fatalf("within depth: %v", err)
	}
	if _, err := Parse(strings.Repeat("-", MaxDepth+2) + "1"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unary depth: %v", err)
	}
	if _, err := Parse(strings.Repeat("not ", MaxDepth+2) + "1"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("not depth: %v", err)
	}
	many := strings.TrimSuffix(strings.Repeat("a+", MaxNodes), "+")
	if len(many) <= MaxLength {
		if _, err := Parse(many); !errors.Is(err, ErrInvalid) {
			t.Fatalf("over terms: %v", err)
		}
	}
	args := "coalesce(" + strings.TrimSuffix(strings.Repeat("a,", MaxCallArgs+1), ",") + ")"
	if _, err := Parse(args); !errors.Is(err, ErrInvalid) {
		t.Fatalf("over args: %v", err)
	}
}

// TestInjection feeds hostile text through every place a user controls and
// checks that none of it reaches the SQL, and that the database stays intact.
func TestInjection(t *testing.T) {
	db := evalDB(t)
	hostile := []string{
		"'; DROP TABLE spans; --",
		"x' OR '1'='1",
		"/* c */ 1",
		"1; DELETE FROM spans",
		"\"; DROP TABLE spans; --",
		"'--'",
	}
	var exprs []string
	for _, h := range hostile {
		lit := "'" + strings.ReplaceAll(h, "'", "''") + "'"
		exprs = append(exprs, lit, "concat(name, "+lit+")", "if(name = "+lit+", 1, 2)", "coalesce(n, "+lit+")")
	}
	for _, e := range exprs {
		q, args, err := Compile(e, testOperand)
		if err != nil {
			continue
		}
		for _, bad := range []string{"DROP", "DELETE", "/*", "--", ";", "1'='1"} {
			if strings.Contains(q, bad) {
				t.Fatalf("%q put %q in the SQL: %s", e, bad, q)
			}
		}
		rows, err := db.Query("SELECT "+q+" FROM spans", args...)
		if err != nil {
			t.Fatalf("%q: %v", e, err)
		}
		rows.Close()
	}
	// Identifiers: a hostile backtick name is bound as a path, never spliced.
	for _, name := range []string{"a'; DROP TABLE spans; --", "x') OR 1=1 --", "a/*b*/"} {
		src := "`" + name + "`"
		q, args, err := Compile(src, testOperand)
		if err != nil {
			t.Fatalf("%q: %v", src, err)
		}
		if strings.Contains(q, "DROP") || strings.Contains(q, "--") || strings.Contains(q, "/*") || strings.Contains(q, name) {
			t.Fatalf("identifier reached the SQL: %s", q)
		}
		if len(args) != 1 || !strings.Contains(args[0].(string), name) {
			t.Fatalf("identifier not bound: %v", args)
		}
		rows, err := db.Query("SELECT "+q+" FROM spans", args...)
		if err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	// Characters outside the grammar never get as far as the compiler.
	for _, src := range []string{"1; 2", "n /* c */", "n\x00", "[n]", "{n}", "n | 1", "n & 1", "n ? 1", "$n", "n:1", "n!"} {
		if _, err := Parse(src); err == nil {
			t.Fatalf("Parse(%q) succeeded", src)
		}
	}
	// "--" is minus then negation, and the generated SQL spaces it out.
	q, _, err := Compile("n --n - -(-n)", testOperand)
	if err != nil || strings.Contains(q, "--") {
		t.Fatalf("comment marker in SQL: %q %v", q, err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM spans`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("spans table damaged: n=%d err=%v", n, err)
	}
}

func TestGeneratedSQLHasNoUserText(t *testing.T) {
	src := "concat(name, 'secret-literal', `secret.key`, 42)"
	q, args, err := Compile(src, testOperand)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(q, "secret") || strings.Contains(q, "42") {
		t.Fatalf("user text in SQL: %s", q)
	}
	if want := strings.Count(q, "?"); want != len(args) {
		t.Fatalf("%d placeholders for %d args", want, len(args))
	}
}

func TestIdents(t *testing.T) {
	e, err := Parse("coalesce(http.route, name) = a and name <> `b-c`")
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(e.Idents()); got != "[http.route name a b-c]" {
		t.Fatalf("idents %s", got)
	}
}

func newSet(t *testing.T, fields ...Field) *Set {
	t.Helper()
	return NewSet(fields, testOperand, isBuiltin)
}

func TestSetNested(t *testing.T) {
	db := evalDB(t)
	s := newSet(t,
		Field{"ms", "duration_us / 1000"},
		Field{"bucket", "if(ms >= 1, 'slow', 'fast')"},
	)
	q, args, ok, err := s.Lookup("bucket")
	if err != nil || !ok {
		t.Fatalf("lookup: ok=%v err=%v", ok, err)
	}
	rows, err := db.Query("SELECT "+q+" FROM spans ORDER BY duration_us DESC", args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		got = append(got, v)
	}
	if fmt.Sprint(got) != "[slow fast]" {
		t.Fatalf("got %v", got)
	}
	if _, _, ok, _ := s.Lookup("nope"); ok {
		t.Fatal("unknown name resolved")
	}
}

func TestSetCycle(t *testing.T) {
	for name, fields := range map[string][]Field{
		"self":   {{"a", "a + 1"}},
		"pair":   {{"a", "b + 1"}, {"b", "a + 1"}},
		"triple": {{"a", "b"}, {"b", "c"}, {"c", "coalesce(a, 1)"}},
	} {
		t.Run(name, func(t *testing.T) {
			s := newSet(t, fields...)
			if err := s.Validate(); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "cycle") {
				t.Fatalf("want cycle error, got %v", err)
			}
		})
	}
}

func TestSetAttributePrefixBreaksSelfReference(t *testing.T) {
	s := newSet(t, Field{"http.route", "coalesce(attributes.http.route, name)"})
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestSetBuiltinNeverShadowed(t *testing.T) {
	s := newSet(t, Field{"name", "1"})
	if s.Has("name") {
		t.Fatal("a field named like a column must not resolve")
	}
}

func TestSetExpansionCap(t *testing.T) {
	fields := []Field{{"f0", "n + n"}}
	for i := 1; i < 20; i++ {
		fields = append(fields, Field{fmt.Sprintf("f%d", i), fmt.Sprintf("f%d + f%d", i-1, i-1)})
	}
	s := newSet(t, fields...)
	_, _, _, err := s.Lookup("f19")
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("want expansion error, got %v", err)
	}
}

func TestSetBadStoredExpressionFailsOnlyItsUsers(t *testing.T) {
	s := newSet(t, Field{"a", "n ; 1"}, Field{"b", "a + 1"}, Field{"ok", "n + 1"})
	if _, _, _, err := s.Lookup("a"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a: %v", err)
	}
	if _, _, _, err := s.Lookup("b"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("b uses a: %v", err)
	}
	if _, _, ok, err := s.Lookup("ok"); err != nil || !ok {
		t.Fatalf("ok: %v", err)
	}
}
