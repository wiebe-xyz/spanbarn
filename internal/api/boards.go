package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/filter"
	"github.com/wiebe-xyz/spanbarn/internal/service"
)

type boardHandlers struct {
	svc *service.BoardService
}

// register mounts the board and release endpoints. All of them need a session.
func (h *boardHandlers) register(mux *http.ServeMux, wrap func(http.Handler) http.Handler) {
	routes := map[string]http.HandlerFunc{
		"GET /api/v1/boards":                        h.handleList,
		"POST /api/v1/boards":                       h.handleCreate,
		"GET /api/v1/boards/{id}":                   h.handleGet,
		"PUT /api/v1/boards/{id}":                   h.handleUpdate,
		"DELETE /api/v1/boards/{id}":                h.handleDelete,
		"POST /api/v1/boards/{id}/panels":           h.handleAddPanel,
		"PUT /api/v1/boards/{id}/panels/order":      h.handleReorder,
		"PUT /api/v1/boards/{id}/panels/{panel}":    h.handleUpdatePanel,
		"DELETE /api/v1/boards/{id}/panels/{panel}": h.handleDeletePanel,
		"GET /api/v1/releases":                      h.handleListReleases,
		"POST /api/v1/releases":                     h.handleCreateRelease,
	}
	for pattern, fn := range routes {
		mux.Handle(pattern, wrap(fn))
	}
}

func pathID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid "+name, "")
		return 0, false
	}
	return id, true
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON", err.Error())
		return false
	}
	return true
}

// writeBoardError maps service errors to HTTP statuses.
func writeBoardError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found", "")
	case errors.Is(err, filter.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid request", err.Error())
	default:
		writeServerError(w, r, "board request failed", err)
	}
}

func (h *boardHandlers) handleList(w http.ResponseWriter, r *http.Request) {
	boards, err := h.svc.ListBoards(parseInt64Param(r, "project_id", 0))
	if err != nil {
		writeBoardError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, boards)
}

type boardBody struct {
	ProjectID      int64  `json:"projectId"`
	Name           string `json:"name"`
	TimeRange      string `json:"timeRange"`
	RefreshSeconds int    `json:"refreshSeconds"`
}

func (h *boardHandlers) handleCreate(w http.ResponseWriter, r *http.Request) {
	var b boardBody
	if !decodeBody(w, r, &b) {
		return
	}
	id, err := h.svc.CreateBoard(b.ProjectID, b.Name, b.TimeRange, b.RefreshSeconds)
	if err != nil {
		writeBoardError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (h *boardHandlers) handleGet(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	board, err := h.svc.GetBoard(id)
	if err != nil {
		writeBoardError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, board)
}

func (h *boardHandlers) handleUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var b boardBody
	if !decodeBody(w, r, &b) {
		return
	}
	if err := h.svc.UpdateBoard(id, b.Name, b.TimeRange, b.RefreshSeconds); err != nil {
		writeBoardError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *boardHandlers) handleDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if err := h.svc.DeleteBoard(id); err != nil {
		writeBoardError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *boardHandlers) handleAddPanel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		Title      string                  `json:"title"`
		View       string                  `json:"view"`
		Filters    json.RawMessage         `json:"filters"`
		Definition service.QueryDefinition `json:"definition"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	expr, err := filter.Parse(string(body.Filters))
	if err != nil {
		writeBoardError(w, r, err)
		return
	}
	panelID, err := h.svc.AddPanel(id, service.PanelRequest{
		Title: body.Title, View: body.View, Filters: expr, Definition: body.Definition,
	})
	if err != nil {
		writeBoardError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]int64{"id": panelID})
}

func (h *boardHandlers) handleUpdatePanel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	panelID, ok := pathID(w, r, "panel")
	if !ok {
		return
	}
	var body struct {
		Title string `json:"title"`
		View  string `json:"view"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if err := h.svc.UpdatePanel(id, panelID, body.Title, body.View); err != nil {
		writeBoardError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *boardHandlers) handleDeletePanel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	panelID, ok := pathID(w, r, "panel")
	if !ok {
		return
	}
	if err := h.svc.DeletePanel(id, panelID); err != nil {
		writeBoardError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *boardHandlers) handleReorder(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		PanelIDs []int64 `json:"panelIds"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if err := h.svc.ReorderPanels(id, body.PanelIDs); err != nil {
		writeBoardError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *boardHandlers) handleListReleases(w http.ResponseWriter, r *http.Request) {
	from, to, ok := parseGetTimeRange(w, r)
	if !ok {
		return
	}
	if from.IsZero() || to.IsZero() {
		writeError(w, http.StatusBadRequest, "from and to are required", "")
		return
	}
	rels, err := h.svc.ListReleases(parseInt64Param(r, "project_id", 0), from, to)
	if err != nil {
		writeBoardError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rels)
}

func (h *boardHandlers) handleCreateRelease(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProjectID  int64     `json:"projectId"`
		Version    string    `json:"version"`
		ReleasedAt time.Time `json:"releasedAt"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	id, err := h.svc.CreateRelease(body.ProjectID, body.Version, body.ReleasedAt)
	if err != nil {
		writeBoardError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}
