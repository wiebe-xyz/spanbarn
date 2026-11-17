package api

import (
	"context"
	"encoding/json"
	"net/http"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
)

// Auth kinds recorded on every authenticated request.
const (
	authKindOIDCSession             = "oidc_session"
	authKindCLIToken                = "cli_token"
	authKindAPIKey                  = "api_key"
	authKindLocalSession            = "local_session"
	ctxCaller            contextKey = "caller"
)

// callerInfo is who made a request, as far as an exposure count needs: the way
// they authenticated and the IamBarn organization they signed in to. It holds
// no user id and no token.
type callerInfo struct {
	kind string
	org  string
	// key is set when an API key authenticated the request.
	key *auth.Identity
}

// withCaller returns a context carrying an empty callerInfo that the auth
// middleware fills in and the access log reads once the request is done.
func withCaller(ctx context.Context) (context.Context, *callerInfo) {
	c := &callerInfo{}
	return context.WithValue(ctx, ctxCaller, c), c
}

// recordCaller notes how the request authenticated and, when known, the
// organization, on the access log line and on the request's span.
func recordCaller(r *http.Request, kind, org string) {
	if c, ok := r.Context().Value(ctxCaller).(*callerInfo); ok {
		c.kind, c.org = kind, org
	}
	attrs := []attribute.KeyValue{attribute.String("auth.kind", kind)}
	if org != "" {
		attrs = append(attrs, attribute.String("auth.org_id", org))
	}
	trace.SpanFromContext(r.Context()).SetAttributes(attrs...)
}

// recordKeyCaller notes that an API key authenticated the request, with the
// key's identity for the access log and the key-use line.
func recordKeyCaller(r *http.Request, ident auth.Identity) {
	recordCaller(r, authKindAPIKey, "")
	if c, ok := r.Context().Value(ctxCaller).(*callerInfo); ok {
		c.key = &ident
	}
}

// sessionOrganization reads organization_id out of a stored claims snapshot.
// Sessions from before the organization claim was parsed have none.
func sessionOrganization(claimsJSON string) string {
	var c struct {
		OrganizationID string `json:"organization_id"`
	}
	if claimsJSON == "" || json.Unmarshal([]byte(claimsJSON), &c) != nil {
		return ""
	}
	return c.OrganizationID
}

// sessionAuthKind names the kind for a stored web session.
func sessionAuthKind(authMethod string) string {
	if authMethod == "oidc" {
		return authKindOIDCSession
	}
	return authKindLocalSession
}
