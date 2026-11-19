package service

import (
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// KeyRejectionRepository is the storage KeyRejectionService needs.
type KeyRejectionRepository interface {
	ListKeyRejections(since time.Time) ([]repository.KeyRejection, error)
}

// KeyRejection is one refused key: how many requests used it and when. Reason
// is "revoked" for a key revoked since the counters exist (its id, project and
// name come with it) and "unknown" for any other fingerprint.
type KeyRejection = repository.KeyRejection

// KeyRejectionRetention is how long the counters keep a fingerprint after its
// newest request.
const KeyRejectionRetention = repository.KeyRejectionRetention

// KeyRejectionService reads the counters of requests made with revoked or
// unknown API keys (#572).
type KeyRejectionService struct{ repo KeyRejectionRepository }

func NewKeyRejectionService(repo KeyRejectionRepository) *KeyRejectionService {
	return &KeyRejectionService{repo: repo}
}

// Since lists the fingerprints refused at or after the given time, newest
// first. A zero time reads everything the counters keep.
func (s *KeyRejectionService) Since(since time.Time) ([]KeyRejection, error) {
	if since.IsZero() {
		since = time.Now().Add(-KeyRejectionRetention)
	}
	return s.repo.ListKeyRejections(since)
}
