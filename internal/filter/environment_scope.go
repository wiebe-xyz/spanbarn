package filter

import (
	"context"
	"strings"
)

type environmentScopeKey struct{}

// WithEnvironmentScope returns ctx carrying the environment a read is scoped
// to. "" and "all" mean every environment and leave ctx as it is.
func WithEnvironmentScope(ctx context.Context, env string) context.Context {
	if env == "" || env == "all" {
		return ctx
	}
	return context.WithValue(ctx, environmentScopeKey{}, env)
}

// EnvironmentScope is the environment ctx is scoped to, or "" for all of them.
func EnvironmentScope(ctx context.Context) string {
	env, _ := ctx.Value(environmentScopeKey{}).(string)
	return env
}

// environmentMarker is how json.Marshal writes the attribute with a string
// value: keys sorted, no spaces. deployment.environment.name does not match,
// because the quote after "environment" must be followed by a colon.
const environmentMarker = `"` + EnvironmentKey + `":"`

// EnvironmentOf reads the environment out of a span's attributes JSON: the
// value of deployment.environment, lowercased, or "" when the span has none.
// Ingest stamps the attribute from the resource or the key (#558), so one
// substring search finds it without decoding the document. A value with an
// escape in it is not an environment name and reads as none.
func EnvironmentOf(attributes string) string {
	_, rest, found := strings.Cut(attributes, environmentMarker)
	if !found {
		return ""
	}
	value, _, closed := strings.Cut(rest, `"`)
	if !closed || strings.Contains(value, `\`) {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(value))
}
