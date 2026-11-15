package repository

import "github.com/wiebe-xyz/spanbarn/internal/repository/engine"

// ForStorage returns a repository that reads through s.Main and routes every
// family's writes to the handle of the file that holds it.
func ForStorage(s *engine.Storage) *Repository {
	repo := NewRepository(s.Main.DB)
	if s.Spans != nil {
		repo.SetFamilyWriter(engine.FamilySpans, s.Spans.DB, nil)
	}
	if s.Shards != nil {
		repo.SetShards(s.Shards)
	}
	return repo
}
