package calcfield

import (
	"strings"
)

// Field is a named expression.
type Field struct {
	Name       string
	Expression string
}

// Set is the calculated fields of one project, ready to compile. Fields may use
// each other. A cycle is an error.
type Set struct {
	exprs map[string]*Expr
	// bad holds the parse error of a field that no longer parses. Only a query
	// that uses that field fails, so one stored mistake does not take down
	// every query of the project.
	bad     map[string]error
	operand Resolver
	builtin func(key string) bool
}

// NewSet parses every field. operand reads a span column or attribute.
// builtin reports keys that always mean a span column or an explicit attribute
// and so never resolve to a calculated field.
func NewSet(fields []Field, operand Resolver, builtin func(key string) bool) *Set {
	s := &Set{
		exprs: make(map[string]*Expr, len(fields)), bad: map[string]error{},
		operand: operand, builtin: builtin,
	}
	for _, f := range fields {
		e, err := Parse(f.Expression)
		if err != nil {
			s.bad[f.Name] = invalid("%s: %v", f.Name, err)
		}
		s.exprs[f.Name] = e
	}
	return s
}

// Has reports whether name is a calculated field of the set.
func (s *Set) Has(name string) bool {
	_, ok := s.exprs[name]
	return ok && !s.builtin(name)
}

// compile expands the field name, or fails with its parse error.
func (s *Set) compile(c *compiler, name string) (string, []any, error) {
	if err := s.bad[name]; err != nil {
		return "", nil, err
	}
	return c.node(s.exprs[name].root)
}

// Lookup compiles the field called name. ok is false when the set has no such
// field, so the caller falls back to the attribute of that name.
func (s *Set) Lookup(name string) (sql string, args []any, ok bool, err error) {
	if !s.Has(name) {
		return "", nil, false, nil
	}
	budget := maxExpansion
	c := &compiler{budget: &budget}
	c.resolve = s.resolver(c.budget, []string{name})
	sql, args, err = s.compile(c, name)
	return sql, args, true, err
}

// resolver reads identifiers for the field at the end of stack: another
// calculated field expands in place, anything else goes to operand.
func (s *Set) resolver(budget *int, stack []string) Resolver {
	return func(key string) (string, []any, error) {
		if !s.Has(key) {
			return s.operand(key)
		}
		for _, seen := range stack {
			if seen == key {
				return "", nil, invalid("calculated fields refer to each other in a cycle: %s -> %s; "+
					"write attributes.%s to read the attribute of the same name",
					strings.Join(stack, " -> "), key, key)
			}
		}
		inner := &compiler{budget: budget}
		inner.resolve = s.resolver(budget, append(append([]string{}, stack...), key))
		return s.compile(inner, key)
	}
}

// Validate compiles every field of the set and returns the first error. It
// finds cycles and expansions over the cap.
func (s *Set) Validate() error {
	for name := range s.exprs {
		if _, _, _, err := s.Lookup(name); err != nil {
			return err
		}
	}
	return nil
}
