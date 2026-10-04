package filter

// Helpers for grouping. The group-by query reads the same keys as a filter, so
// it shares key resolution with Compile.

// ValidateKey checks that key can name a span column or attribute.
func ValidateKey(key string) error {
	c := Node{Key: key, Op: OpExists}
	return validateCondition(&c)
}

// TextSQL returns SQL that reads key as text, with its arguments. A span column
// reads as itself, an attribute as its JSON value (booleans as true/false) and
// NULL when the attribute is missing.
func TextSQL(key string) (string, []any) {
	return resolveBuiltinOrAttr(key).text()
}

// TextSQLWith is TextSQL with a Resolver, so a key can name a calculated field.
func TextSQLWith(key string, r Resolver) (string, []any, error) {
	o, err := resolve(key, r)
	if err != nil {
		return "", nil, err
	}
	s, a := o.text()
	return s, a, nil
}

// GroupCondition is the filter condition that selects spans whose key reads as
// value. An empty value selects spans that do not have the key, which is how
// a group row reads once the caller coalesces a missing key to "".
func GroupCondition(key, value string) Node {
	if value == "" {
		return Node{Key: key, Op: OpNotExists}
	}
	return Node{Key: key, Op: OpEq, Value: Scalar(value)}
}

// Conjoin returns base AND conds as one valid expression. It returns false when
// the result would nest groups two levels deep, which the model does not allow.
func Conjoin(base *Expr, conds []Node) (*Expr, bool) {
	if base == nil || len(base.Filters) == 0 {
		return &Expr{Match: MatchAnd, Filters: append([]Node{}, conds...)}, true
	}
	if base.Match != MatchOr || len(base.Filters) == 1 {
		fs := append(append([]Node{}, base.Filters...), conds...)
		return &Expr{Match: MatchAnd, Filters: fs}, true
	}
	for _, n := range base.Filters {
		if len(n.Filters) > 0 {
			return nil, false
		}
	}
	inner := Node{Match: MatchOr, Filters: append([]Node{}, base.Filters...)}
	return &Expr{Match: MatchAnd, Filters: append([]Node{inner}, conds...)}, true
}
