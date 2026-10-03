// Package calcresolve joins the calculated field compiler to the filter key
// rules. filter and calcfield are both leaf packages, so the glue lives here.
package calcresolve

import (
	"fmt"

	"github.com/wiebe-xyz/spanbarn/internal/calcfield"
	"github.com/wiebe-xyz/spanbarn/internal/filter"
)

// NewSet parses the calculated fields of one project. Their identifiers
// follow the filter key rules: a span column or "attributes." key is built in,
// another field of the set expands in place, anything else is an attribute.
func NewSet(fields []calcfield.Field) *calcfield.Set {
	operand := func(key string) (string, []any, error) {
		s, a := filter.ValueSQL(key)
		return s, a, nil
	}
	return calcfield.NewSet(fields, operand, filter.IsBuiltin)
}

// Resolver makes the filter.Resolver that lets filters and group-bys use the
// fields of set as keys. A field that fails to compile (a cycle) is an error,
// never a silent NULL.
func Resolver(set *calcfield.Set) filter.Resolver {
	return func(key string) (*filter.Resolved, error) {
		sql, args, ok, err := set.Lookup(key)
		if err != nil {
			return nil, fmt.Errorf("%w: calculated field %q: %v", filter.ErrInvalid, key, err)
		}
		if !ok {
			return nil, nil
		}
		return &filter.Resolved{SQL: sql, Args: args}, nil
	}
}
