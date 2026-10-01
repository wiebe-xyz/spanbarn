package filter

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

type colKind int

const (
	colText colKind = iota
	colInt
)

// columns are the span columns a key can name. Anything else is an attribute.
var columns = map[string]struct {
	name string
	kind colKind
}{
	"service":        {"service", colText},
	"name":           {"name", colText},
	"operation":      {"name", colText},
	"kind":           {"kind", colText},
	"status":         {"status", colText},
	"resource":       {"resource", colText},
	"trace_id":       {"trace_id", colText},
	"span_id":        {"span_id", colText},
	"parent_span_id": {"parent_span_id", colText},
	"duration_us":    {"duration_us", colInt},
	"duration":       {"duration_us", colInt},
	"start_time_us":  {"start_time_us", colInt},
	"http_status":    {"http_status", colInt},
}

// attrPrefix forces a key to be read as an attribute, for an attribute whose
// name equals a span column.
const attrPrefix = "attributes."

// operand is the SQL for one key. Each method returns fresh SQL and args
// because a path placeholder is bound once per use.
type operand struct {
	col  string
	kind colKind
	attr bool
	path string
}

// IsColumn reports whether a bare key names a span column instead of an
// attribute.
func IsColumn(key string) bool {
	_, ok := columns[key]
	return ok
}

// AttrKey returns the key that selects the attribute `name` in a filter. An
// attribute named like a span column needs the "attributes." prefix.
func AttrKey(name string) string {
	if IsColumn(name) || strings.HasPrefix(name, attrPrefix) {
		return attrPrefix + name
	}
	return name
}

func resolve(key string) operand {
	if rest, ok := strings.CutPrefix(key, attrPrefix); ok && rest != "" {
		return operand{attr: true, path: `$."` + rest + `"`}
	}
	if c, ok := columns[key]; ok {
		return operand{col: c.name, kind: c.kind}
	}
	return operand{attr: true, path: `$."` + key + `"`}
}

func (o operand) isInt() bool { return !o.attr && o.kind == colInt }

// text is the key as text. Attributes get booleans as true/false and a NULL
// when the key is missing, the value is JSON null or attributes is not JSON.
func (o operand) text() (string, []any) {
	if !o.attr {
		if o.kind == colInt {
			return "CAST(" + o.col + " AS TEXT)", nil
		}
		return o.col, nil
	}
	return `(CASE WHEN NOT json_valid(attributes) THEN NULL ELSE CASE json_type(attributes, ?)
		WHEN 'true' THEN 'true' WHEN 'false' THEN 'false'
		ELSE CAST(json_extract(attributes, ?) AS TEXT) END END)`, []any{o.path, o.path}
}

// number is the key as a number: NULL unless an attribute holds a JSON number.
func (o operand) number() (string, []any) {
	if !o.attr {
		return o.col, nil
	}
	return `(CASE WHEN json_valid(attributes) AND json_type(attributes, ?) IN ('integer','real')
		THEN json_extract(attributes, ?) END)`, []any{o.path, o.path}
}

// present is true when the key has a value.
func (o operand) present() (string, []any) {
	if o.attr {
		return `(CASE WHEN json_valid(attributes) THEN json_extract(attributes, ?) END) IS NOT NULL`, []any{o.path}
	}
	if o.kind == colInt {
		return o.col + " IS NOT NULL", nil
	}
	return "COALESCE(" + o.col + ", '') <> ''", nil
}

// Compile returns a SQL predicate over the spans table and its arguments. A nil
// or empty Expr compiles to "" with no arguments.
func Compile(e *Expr) (string, []any, error) {
	if e == nil || len(e.Filters) == 0 {
		return "", nil, nil
	}
	if err := e.Validate(); err != nil {
		return "", nil, err
	}
	return compileGroup(e)
}

