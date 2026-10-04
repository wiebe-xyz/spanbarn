package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/calcfield"
	"github.com/wiebe-xyz/spanbarn/internal/calcresolve"
	"github.com/wiebe-xyz/spanbarn/internal/filter"
)

// ErrDuplicateField reports a calculated field name the project already uses.
var ErrDuplicateField = errors.New("a calculated field with this name already exists")

// CalculatedField is a named expression of one project. Filters and group-bys
// use Name like a span column or attribute key.
type CalculatedField struct {
	ID         int64     `json:"id"`
	ProjectID  int64     `json:"projectId"`
	Name       string    `json:"name"`
	Expression string    `json:"expression"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

const calculatedFieldColumns = `id, project_id, name, expression, created_at, updated_at`

func scanCalculatedField(s rowScanner) (CalculatedField, error) {
	var f CalculatedField
	err := s.Scan(&f.ID, &f.ProjectID, &f.Name, &f.Expression, &f.CreatedAt, &f.UpdatedAt)
	return f, err
}

func duplicateField(err error) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return ErrDuplicateField
	}
	return err
}

// CreateCalculatedField stores a field. The caller validates it.
func (r *Repository) CreateCalculatedField(f CalculatedField) (int64, error) {
	var id int64
	err := r.execHigh(FamilyCore, func(db *sql.DB) error {
		res, e := db.Exec(
			`INSERT INTO calculated_fields (project_id, name, expression) VALUES (?, ?, ?)`,
			f.ProjectID, f.Name, f.Expression,
		)
		if e != nil {
			return e
		}
		id, _ = res.LastInsertId()
		return nil
	})
	return id, duplicateField(err)
}

// UpdateCalculatedField replaces the name and expression of a field.
func (r *Repository) UpdateCalculatedField(id int64, name, expression string) error {
	err := r.execHigh(FamilyCore, func(db *sql.DB) error {
		res, e := db.Exec(
			`UPDATE calculated_fields SET name = ?, expression = ?, updated_at = datetime('now') WHERE id = ?`,
			name, expression, id,
		)
		return expectRow(res, e)
	})
	return duplicateField(err)
}

// DeleteCalculatedField removes a field.
func (r *Repository) DeleteCalculatedField(id int64) error {
	return r.execHigh(FamilyCore, func(db *sql.DB) error {
		res, err := db.Exec(`DELETE FROM calculated_fields WHERE id = ?`, id)
		return expectRow(res, err)
	})
}

// GetCalculatedField returns one field.
func (r *Repository) GetCalculatedField(id int64) (*CalculatedField, error) {
	f, err := scanCalculatedField(r.db.QueryRow(
		`SELECT `+calculatedFieldColumns+` FROM calculated_fields WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// ListCalculatedFields returns the fields of one project by name.
func (r *Repository) ListCalculatedFields(projectID int64) ([]CalculatedField, error) {
	rows, err := r.db.Query(
		`SELECT `+calculatedFieldColumns+` FROM calculated_fields WHERE project_id = ? ORDER BY name`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CalculatedField
	for rows.Next() {
		f, err := scanCalculatedField(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func calcFields(fs []CalculatedField) []calcfield.Field {
	out := make([]calcfield.Field, len(fs))
	for i, f := range fs {
		out[i] = calcfield.Field{Name: f.Name, Expression: f.Expression}
	}
	return out
}

// calcResolver loads the calculated fields of one project once per query. It
// returns a nil Resolver for a project without any, and for projectID 0, which
// spans every project and so has no fields to resolve.
func (r *Repository) calcResolver(projectID int64) (filter.Resolver, error) {
	if projectID == 0 {
		return nil, nil
	}
	fields, err := r.ListCalculatedFields(projectID)
	if err != nil || len(fields) == 0 {
		return nil, err
	}
	return calcresolve.Resolver(calcresolve.NewSet(calcFields(fields))), nil
}

// CalculatedFieldSample is the value of a field for one recent span.
type CalculatedFieldSample struct {
	SpanID string `json:"spanId"`
	Name   string `json:"name"`
	Value  any    `json:"value"`
}

const previewSpans = 5

// PreviewCalculatedField evaluates a field, as it would be saved under name,
// on the newest spans of the project. The project's other fields stay
// available to it, and a field of the same name is replaced.
func (r *Repository) PreviewCalculatedField(ctx context.Context, projectID int64, name, expression string) ([]CalculatedFieldSample, error) {
	existing, err := r.ListCalculatedFields(projectID)
	if err != nil {
		return nil, err
	}
	fields := make([]calcfield.Field, 0, len(existing)+1)
	for _, f := range existing {
		if f.Name != name {
			fields = append(fields, calcfield.Field{Name: f.Name, Expression: f.Expression})
		}
	}
	fields = append(fields, calcfield.Field{Name: name, Expression: expression})
	res, err := calcresolve.Resolver(calcresolve.NewSet(fields))(name)
	if err != nil {
		return nil, err
	}
	if res == nil {
		return nil, fmt.Errorf("%w: %q is a span column", filter.ErrInvalid, name)
	}
	ctx, cancel := context.WithTimeout(ctx, r.queryTimeout)
	defer cancel()
	rows, err := r.db.QueryContext(ctx,
		`SELECT span_id, name, `+res.SQL+` FROM spans WHERE project_id = ? ORDER BY ingested_at DESC LIMIT ?`,
		append(res.Args, projectID, previewSpans)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CalculatedFieldSample{}
	for rows.Next() {
		var s CalculatedFieldSample
		if err := rows.Scan(&s.SpanID, &s.Name, &s.Value); err != nil {
			return nil, err
		}
		if b, ok := s.Value.([]byte); ok {
			s.Value = string(b)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
