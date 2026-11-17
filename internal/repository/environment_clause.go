package repository

import "github.com/wiebe-xyz/spanbarn/internal/filter"

// environmentClause is the WHERE condition that selects one environment's rows
// of a table with an environment column, and its arguments. production also
// selects the rows with no environment: a sender that never set one has always
// been production. "" and "all" select every row and give no condition. The
// COALESCE reads NULL, which the read views give for a shard written before the
// column existed, as no environment.
func environmentClause(env string) (string, []any) {
	switch env {
	case "", "all":
		return "", nil
	case filter.ProductionEnvironment:
		return "COALESCE(environment, '') IN ('', ?)", []any{env}
	}
	return "environment = ?", []any{env}
}