func compileGroup(g *Node) (string, []any, error) {
	join := " AND "
	if g.Match == MatchOr {
		join = " OR "
	}
	parts := make([]string, 0, len(g.Filters))
	var args []any
	for i := range g.Filters {
		c := &g.Filters[i]
		var s string
		var a []any
		var err error
		if len(c.Filters) > 0 {
			s, a, err = compileGroup(c)
		} else {
			s, a, err = compileCondition(c)
		}
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, s)
		args = append(args, a...)
	}
	return "(" + strings.Join(parts, join) + ")", args, nil
}

func compileCondition(c *Node) (string, []any, error) {
	o := resolve(c.Key)
	switch c.Op {
	case OpExists:
		s, a := o.present()
		return s, a, nil
	case OpNotExists:
		s, a := o.present()
		return "NOT (" + s + ")", a, nil
	case OpEq, OpNe:
		return compileEquality(o, c)
	case OpGt, OpLt, OpGte, OpLte:
		return compileOrdering(o, c)
	case OpContains, OpStartsWith:
		return compileSubstring(o, c)
	case OpIn, OpNotIn:
		return compileIn(o, c)
	}
	return "", nil, invalid("unknown operator %q on %q", c.Op, c.Key)
}

func numArg(c *Node, v Scalar) (float64, error) {
	f, err := strconv.ParseFloat(string(v), 64)
	if err != nil {
		return 0, invalid("%q needs a number for %s, got %q", c.Key, c.Op, string(v))
	}
	return f, nil
}

// negate wraps a comparison so a missing key also matches.
func negate(ref string, args []any, tail string, vals []any) (string, []any) {
	all := append(append([]any{}, args...), args...)
	return "(" + ref + " IS NULL OR " + ref + " " + tail + ")", append(all, vals...)
}

func compileEquality(o operand, c *Node) (string, []any, error) {
	ref, args := o.text()
	var val any = string(c.Value)
	if o.isInt() {
		f, err := numArg(c, c.Value)
		if err != nil {
			return "", nil, err
		}
		ref, args, val = o.col, nil, f
	}
	if c.Op == OpEq {
		return ref + " = ?", append(args, val), nil
	}
	s, a := negate(ref, args, "<> ?", []any{val})
	return s, a, nil
}

var orderSQL = map[Op]string{OpGt: ">", OpLt: "<", OpGte: ">=", OpLte: "<="}

func compileOrdering(o operand, c *Node) (string, []any, error) {
	sym := orderSQL[c.Op]
	f, numErr := numArg(c, c.Value)
	if o.isInt() {
		if numErr != nil {
			return "", nil, numErr
		}
		return o.col + " " + sym + " ?", []any{f}, nil
	}
	if numErr == nil {
		ref, args := o.number()
		return ref + " " + sym + " ?", append(args, f), nil
	}
	ref, args := o.text()
	return ref + " " + sym + " ?", append(args, string(c.Value)), nil
}

func compileSubstring(o operand, c *Node) (string, []any, error) {
	ref, args := o.text()
	if c.Op == OpContains {
		return "instr(" + ref + ", ?) > 0", append(args, string(c.Value)), nil
	}
	n := utf8.RuneCountInString(string(c.Value))
	return "substr(" + ref + ", 1, ?) = ?", append(args, n, string(c.Value)), nil
}

func compileIn(o operand, c *Node) (string, []any, error) {
	ref, args := o.text()
	if o.isInt() {
		ref, args = o.col, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(c.Values)), ",")
	vals := make([]any, 0, len(c.Values))
	for _, v := range c.Values {
		if !o.isInt() {
			vals = append(vals, string(v))
			continue
		}
		f, err := numArg(c, v)
		if err != nil {
			return "", nil, err
		}
		vals = append(vals, f)
	}
	if c.Op == OpIn {
		return ref + " IN (" + marks + ")", append(args, vals...), nil
	}
	s, a := negate(ref, args, "NOT IN ("+marks+")", vals)
	return s, a, nil
}
