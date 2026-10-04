package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/filter"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

type fakeFieldRepo struct {
	fields  []repository.CalculatedField
	nextID  int64
	err     error
	preview []repository.CalculatedFieldSample
	// previewed records the arguments of the last preview call.
	previewed [3]any
}

func (f *fakeFieldRepo) CreateCalculatedField(c repository.CalculatedField) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.nextID++
	c.ID = f.nextID
	f.fields = append(f.fields, c)
	return c.ID, nil
}

func (f *fakeFieldRepo) UpdateCalculatedField(id int64, name, expression string) error {
	for i := range f.fields {
		if f.fields[i].ID == id {
			f.fields[i].Name, f.fields[i].Expression = name, expression
			return f.err
		}
	}
	return repository.ErrNotFound
}

func (f *fakeFieldRepo) DeleteCalculatedField(id int64) error {
	for i := range f.fields {
		if f.fields[i].ID == id {
			f.fields = append(f.fields[:i], f.fields[i+1:]...)
			return nil
		}
	}
	return repository.ErrNotFound
}

func (f *fakeFieldRepo) GetCalculatedField(id int64) (*repository.CalculatedField, error) {
	for _, c := range f.fields {
		if c.ID == id {
			return &c, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (f *fakeFieldRepo) ListCalculatedFields(project int64) ([]repository.CalculatedField, error) {
	if f.err != nil {
		return nil, f.err
	}
	var out []repository.CalculatedField
	for _, c := range f.fields {
		if c.ProjectID == project {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeFieldRepo) PreviewCalculatedField(_ context.Context, project int64, name, expr string) ([]repository.CalculatedFieldSample, error) {
	f.previewed = [3]any{project, name, expr}
	return f.preview, f.err
}

func newFieldService() (*CalculatedFieldService, *fakeFieldRepo, *bytes.Buffer) {
	repo := &fakeFieldRepo{}
	var logs bytes.Buffer
	return NewCalculatedFieldService(repo, slog.New(slog.NewTextHandler(&logs, nil))), repo, &logs
}

func wantInvalid(t *testing.T, err error, contains string) {
	t.Helper()
	if !errors.Is(err, filter.ErrInvalid) {
		t.Fatalf("want a 400 error (filter.ErrInvalid), got %v", err)
	}
	if !strings.Contains(err.Error(), contains) {
		t.Fatalf("error %q does not mention %q", err, contains)
	}
}

func TestCalculatedFieldCreateAndList(t *testing.T) {
	svc, repo, _ := newFieldService()
	id, err := svc.Create(1, " ms ", " duration_us / 1000 ")
	if err != nil || id == 0 {
		t.Fatalf("create: %d %v", id, err)
	}
	if got := repo.fields[0]; got.Name != "ms" || got.Expression != "duration_us / 1000" || got.ProjectID != 1 {
		t.Fatalf("stored %+v", got)
	}
	list, err := svc.List(1)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %v", list, err)
	}
	if empty, err := svc.List(2); err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty list must be [] not null: %#v %v", empty, err)
	}
}

func TestCalculatedFieldCreateRejects(t *testing.T) {
	svc, repo, _ := newFieldService()
	for i := 0; i < maxFieldsPerProject; i++ {
		repo.fields = append(repo.fields, repository.CalculatedField{ID: int64(100 + i), ProjectID: 9, Name: fmt.Sprintf("f%d", i), Expression: "1"})
	}
	repo.fields = append(repo.fields, repository.CalculatedField{ID: 1, ProjectID: 1, Name: "taken", Expression: "1"})
	repo.fields = append(repo.fields, repository.CalculatedField{ID: 2, ProjectID: 1, Name: "a", Expression: "1"})
	tests := []struct {
		name       string
		project    int64
		field, src string
		contains   string
	}{
		{"no project", 0, "x", "1", "project_id"},
		{"empty name", 1, "", "1", "name is"},
		{"name with space", 1, "a b", "1", "name is"},
		{"name with quote", 1, `a"b`, "1", "name is"},
		{"name with semicolon", 1, "a;b", "1", "name is"},
		{"digit first", 1, "1a", "1", "name is"},
		{"long name", 1, strings.Repeat("a", maxFieldName+1), "1", "name is"},
		{"shadows column", 1, "duration_us", "1", "span column"},
		{"shadows alias", 1, "operation", "1", "span column"},
		{"attributes prefix", 1, "attributes.x", "1", "span column"},
		{"duplicate", 1, "taken", "1", "already exists"},
		{"empty expression", 1, "x", " ", "empty"},
		{"unknown function", 1, "x", "sum(duration_us)", "unknown function"},
		{"raw sql", 1, "x", "1; DROP TABLE spans", "unexpected character"},
		{"subquery", 1, "x", "(SELECT 1)", "closing parenthesis"},
		{"too long", 1, "x", strings.Repeat("1 + ", 200) + "1", "longer than"},
		{"too deep", 1, "x", strings.Repeat("(", 20) + "1" + strings.Repeat(")", 20), "nests deeper"},
		{"self cycle", 1, "x", "x + 1", "cycle"},
		{"cycle through another", 1, "b", "a + 1", "cycle"},
		{"project full", 9, "extra", "1", "at most"},
	}
	// "a" refers to "b", so defining "b" as a + 1 closes a loop.
	repo.fields[len(repo.fields)-1].Expression = "b + 1"
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := len(repo.fields)
			_, err := svc.Create(tc.project, tc.field, tc.src)
			wantInvalid(t, err, tc.contains)
			if len(repo.fields) != before {
				t.Fatal("an invalid field reached storage")
			}
		})
	}
}

