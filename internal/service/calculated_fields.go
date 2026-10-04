package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/wiebe-xyz/spanbarn/internal/calcfield"
	"github.com/wiebe-xyz/spanbarn/internal/calcresolve"
	"github.com/wiebe-xyz/spanbarn/internal/filter"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

const (
	maxFieldName        = 64
	maxFieldsPerProject = 50
	// previewFieldName names a field under test that has no name yet.
	previewFieldName = "preview"
)

// fieldName is the shape of a calculated field name: the characters of a bare
// identifier in an expression, so any field can be written without backticks.
var fieldName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)

// CalculatedField and CalculatedFieldSample are what the API returns.
type (
	CalculatedField       = repository.CalculatedField
	CalculatedFieldSample = repository.CalculatedFieldSample
)

// CalculatedFieldRepository is the storage CalculatedFieldService needs.
type CalculatedFieldRepository interface {
	CreateCalculatedField(f repository.CalculatedField) (int64, error)
	UpdateCalculatedField(id int64, name, expression string) error
	DeleteCalculatedField(id int64) error
	GetCalculatedField(id int64) (*repository.CalculatedField, error)
	ListCalculatedFields(projectID int64) ([]repository.CalculatedField, error)
	PreviewCalculatedField(ctx context.Context, projectID int64, name, expression string) ([]repository.CalculatedFieldSample, error)
}

// CalculatedFieldService manages the calculated fields of a project and
// validates them before they reach storage, where a query would compile them.
type CalculatedFieldService struct {
	repo   CalculatedFieldRepository
	logger *slog.Logger
}

func NewCalculatedFieldService(repo CalculatedFieldRepository, logger *slog.Logger) *CalculatedFieldService {
	if logger == nil {
		logger = slog.Default()
	}
	return &CalculatedFieldService{repo: repo, logger: logger}
}

func (s *CalculatedFieldService) fail(msg string, err error, args ...any) error {
	switch {
	case errors.Is(err, repository.ErrDuplicateField):
		return analyzeInvalid("%v", err)
	case errors.Is(err, ErrNotFound), errors.Is(err, filter.ErrInvalid):
		return err
	}
	s.logger.Error(msg, append([]any{"error", err}, args...)...)
	return err
}

// validateName rejects a name a filter could not use as a key or that would
// hide a span column. A field may share a name with an attribute and then
// takes its place; it reads the attribute with the "attributes." prefix.
func validateName(name string) error {
	switch {
	case name == "" || len(name) > maxFieldName || !fieldName.MatchString(name):
		return analyzeInvalid("name is 1 to %d letters, digits, underscores and dots, starting with a letter or underscore", maxFieldName)
	case filter.IsBuiltin(name):
		return analyzeInvalid("name %q is a span column or starts with %q, which always read the column or attribute", name, "attributes.")
	}
	return nil
}

// validateExpression checks expression as the field called name next to the
// other fields of the project: it must parse, compile, and close no cycle.
func validateExpression(others []repository.CalculatedField, name, expression string) error {
	fields := make([]calcfield.Field, 0, len(others)+1)
	for _, f := range others {
		fields = append(fields, calcfield.Field{Name: f.Name, Expression: f.Expression})
	}
	fields = append(fields, calcfield.Field{Name: name, Expression: expression})
	if _, err := calcfield.Parse(expression); err != nil {
		return analyzeInvalid("%v", err)
	}
	// Every field is compiled: changing one can close a cycle through another.
	if err := calcresolve.NewSet(fields).Validate(); err != nil {
		return analyzeInvalid("%v", err)
	}
	return nil
}

// without returns fields minus the one with the given id.
func without(fields []repository.CalculatedField, id int64) []repository.CalculatedField {
	out := make([]repository.CalculatedField, 0, len(fields))
	for _, f := range fields {
		if f.ID != id {
			out = append(out, f)
		}
	}
	return out
}

func hasName(fields []repository.CalculatedField, name string) bool {
	for _, f := range fields {
		if f.Name == name {
			return true
		}
	}
	return false
}

