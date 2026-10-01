package repository

import (
	"errors"
	"testing"
	"time"
)

func TestDeleteProjectRemovesItsBoards(t *testing.T) {
	repo, projectID, board := boardFixture(t)
	addPanel(t, repo, board, "a")
	if _, err := repo.CreateRelease(projectID, "v1", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteProject(projectID); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	if _, err := repo.GetBoard(board); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetBoard err = %v, want ErrNotFound", err)
	}
	if n := queryCount(t, repo, ""); n != 0 {
		t.Fatalf("saved queries left = %d", n)
	}
}
