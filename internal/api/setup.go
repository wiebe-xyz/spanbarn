package api

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
)

// setupKeyKind says which ingest key, if any, a setup guide carries.
type setupKeyKind int

const (
	setupKeyNone   setupKeyKind = iota // existing project: its key was shown at creation
	setupKeyMinted                     // new project: a random key, shown this once
)

// mintSetupKey registers a random ingest key as the project's setup key and
// returns it. Only its hash is stored, so the key cannot be shown again.
func (s *Server) mintSetupKey(projectID int64) (string, error) {
	key, hash, err := auth.NewAPIKey()
	if err != nil {
		return "", err
	}
	return key, s.projectService().EnsureSetupAPIKey(projectID, hash)
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	_, span := apiTracer.Start(r.Context(), "api.setup")
	defer span.End()

	// The guide can carry an ingest key, so the route requires a web session
	// (routes.go). GET only reads: an unknown slug is a 404. POST creates the
	// pending project and mints its setup key, and only when the slug is new;
	// the guide shows that key once. For an existing project both methods
	// render the guide without a key and write nothing.
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	}

	slug := r.PathValue("slug")
	if slug == "" {
		http.Error(w, "slug is required", http.StatusBadRequest)
		return
	}

	status, key, kind := http.StatusOK, "", setupKeyNone
	project, err := s.projectService().BySlug(slug)
	switch {
	case errors.Is(err, sql.ErrNoRows) && r.Method == http.MethodGet:
		writeError(w, http.StatusNotFound, "no project with this slug", "create it with POST /api/v1/setup/"+slug)
		return
	case errors.Is(err, sql.ErrNoRows):
		project, err = s.projectService().EnsurePending(slug, slug)
		if err == nil {
			key, err = s.mintSetupKey(project.ID)
		}
		status, kind = http.StatusCreated, setupKeyMinted
	}
	if err != nil {
		s.logger.Error("setup: ensure project/key", "slug", slug, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Ingest links follow the host the guide was requested on, so a branded
	// sb. alias stays branded. Dashboard links (E2E, project admin) always use
	// the canonical host: branded aliases carry the ingest surface only.
	publicURL := s.canonicalURL()
	ingestURL := s.ingestBaseURL(r)

	endpoint := ingestURL + "/v1/traces"
	setupURL := ingestURL + "/api/v1/setup/" + slug
	now := time.Now().UTC().Format(time.RFC3339)

	g := setupGuide{
		slug:       slug,
		status:     project.Status,
		projectID:  project.ID,
		e2eEnabled: project.E2EEnabled,
		now:        now,
		endpoint:   endpoint,
		setupURL:   setupURL,
		apiKey:     key,
		keyKind:    kind,
		ingestURL:  ingestURL,
		publicURL:  publicURL,
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Robots-Tag", "noindex")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(g.render()))
}
