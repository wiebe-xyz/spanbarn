package service

import (
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// The configuration services add no behavior of their own. Errors pass
// through unchanged and the API layer decides the HTTP status; the HTTP
// error path logs them.

// SpanStreamRepository is the storage ExportService needs.
type SpanStreamRepository interface {
	StreamSpans(f repository.SpanFilter, fn func(repository.Span) error) error
}

// ExportService streams spans for download.
type ExportService struct{ repo SpanStreamRepository }

func NewExportService(repo SpanStreamRepository) *ExportService { return &ExportService{repo: repo} }

// StreamSpans calls fn for each span matching f until fn fails.
func (s *ExportService) StreamSpans(f SpanFilter, fn func(Span) error) error {
	return s.repo.StreamSpans(f, fn)
}
