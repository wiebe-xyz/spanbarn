package calcfield

import (
	"strings"
)

// Resolver returns the SQL that reads one identifier as a typed value (a span
// column, or an attribute as its JSON value) and the arguments that SQL binds,
// in order of appearance.
type Resolver func(key string) (sql string, args []any, err error)

// Compile returns a SQL value expression for e and its bound arguments. The
// result is NULL when an operand is missing. Division is real division and
// yields NULL for a zero divisor.
func (e *Expr) Compile(r Resolver) (string, []any, error) {
	budget := maxExpansion
	c := &compiler{resolve: r, budget: &budget}
	return c.node(e.root)
}

// Compile parses and compiles src.
func Compile(src string, r Resolver) (string, []any, error) {
	e, err := Parse(src)
	if err != nil {
		return "", nil, err
	}
	return e.Compile(r)
}

type compiler struct {
	resolve Resolver
	// budget counts down the terms one compile may produce, so a chain of
	// calculated fields that each use the previous one twice stays bounded.
	budget *int
}

// spend takes n terms from the budget.
func (c *compiler) spend(n int) error {
	if *c.budget -= n; *c.budget < 0 {
		return invalid("the expression expands to more than %d terms once its calculated fields are included", maxExpansion)
	}
	return nil
}

var binarySQL = map[string]string{
	"+": "+", "-": "-", "*": "*", "%": "%",
	"=": "=", "==": "=", "!=": "<>", "<>": "<>", "<": "<", "<=": "<=", ">": ">", ">=": ">=",
	"and": "AND", "or": "OR",
}

func (c *compiler) node(n *node) (string, []any, error) {
	if err := c.spend(1); err != nil {
		return "", nil, err
	}
	switch n.kind {
	case kNumber:
		return "?", []any{n.num}, nil
	case kString:
		return "?", []any{n.text}, nil
	case kIdent:
		return c.resolve(n.text)
	case kUnary:
		return c.unary(n)
	case kBinary:
		return c.binary(n)
	}
	return c.call(n)
}

func (c *compiler) unary(n *node) (string, []any, error) {
	x, args, err := c.node(n.args[0])
	if err != nil {
		return "", nil, err
	}
	if n.text == "not" {
		return "(NOT " + x + ")", args, nil
	}
	return "(0 - " + x + ")", args, nil
}

func (c *compiler) binary(n *node) (string, []any, error) {
	l, la, err := c.node(n.args[0])
	if err != nil {
		return "", nil, err
	}
	r, ra, err := c.node(n.args[1])
	if err != nil {
		return "", nil, err
	}
	args := append(la, ra...)
	if n.text == "/" {
		return "(CAST(" + l + " AS REAL) / " + r + ")", args, nil
	}
	return "(" + l + " " + binarySQL[n.text] + " " + r + ")", args, nil
}

func (c *compiler) call(n *node) (string, []any, error) {
	parts := make([]string, len(n.args))
	var args []any
	for i, a := range n.args {
		s, as, err := c.node(a)
		if err != nil {
			return "", nil, err
		}
		parts[i] = s
		args = append(args, as...)
	}
	switch n.text {
	case "coalesce":
		return "COALESCE(" + strings.Join(parts, ", ") + ")", args, nil
	case "lower":
		return "lower(" + parts[0] + ")", args, nil
	case "if":
		return "(CASE WHEN " + parts[0] + " THEN " + parts[1] + " ELSE " + parts[2] + " END)", args, nil
	}
	return concatSQL(parts, args)
}

// concatSQL joins its arguments as text. A missing argument reads as "".
func concatSQL(parts []string, args []any) (string, []any, error) {
	wrapped := make([]string, len(parts))
	for i, p := range parts {
		wrapped[i] = "COALESCE(CAST(" + p + " AS TEXT), '')"
	}
	return "(" + strings.Join(wrapped, " || ") + ")", args, nil
}
