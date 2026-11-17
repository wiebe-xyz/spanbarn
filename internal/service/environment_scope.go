package service

import (
	"context"

	"github.com/wiebe-xyz/spanbarn/internal/filter"
)

// environmentOf returns the environment the request is scoped to, or "" when it
// reads every environment. A method whose parameter shadows the filter package
// reads the scope through it.
func environmentOf(ctx context.Context) string { return filter.EnvironmentScope(ctx) }