func TestCalculatedFieldUpdate(t *testing.T) {
	svc, repo, _ := newFieldService()
	a, _ := svc.Create(1, "a", "duration_us")
	b, _ := svc.Create(1, "b", "a + 1")
	if _, err := svc.Create(2, "a", "1"); err != nil {
		t.Fatalf("same name in another project: %v", err)
	}

	if err := svc.Update(a, "a", "duration_us * 2"); err != nil {
		t.Fatalf("update own expression: %v", err)
	}
	if repo.fields[0].Expression != "duration_us * 2" {
		t.Fatalf("stored %+v", repo.fields[0])
	}
	// Making a depend on b closes the loop a -> b -> a.
	wantInvalid(t, svc.Update(a, "a", "b * 2"), "cycle")
	wantInvalid(t, svc.Update(b, "a", "1"), "already exists")
	wantInvalid(t, svc.Update(b, "name", "1"), "span column")
	wantInvalid(t, svc.Update(b, "b", "1 +"), "unexpected")
	if err := svc.Update(999, "x", "1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if repo.fields[0].Expression != "duration_us * 2" {
		t.Fatal("a rejected update changed storage")
	}
}

func TestCalculatedFieldDelete(t *testing.T) {
	svc, repo, _ := newFieldService()
	id, _ := svc.Create(1, "a", "1")
	if err := svc.Delete(id); err != nil || len(repo.fields) != 0 {
		t.Fatalf("delete: %v", err)
	}
	if err := svc.Delete(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
}

func TestCalculatedFieldPreview(t *testing.T) {
	svc, repo, _ := newFieldService()
	repo.preview = []repository.CalculatedFieldSample{{SpanID: "s1", Name: "op", Value: 2.5}}
	got, err := svc.Preview(context.Background(), 1, "", "duration_us / 1000")
	if err != nil || len(got) != 1 {
		t.Fatalf("preview: %v %v", got, err)
	}
	if repo.previewed != [3]any{int64(1), "preview", "duration_us / 1000"} {
		t.Fatalf("repo called with %v", repo.previewed)
	}
	_, err = svc.Preview(context.Background(), 1, "x", "nope(1)")
	wantInvalid(t, err, "unknown function")
	_, err = svc.Preview(context.Background(), 1, "name", "1")
	wantInvalid(t, err, "span column")
	_, err = svc.Preview(context.Background(), 0, "x", "1")
	wantInvalid(t, err, "project_id")
}

func TestCalculatedFieldServerErrorsAreLogged(t *testing.T) {
	svc, repo, logs := newFieldService()
	repo.err = errors.New("disk is gone")
	if _, err := svc.Create(1, "a", "1"); err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(logs.String(), "level=ERROR") {
		t.Fatalf("a storage failure must log at error level: %q", logs.String())
	}
	logs.Reset()
	repo.err = nil
	_, _ = svc.Create(1, "a b", "1")
	if logs.Len() != 0 {
		t.Fatalf("a validation error must not log: %q", logs.String())
	}
	repo.err = repository.ErrDuplicateField
	_, err := svc.Create(1, "dup", "1")
	wantInvalid(t, err, "already exists")
}
