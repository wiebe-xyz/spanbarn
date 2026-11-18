package repository

import (
	"context"
	"errors"
	"testing"
)

func archivedAt(t *testing.T, repo *Repository, id int64) bool {
	t.Helper()
	return mergeCount(t, repo, `SELECT count(*) FROM projects WHERE id = ? AND archived_at IS NOT NULL`, id) == 1
}

// A project that holds nothing is archived, and doing it twice changes nothing.
func TestArchiveEmptyProjectArchivesAProjectThatHoldsNothing(t *testing.T) {
	repo := setupTestDB(t)
	p, err := repo.CreateProject("status", "status")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := repo.ArchiveEmptyProject(context.Background(), p.ID); err != nil {
			t.Fatalf("archive %d: %v", i, err)
		}
	}
	if !archivedAt(t, repo, p.ID) {
		t.Error("project is not archived")
	}
}

// A key, or a row in a core table, keeps the project: it refuses and the
// project stays unarchived.
func TestArchiveEmptyProjectRefusesAProjectWithKeysOrRows(t *testing.T) {
	f := newMergeFixture(t)
	err := f.repo.ArchiveEmptyProject(context.Background(), f.source.ID)
	if !errors.Is(err, ErrProjectNotEmpty) {
		t.Fatalf("archive with keys = %v, want ErrProjectNotEmpty", err)
	}
	if archivedAt(t, f.repo, f.source.ID) {
		t.Error("a refused project was archived")
	}

	empty, err := f.repo.CreateProject("scanoo", "scanoo")
	if err != nil {
		t.Fatal(err)
	}
	mergeExec(t, f.repo, `INSERT INTO span_ledger (bucket, project_id, environment, accepted) VALUES (1, ?, '', 1)`, empty.ID)
	if err := f.repo.ArchiveEmptyProject(context.Background(), empty.ID); !errors.Is(err, ErrProjectNotEmpty) {
		t.Fatalf("archive with a ledger row = %v, want ErrProjectNotEmpty", err)
	}
}
