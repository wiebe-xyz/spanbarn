package service

import (
	"errors"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

type fakeKeyRejectionRepo struct {
	since time.Time
	rows  []repository.KeyRejection
	err   error
}

func (f *fakeKeyRejectionRepo) ListKeyRejections(since time.Time) ([]repository.KeyRejection, error) {
	f.since = since
	return f.rows, f.err
}

func TestKeyRejectionServiceReadsTheRetentionWindowByDefault(t *testing.T) {
	repo := &fakeKeyRejectionRepo{rows: []repository.KeyRejection{{Fingerprint: "1d4e4d34", Count: 3}}}
	before := time.Now()
	rows, err := NewKeyRejectionService(repo).Since(time.Time{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %+v, err = %v", rows, err)
	}
	want := before.Add(-KeyRejectionRetention)
	if repo.since.Before(want.Add(-time.Second)) || repo.since.After(time.Now().Add(-KeyRejectionRetention).Add(time.Second)) {
		t.Errorf("since = %v, want about %v", repo.since, want)
	}
}

func TestKeyRejectionServicePassesAnExplicitSinceAndTheError(t *testing.T) {
	boom := errors.New("disk")
	repo := &fakeKeyRejectionRepo{err: boom}
	from := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	if _, err := NewKeyRejectionService(repo).Since(from); !errors.Is(err, boom) {
		t.Errorf("err = %v, want the repository error", err)
	}
	if !repo.since.Equal(from) {
		t.Errorf("since = %v, want %v", repo.since, from)
	}
}
