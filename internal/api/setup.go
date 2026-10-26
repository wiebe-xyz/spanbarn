package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"time"
)

// setupKey deterministically derives a project's setup ingest key from the
// server session secret and the project slug. The secret is mandatory outside
// dev (enforced by config.Validate), so there is deliberately no constant
// fallback — a weak/guessable key would let anyone forge a project's ingest
// credentials, and the setup endpoint is public.
func setupKey(secret, slug string) (plaintext, keySHA256 string) {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("setup:" + slug))
	raw := mac.Sum(nil)
	plaintext = hex.EncodeToString(raw)[:40]
	sum := sha256.Sum256([]byte(plaintext))
	keySHA256 = hex.EncodeToString(sum[:])
	return
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	_, span := apiTracer.Start(r.Context(), "api.setup")
	defer span.End()

	// The guide carries an ingest key, so the route requires a web session
	// (routes.go). GET only reads: an unknown slug is a 404. POST creates the
	// pending project and registers its setup key, and only when the slug is
	// new; for an existing project it renders the guide and writes nothing.
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	}

	slug := r.PathValue("slug")
	if slug == "" {
		http.Error(w, "slug is required", http.StatusBadRequest)
		return
	}

	// The setup key is deterministic from (session secret, slug), so it never
	// needs to be read back; render it directly.
	plaintext, keySHA := setupKey(s.sessionSecret, slug)

	status := http.StatusOK
	project, err := s.projectService().BySlug(slug)
	if errors.Is(err, sql.ErrNoRows) {
		if r.Method == http.MethodGet {
			writeError(w, http.StatusNotFound, "no project with this slug", "create it with POST /api/v1/setup/"+slug)
			return
		}
		project, err = s.projectService().EnsurePending(slug, slug)
		if err == nil {
			err = s.projectService().EnsureSetupAPIKey(project.ID, keySHA)
		}
		status = http.StatusCreated
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
		apiKey:     plaintext,
		ingestURL:  ingestURL,
		publicURL:  publicURL,
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Robots-Tag", "noindex")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(g.render()))
}
