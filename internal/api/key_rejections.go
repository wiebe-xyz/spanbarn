package api

import (
	"net/http"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/service"
)

type keyRejectionHandlers struct{ svc *service.KeyRejectionService }

type keyRejectionResponse struct {
	Fingerprint string `json:"fingerprint"`
	Reason      string `json:"reason"`
	Count       int64  `json:"count"`
	FirstSeen   string `json:"firstSeen"`
	LastSeen    string `json:"lastSeen"`
	KeyID       int64  `json:"keyId,omitempty"`
	ProjectID   int64  `json:"projectId,omitempty"`
	KeyName     string `json:"keyName,omitempty"`
	RevokedAt   string `json:"revokedAt,omitempty"`
}

type keyRejectionsResponse struct {
	RetentionDays int                    `json:"retentionDays"`
	Rejections    []keyRejectionResponse `json:"rejections"`
}

// ServeHTTP answers GET /api/v1/apikey-rejections?since=<rfc3339>: the API keys
// that requests used although no stored key matches, newest first, with the
// number of requests and, for a key revoked since the counters exist, its id,
// project and name (#572). The list spans every project, so only a caller that
// is not bound to a project may read it: the static key and signed-in users.
// A caller from another organization cannot reach it, because the middleware
// asks that caller to name a project and this route takes none.
func (h *keyRejectionHandlers) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	}
	if GetProjectID(r.Context()) != 0 || r.URL.Query().Get("project_id") != "" {
		writeError(w, http.StatusForbidden, "forbidden", "the rejection counters span every project")
		return
	}
	var since time.Time
	if v := r.URL.Query().Get("since"); v != "" {
		t, err := parseTimeParam(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid since", err.Error())
			return
		}
		since = t
	}
	rows, err := h.svc.Since(since)
	if err != nil {
		writeServerError(w, r, "query failed", err)
		return
	}
	out := keyRejectionsResponse{
		RetentionDays: int(service.KeyRejectionRetention / (24 * time.Hour)),
		Rejections:    make([]keyRejectionResponse, 0, len(rows)),
	}
	for _, k := range rows {
		item := keyRejectionResponse{
			Fingerprint: k.Fingerprint, Reason: "unknown", Count: k.Count,
			FirstSeen: k.FirstSeen.Format(time.RFC3339), LastSeen: k.LastSeen.Format(time.RFC3339),
		}
		if k.Revoked {
			item.Reason = "revoked"
			item.KeyID, item.ProjectID, item.KeyName = k.KeyID, k.ProjectID, k.KeyName
			if !k.RevokedAt.IsZero() {
				item.RevokedAt = k.RevokedAt.Format(time.RFC3339)
			}
		}
		out.Rejections = append(out.Rejections, item)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}
