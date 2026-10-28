package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
)

// setupKey derives the setup ingest key that the setup flow handed out before
// it minted random keys: an HMAC of the slug under the server session secret.
// It stays only to recognise a project that still holds such a key; once no
// project does, it goes.
func setupKey(secret, slug string) (plaintext, keySHA256 string) {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("setup:" + slug))
	raw := mac.Sum(nil)
	plaintext = hex.EncodeToString(raw)[:40]
	sum := sha256.Sum256([]byte(plaintext))
	keySHA256 = hex.EncodeToString(sum[:])
	return
}

// setupKeyKind says which ingest key, if any, a setup guide carries.
type setupKeyKind int

const (
	setupKeyNone    setupKeyKind = iota // existing project: its key was shown at creation
	setupKeyMinted                      // new project: a random key, shown this once
	setupKeyDerived                     // a project that still holds its slug-derived key
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

// derivedSetupKey returns the slug-derived key of a project that still holds
// it, so its guide keeps working until the key is rotated and revoked.
func (s *Server) derivedSetupKey(projectID int64, slug string) (string, setupKeyKind, error) {
	plaintext, hash := setupKey(s.sessionSecret, slug)
	held, err := s.projectService().HoldsSetupKey(projectID, hash)
	if err != nil || !held {
		return "", setupKeyNone, err
	}
	return plaintext, setupKeyDerived, nil
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
	case err == nil:
		key, kind, err = s.derivedSetupKey(project.ID, slug)
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