// List returns the fields of a project.
func (s *CalculatedFieldService) List(projectID int64) ([]CalculatedField, error) {
	if projectID <= 0 {
		return nil, analyzeInvalid("project_id is required")
	}
	out, err := s.repo.ListCalculatedFields(projectID)
	if err != nil {
		return nil, s.fail("list calculated fields failed", err, "project_id", projectID)
	}
	if out == nil {
		out = []CalculatedField{}
	}
	return out, nil
}

// Create validates and stores a field and returns its id.
func (s *CalculatedFieldService) Create(projectID int64, name, expression string) (int64, error) {
	name, expression = strings.TrimSpace(name), strings.TrimSpace(expression)
	if projectID <= 0 {
		return 0, analyzeInvalid("project_id is required")
	}
	if err := validateName(name); err != nil {
		return 0, err
	}
	existing, err := s.repo.ListCalculatedFields(projectID)
	if err != nil {
		return 0, s.fail("create calculated field failed", err, "project_id", projectID)
	}
	if len(existing) >= maxFieldsPerProject {
		return 0, analyzeInvalid("a project holds at most %d calculated fields", maxFieldsPerProject)
	}
	if hasName(existing, name) {
		return 0, analyzeInvalid("%v", repository.ErrDuplicateField)
	}
	if err := validateExpression(existing, name, expression); err != nil {
		return 0, err
	}
	id, err := s.repo.CreateCalculatedField(repository.CalculatedField{ProjectID: projectID, Name: name, Expression: expression})
	if err != nil {
		return 0, s.fail("create calculated field failed", err, "project_id", projectID)
	}
	return id, nil
}

// Update validates and replaces the name and expression of a field.
func (s *CalculatedFieldService) Update(id int64, name, expression string) error {
	name, expression = strings.TrimSpace(name), strings.TrimSpace(expression)
	cur, err := s.repo.GetCalculatedField(id)
	if err != nil {
		return s.fail("update calculated field failed", err, "id", id)
	}
	if err := validateName(name); err != nil {
		return err
	}
	list, err := s.repo.ListCalculatedFields(cur.ProjectID)
	if err != nil {
		return s.fail("update calculated field failed", err, "id", id)
	}
	others := without(list, id)
	if hasName(others, name) {
		return analyzeInvalid("%v", repository.ErrDuplicateField)
	}
	if err := validateExpression(others, name, expression); err != nil {
		return err
	}
	if err := s.repo.UpdateCalculatedField(id, name, expression); err != nil {
		return s.fail("update calculated field failed", err, "id", id)
	}
	return nil
}

// Delete removes a field.
func (s *CalculatedFieldService) Delete(id int64) error {
	if err := s.repo.DeleteCalculatedField(id); err != nil {
		return s.fail("delete calculated field failed", err, "id", id)
	}
	return nil
}

// Preview evaluates an expression on the newest spans of a project without
// saving it. An empty name previews a new field.
func (s *CalculatedFieldService) Preview(ctx context.Context, projectID int64, name, expression string) ([]CalculatedFieldSample, error) {
	name, expression = strings.TrimSpace(name), strings.TrimSpace(expression)
	if projectID <= 0 {
		return nil, analyzeInvalid("project_id is required")
	}
	if name == "" {
		name = previewFieldName
	}
	if err := validateName(name); err != nil {
		return nil, err
	}
	existing, err := s.repo.ListCalculatedFields(projectID)
	if err != nil {
		return nil, s.fail("preview calculated field failed", err, "project_id", projectID)
	}
	var others []repository.CalculatedField
	for _, f := range existing {
		if f.Name != name {
			others = append(others, f)
		}
	}
	if err := validateExpression(others, name, expression); err != nil {
		return nil, err
	}
	out, err := s.repo.PreviewCalculatedField(ctx, projectID, name, expression)
	if err != nil {
		return nil, s.fail("preview calculated field failed", fmt.Errorf("project %d: %w", projectID, err))
	}
	return out, nil
}
