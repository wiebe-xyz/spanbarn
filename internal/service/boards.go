package service

import (
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/filter"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// ErrNotFound reports a board, panel or release that does not exist.
var ErrNotFound = repository.ErrNotFound

const (
	maxBoardName   = 120
	maxPanelTitle  = 200
	maxPanels      = 50
	maxVersionName = 120
	// DefaultBoardRange is the shared time range of a new board.
	DefaultBoardRange = "24h"
)

// BoardRanges are the shared time ranges a board offers, each within the
// 30-day limit of a group-by query.
var BoardRanges = []string{"1h", "24h", "7d", "30d"}

// BoardRefreshSeconds are the refresh intervals a board offers. 0 is off.
var BoardRefreshSeconds = []int{0, 30, 60, 300, 900}

// PanelViews are the two ways a panel draws its query.
var PanelViews = []string{"table", "chart"}

// BoardRepository is the storage BoardService needs.
type BoardRepository interface {
	CreateBoard(projectID int64, name, timeRange string, refreshSeconds int) (int64, error)
	UpdateBoard(id int64, name, timeRange string, refreshSeconds int) error
	GetBoard(id int64) (*repository.Board, error)
	ListBoards(projectID int64) ([]repository.Board, error)
	DeleteBoard(id int64) error
	AddPanelWithQuery(boardID int64, q repository.SavedQuery, title, view string) (int64, error)
	UpdatePanel(boardID, panelID int64, title, view string) error
	DeletePanel(boardID, panelID int64) error
	ReorderPanels(boardID int64, ids []int64) error
	CreateRelease(projectID int64, version string, at time.Time) (int64, error)
	ListReleases(projectID int64, from, to time.Time) ([]repository.Release, error)
}

// Board, BoardPanel and Release are what the API returns.
type (
	Board      = repository.Board
	BoardPanel = repository.BoardPanel
	Release    = repository.Release
)

// QueryDefinition is the part of a board query that the filter model does not
// cover. It matches the parameters of the group-by endpoints.
type QueryDefinition struct {
	GroupBy []string `json:"groupBy"`
	Calcs   []string `json:"calcs"`
	OrderBy string   `json:"orderBy,omitempty"`
	Asc     bool     `json:"asc,omitempty"`
	Limit   int      `json:"limit,omitempty"`
	Sample  int      `json:"sample,omitempty"`
	// ChartCalc is the calculation a chart panel draws. It defaults to the first.
	ChartCalc string `json:"chartCalc,omitempty"`
}

// PanelRequest adds a query to a board ("save to board").
type PanelRequest struct {
	Title      string
	View       string
	Filters    *filter.Expr
	Definition QueryDefinition
}

// BoardService manages boards, their panels and release markers.
type BoardService struct {
	repo   BoardRepository
	logger *slog.Logger
}

func NewBoardService(repo BoardRepository, logger *slog.Logger) *BoardService {
	if logger == nil {
		logger = slog.Default()
	}
	return &BoardService{repo: repo, logger: logger}
}

func oneOf[T comparable](v T, allowed []T) bool {
	for _, a := range allowed {
		if a == v {
			return true
		}
	}
	return false
}

func (s *BoardService) fail(msg string, err error, args ...any) error {
	if errors.Is(err, ErrNotFound) || errors.Is(err, filter.ErrInvalid) || errors.Is(err, repository.ErrPanelSet) {
		return err
	}
	s.logger.Error(msg, append([]any{"error", err}, args...)...)
	return err
}

func validateBoardSettings(name, timeRange string, refresh int) error {
	switch {
	case strings.TrimSpace(name) == "":
		return analyzeInvalid("name is required")
	case len(name) > maxBoardName:
		return analyzeInvalid("name is limited to %d characters", maxBoardName)
	case !oneOf(timeRange, BoardRanges):
		return analyzeInvalid("time range must be one of %s", strings.Join(BoardRanges, ", "))
	case !oneOf(refresh, BoardRefreshSeconds):
		return analyzeInvalid("refresh interval must be 0, 30, 60, 300 or 900 seconds")
	}
	return nil
}

// CreateBoard makes an empty board. An empty timeRange means DefaultBoardRange.
func (s *BoardService) CreateBoard(projectID int64, name, timeRange string, refresh int) (int64, error) {
	if timeRange == "" {
		timeRange = DefaultBoardRange
	}
	name = strings.TrimSpace(name)
	if projectID <= 0 {
		return 0, analyzeInvalid("project_id is required")
	}
	if err := validateBoardSettings(name, timeRange, refresh); err != nil {
		return 0, err
	}
	id, err := s.repo.CreateBoard(projectID, name, timeRange, refresh)
	if err != nil {
		return 0, s.fail("create board failed", err, "project_id", projectID)
	}
	return id, nil
}

// UpdateBoard replaces the name, shared time range and refresh interval.
func (s *BoardService) UpdateBoard(id int64, name, timeRange string, refresh int) error {
	name = strings.TrimSpace(name)
	if err := validateBoardSettings(name, timeRange, refresh); err != nil {
		return err
	}
	if err := s.repo.UpdateBoard(id, name, timeRange, refresh); err != nil {
		return s.fail("update board failed", err, "board_id", id)
	}
	return nil
}

func (s *BoardService) GetBoard(id int64) (*Board, error) {
	b, err := s.repo.GetBoard(id)
	if err != nil {
		return nil, s.fail("get board failed", err, "board_id", id)
	}
	return b, nil
}

func (s *BoardService) ListBoards(projectID int64) ([]Board, error) {
	boards, err := s.repo.ListBoards(projectID)
	if err != nil {
		return nil, s.fail("list boards failed", err, "project_id", projectID)
	}
	if boards == nil {
		boards = []Board{}
	}
	return boards, nil
}

func (s *BoardService) DeleteBoard(id int64) error {
	if err := s.repo.DeleteBoard(id); err != nil {
		return s.fail("delete board failed", err, "board_id", id)
	}
	return nil
}

func (d QueryDefinition) validate() error {
	if len(d.Calcs) == 0 {
		return analyzeInvalid("at least one calculation is required")
	}
	req := AnalyzeRequest{
		ProjectID: 1, From: time.Unix(0, 0), To: time.Unix(1, 0),
		GroupBy: d.GroupBy, Calcs: d.Calcs, Sample: d.Sample,
	}
	if err := req.validate(); err != nil {
		return err
	}
	for _, c := range d.Calcs {
		if _, err := parseCalc(c); err != nil {
			return err
		}
	}
	for _, c := range []string{d.OrderBy, d.ChartCalc} {
		if c != "" && !contains(d.Calcs, c) {
			return analyzeInvalid("%q is not one of the calculations", c)
		}
	}
	if d.Limit < 0 || d.Limit > hardAnalyzeGroups {
		return analyzeInvalid("limit must be between 1 and %d", hardAnalyzeGroups)
	}
	return nil
}

func (r PanelRequest) validate() error {
	if len(r.Title) > maxPanelTitle {
		return analyzeInvalid("title is limited to %d characters", maxPanelTitle)
	}
	if !oneOf(r.View, PanelViews) {
		return analyzeInvalid("view must be table or chart")
	}
	return r.Definition.validate()
}

// AddPanel saves a query and appends it to the board as a panel.
func (s *BoardService) AddPanel(boardID int64, req PanelRequest) (int64, error) {
	if req.View == "" {
		req.View = "table"
	}
	if err := req.validate(); err != nil {
		return 0, err
	}
	board, err := s.repo.GetBoard(boardID)
	if err != nil {
		return 0, s.fail("add panel failed", err, "board_id", boardID)
	}
	if len(board.Panels) >= maxPanels {
		return 0, analyzeInvalid("a board holds at most %d panels", maxPanels)
	}
	def, err := json.Marshal(req.Definition)
	if err != nil {
		return 0, err
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "Untitled panel"
	}
	id, err := s.repo.AddPanelWithQuery(boardID, repository.SavedQuery{
		Name:       title,
		Filters:    json.RawMessage(filter.Marshal(req.Filters)),
		Definition: def,
	}, title, req.View)
	if err != nil {
		return 0, s.fail("add panel failed", err, "board_id", boardID)
	}
	return id, nil
}

// UpdatePanel renames a panel and switches it between table and chart.
func (s *BoardService) UpdatePanel(boardID, panelID int64, title, view string) error {
	title = strings.TrimSpace(title)
	if title == "" || len(title) > maxPanelTitle {
		return analyzeInvalid("title is required and limited to %d characters", maxPanelTitle)
	}
	if !oneOf(view, PanelViews) {
		return analyzeInvalid("view must be table or chart")
	}
	if err := s.repo.UpdatePanel(boardID, panelID, title, view); err != nil {
		return s.fail("update panel failed", err, "board_id", boardID, "panel_id", panelID)
	}
	return nil
}

func (s *BoardService) DeletePanel(boardID, panelID int64) error {
	if err := s.repo.DeletePanel(boardID, panelID); err != nil {
		return s.fail("delete panel failed", err, "board_id", boardID, "panel_id", panelID)
	}
	return nil
}

// ReorderPanels sets the grid order. ids must list every panel once.
func (s *BoardService) ReorderPanels(boardID int64, ids []int64) error {
	if err := s.repo.ReorderPanels(boardID, ids); err != nil {
		if errors.Is(err, repository.ErrPanelSet) {
			return analyzeInvalid("%s", err.Error())
		}
		return s.fail("reorder panels failed", err, "board_id", boardID)
	}
	return nil
}

// CreateRelease records a release marker. A zero time means now.
func (s *BoardService) CreateRelease(projectID int64, version string, at time.Time) (int64, error) {
	version = strings.TrimSpace(version)
	switch {
	case projectID <= 0:
		return 0, analyzeInvalid("project_id is required")
	case version == "" || len(version) > maxVersionName:
		return 0, analyzeInvalid("version is required and limited to %d characters", maxVersionName)
	}
	if at.IsZero() {
		at = time.Now()
	}
	id, err := s.repo.CreateRelease(projectID, version, at)
	if err != nil {
		return 0, s.fail("create release failed", err, "project_id", projectID)
	}
	return id, nil
}

// ListReleases returns the release markers of a project in a time range.
func (s *BoardService) ListReleases(projectID int64, from, to time.Time) ([]Release, error) {
	if projectID <= 0 {
		return nil, analyzeInvalid("project_id is required")
	}
	rels, err := s.repo.ListReleases(projectID, from, to)
	if err != nil {
		return nil, s.fail("list releases failed", err, "project_id", projectID)
	}
	return rels, nil
}
