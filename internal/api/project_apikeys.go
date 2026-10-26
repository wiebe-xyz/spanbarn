package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
	"github.com/wiebe-xyz/spanbarn/internal/service"
)

// createdAPIKeyResponse is the one response that carries a key in plain
// text. Nothing stores it: a lost key is revoked and replaced.
type createdAPIKeyResponse struct {
	apiKeyResponse
	Key string `json:"key"`
}

// handleCreateAPIKey mints a random key for the project. The project routes
// let only a web session through for POST, so neither an API key nor an
// OIDC bearer can mint keys.
func (h *projectHandlers) handleCreateAPIKey(w http.ResponseWriter, r *http.Request, projectID int64) {
	_, span := apiTracer.Start(r.Context(), "api.projects.create_api_key")
	defer span.End()
	span.SetAttributes(attribute.Int64("project.id", projectID))

	var req struct {
		Name  string `json:"name"`
		Scope string `json:"scope"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Scope == "" {
		req.Scope = "ingest"
	}
	key, hash, err := auth.NewAPIKey()
	if err != nil {
		writeServerError(w, r, "failed to generate api key", err)
		return
	}
	id, err := h.svc.CreateAPIKey(projectID, req.Name, req.Scope, hash)
	if err != nil {
		writeAPIKeyError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, createdAPIKeyResponse{
		apiKeyResponse: apiKeyResponse{
			ID: id, ProjectID: projectID, Name: req.Name, Scope: req.Scope,
			CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		},
		Key: key,
	})
}

// revokeAPIKey returns the handler for DELETE /projects/{id}/apikeys/{keyID}.
func (h *projectHandlers) revokeAPIKey(rawKeyID string) projectIDHandler {
	return func(w http.ResponseWriter, r *http.Request, projectID int64) {
		_, span := apiTracer.Start(r.Context(), "api.projects.revoke_api_key")
		defer span.End()
		span.SetAttributes(attribute.Int64("project.id", projectID))

		keyID, err := strconv.ParseInt(rawKeyID, 10, 64)
		if err != nil || keyID <= 0 {
			writeError(w, http.StatusBadRequest, "invalid api key id", "")
			return
		}
		if err := h.svc.RevokeAPIKey(projectID, keyID); err != nil {
			writeAPIKeyError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func writeAPIKeyError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidAPIKey):
		writeError(w, http.StatusBadRequest, "invalid api key request", err.Error())
	case errors.Is(err, service.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found", "")
	default:
		writeServerError(w, r, "api key request failed", err)
	}
}
