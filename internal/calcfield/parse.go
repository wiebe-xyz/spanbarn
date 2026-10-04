// Package calcfield parses and compiles calculated fields: a named expression
// over span columns and attributes that filters and group-bys use like a normal
// key.
//
// The grammar is closed. An expression is built from number and string
// literals, identifiers, arithmetic (+ - * / %), comparison (= == != <> < <=
// > >=), and, or, not, parentheses and four functions: coalesce, if, concat,
// lower. An identifier is a span column or attribute key, written bare
// (http.route) or in backticks when it holds other characters (`app.user-id`).
//
// Nothing a user writes is copied into SQL. Literals and attribute paths are
// bound parameters, operators and functions come from a fixed table and
// identifiers go through a caller-supplied Resolver. Length, depth, term count
// and argument count are capped.
package calcfield

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInvalid is wrapped by every parse and compile error.
var ErrInvalid = errors.New("invalid calculated field")

// Caps keep an expression from becoming a denial of service on the single
// reader.
const (
	MaxLength    = 500
	MaxDepth     = 16
	MaxNodes     = 200
	MaxCallArgs  = 8
	maxIdentLen  = 200
	maxExpansion = 2000
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

func tooDeep() error {
	return invalid("the expression nests deeper than %d levels", MaxDepth)
}

type kind int

const (
	kNumber kind = iota
	kString
	kIdent
	kUnary
	kBinary
	kCall
)

type node struct {
	kind kind
	num  any // int64 or float64
	text string
	args []*node
}

// Expr is a parsed expression.
type Expr struct {
	root *node
}

// functions lists the callable functions with their argument count range.
var functions = map[string]struct{ min, max int }{
	"coalesce": {2, MaxCallArgs},
	"if":       {3, 3},
	"concat":   {2, MaxCallArgs},
	"lower":    {1, 1},
}

// Functions returns the function names of the grammar.
func Functions() []string { return []string{"coalesce", "concat", "if", "lower"} }

// Parse reads src. The error wraps ErrInvalid.
func Parse(src string) (*Expr, error) {
	if strings.TrimSpace(src) == "" {
		return nil, invalid("the expression is empty")
	}
	if len(src) > MaxLength {
		return nil, invalid("the expression is longer than %d characters", MaxLength)
	}
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	root, err := p.parseOr(1)
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.typ != tEOF {
		return nil, invalid("unexpected %s", t.describe())
	}
	return &Expr{root: root}, nil
}

// Idents lists the identifiers an expression reads, once each in order of
// first use.
func (e *Expr) Idents() []string {
	var out []string
	seen := map[string]bool{}
	var walk func(n *node)
	walk = func(n *node) {
		if n.kind == kIdent && !seen[n.text] {
			seen[n.text] = true
			out = append(out, n.text)
		}
		for _, a := range n.args {
			walk(a)
		}
	}
	walk(e.root)
	return out
}

type parser struct {
	toks  []token
	pos   int
	nodes int
}

func (p *parser) peek() token { return p.toks[p.pos] }

func (p *parser) next() token {
	t := p.toks[p.pos]
	if t.typ != tEOF {
		p.pos++
	}
	return t
}

func (p *parser) accept(typ tokType, text string) bool {
	if t := p.peek(); t.typ == typ && t.text == text {
		p.pos++
		return true
	}
	return false
}

func (p *parser) mk(n *node) (*node, error) {
	if p.nodes++; p.nodes > MaxNodes {
		return nil, invalid("the expression has more than %d terms", MaxNodes)
	}
	return n, nil
}

func (p *parser) binary(op string, l, r *node) (*node, error) {
	return p.mk(&node{kind: kBinary, text: op, args: []*node{l, r}})
}

// parseOr is the entry of one nesting level. Every level of parentheses, call
// arguments, not and unary minus enters through depth+1, which bounds the
// recursion at MaxDepth.
func (p *parser) parseOr(depth int) (*node, error) {
	if depth > MaxDepth {
		return nil, tooDeep()
	}
	l, err := p.parseAnd(depth)
	for err == nil && p.accept(tWord, "or") {
		var r *node
		if r, err = p.parseAnd(depth); err == nil {
			l, err = p.binary("or", l, r)
		}
	}
	return l, err
}

func (p *parser) parseAnd(depth int) (*node, error) {
	l, err := p.parseNot(depth)
	for err == nil && p.accept(tWord, "and") {
		var r *node
		if r, err = p.parseNot(depth); err == nil {
			l, err = p.binary("and", l, r)
		}
	}
	return l, err
}

func (p *parser) parseNot(depth int) (*node, error) {
	if !p.accept(tWord, "not") {
		return p.parseCompare(depth)
	}
	if depth+1 > MaxDepth {
		return nil, tooDeep()
	}
	x, err := p.parseNot(depth + 1)
	if err != nil {
		return nil, err
	}
	return p.mk(&node{kind: kUnary, text: "not", args: []*node{x}})
}

var compareOps = map[string]bool{"=": true, "==": true, "!=": true, "<>": true, "<": true, "<=": true, ">": true, ">=": true}

func (p *parser) parseCompare(depth int) (*node, error) {
	l, err := p.parseSum(depth)
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.typ == tOp && compareOps[t.text] {
		p.next()
		r, err := p.parseSum(depth)
		if err != nil {
			return nil, err
		}
		return p.binary(t.text, l, r)
	}
	return l, nil
}

func (p *parser) parseSum(depth int) (*node, error) {
	l, err := p.parseProduct(depth)
	for err == nil {
		t := p.peek()
		if t.typ != tOp || (t.text != "+" && t.text != "-") {
			break
		}
		p.next()
		var r *node
		if r, err = p.parseProduct(depth); err == nil {
			l, err = p.binary(t.text, l, r)
		}
	}
	return l, err
}

func (p *parser) parseProduct(depth int) (*node, error) {
	l, err := p.parseUnary(depth)
	for err == nil {
		t := p.peek()
		if t.typ != tOp || (t.text != "*" && t.text != "/" && t.text != "%") {
			break
		}
		p.next()
		var r *node
		if r, err = p.parseUnary(depth); err == nil {
			l, err = p.binary(t.text, l, r)
		}
	}
	return l, err
}

func (p *parser) parseUnary(depth int) (*node, error) {
	t := p.peek()
	if t.typ != tOp || (t.text != "-" && t.text != "+") {
		return p.parsePrimary(depth)
	}
	p.next()
	if depth+1 > MaxDepth {
		return nil, tooDeep()
	}
	x, err := p.parseUnary(depth + 1)
	if err != nil || t.text == "+" {
		return x, err
	}
	return p.mk(&node{kind: kUnary, text: "-", args: []*node{x}})
}

func (p *parser) parsePrimary(depth int) (*node, error) {
	t := p.next()
	switch t.typ {
	case tNumber:
		return p.mk(&node{kind: kNumber, num: t.num})
	case tString:
		return p.mk(&node{kind: kString, text: t.text})
	case tQuoted:
		return p.mk(&node{kind: kIdent, text: t.text})
	case tWord:
		if p.peek().typ == tLParen {
			return p.parseCall(t.text, depth)
		}
		if reserved[t.text] {
			return nil, invalid("%q is a reserved word", t.text)
		}
		return p.mk(&node{kind: kIdent, text: t.text})
	case tLParen:
		x, err := p.parseOr(depth + 1)
		if err != nil {
			return nil, err
		}
		if p.next().typ != tRParen {
			return nil, invalid("missing closing parenthesis")
		}
		return x, nil
	}
	return nil, invalid("unexpected %s", t.describe())
}

func (p *parser) parseCall(name string, depth int) (*node, error) {
	fn := strings.ToLower(name)
	sig, ok := functions[fn]
	if !ok {
		return nil, invalid("unknown function %q, use one of %s", name, strings.Join(Functions(), ", "))
	}
	p.next() // (
	var args []*node
	for p.peek().typ != tRParen {
		a, err := p.parseOr(depth + 1)
		if err != nil {
			return nil, err
		}
		if args = append(args, a); len(args) > MaxCallArgs {
			return nil, invalid("%s takes at most %d arguments", name, MaxCallArgs)
		}
		if p.peek().typ != tComma {
			break
		}
		p.next()
	}
	if p.next().typ != tRParen {
		return nil, invalid("missing closing parenthesis in %s(", name)
	}
	if len(args) < sig.min || len(args) > sig.max {
		return nil, invalid("%s takes %d to %d arguments, got %d", name, sig.min, sig.max, len(args))
	}
	return p.mk(&node{kind: kCall, text: fn, args: args})
}
