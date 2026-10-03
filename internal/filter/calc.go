package filter

import (
	"fmt"

	"github.com/wiebe-xyz/spanbarn/internal/calcfield"
)

// NewCalcSet parses the calculated fields of one project. Their identifiers
// follow the filter key rules: a span column or "attributes." key is built in,
// another field of the set expands in place, anything else is an attribute.
func NewCalcSet(fields []calcfield.Field) *calcfield.Set {
	operand := func(key string) (string, []any, error) {
		s, a := ValueSQL(key)
		return s, a, nil
	}
	return calcfield.NewSet(fields, operand, IsBuiltin)
}

// CalcResolver makes the Resolver that lets filters and group-bys use the
// fields of set as keys. A field that fails to compile (a cycle) is an error,
// never a silent NULL.
func CalcResolver(set *calcfield.Set) Resolver {
	return func(key string) (*Resolved, error) {
		sql, args, ok, err := set.Lookup(key)
		if err != nil {
			return nil, fmt.Errorf("%w: calculated field %q: %v", ErrInvalid, key, err)
		}
		if !ok {
			return nil, nil
		}
		return &Resolved{SQL: sql, Args: args}, nil
	}
}
