package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/repository/engine"
)

func newRejectionTestRepo(t *testing.T) *repository.Repository {
	t.Helper()
	db, err := engine.NewDB(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := engine.MigrateSingleFile(context.Background(), db.DB); err != nil {
		t.Fatal(err)
	}
	return repository.NewRepository(db.DB)
}

// A request with a revoked key raises its counter: the authorizer reports it,
// the pod's counter hands it on, the table counts it and reads it back as a
// revoked key (#572).
func TestRevokedKeyRequestRaisesItsCounter(t *testing.T) {
	repo := newRejectionTestRepo(t)
	project, err := repo.CreateProject("shop", "Shop")
	if err != nil {
		t.Fatal(err)
	}
	key, hash, err := auth.NewAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	id, err := repo.CreateAPIKey(project.ID, "site", hash, "ingest")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.RevokeAPIKey(id); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	authorizer := auth.NewAuthorizer("", &keyLookupAdapter{repo: repo}, logger)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	startKeyRejectionCounter(ctx, &wg, authorizer, storeKeyRejections(repo), logger)

	for i := 0; i < 3; i++ {
		if _, err := authorizer.AuthorizeIdentity(key); !errors.Is(err, auth.ErrUnauthorized) {
			t.Fatalf("revoked key accepted: %v", err)
		}
	}
	cancel() // stopping the pod makes the last hand-over
	wg.Wait()

	rows, err := repo.ListKeyRejections(time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Count != 3 || !rows[0].Revoked || rows[0].KeyID != id {
		t.Errorf("counters = %+v, want 3 requests of revoked key %d", rows, id)
	}
}

type fakeRejectionSource struct {
	batches [][]model.KeyRejection
	errs    []error
}

func (f *fakeRejectionSource) ConsumeKeyRejections(ctx context.Context) ([]model.KeyRejection, error) {
	if len(f.errs) > 0 {
		err := f.errs[0]
		f.errs = f.errs[1:]
		return nil, err
	}
	if len(f.batches) == 0 {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	b := f.batches[0]
	f.batches = f.batches[1:]
	return b, nil
}

type recordingRejectionSink struct {
	mu   sync.Mutex
	rows []model.KeyRejection
	fail bool
	done chan struct{}
}

func (r *recordingRejectionSink) AddKeyRejections(rows []model.KeyRejection, _ time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, rows...)
	if len(r.rows) >= 2 {
		select {
		case <-r.done:
		default:
			close(r.done)
		}
	}
	if r.fail {
		return errors.New("disk full")
	}
	return nil
}

func TestRunKeyRejectionConsumerStoresBatchesAndSurvivesAFailedStore(t *testing.T) {
	src := &fakeRejectionSource{batches: [][]model.KeyRejection{
		{{Fingerprint: "aaaaaaaa", Count: 2}},
		{},
		{{Fingerprint: "bbbbbbbb", Count: 1}},
	}}
	sink := &recordingRejectionSink{fail: true, done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan struct{})
	go func() {
		runKeyRejectionConsumer(ctx, src, sink, slog.New(slog.NewTextHandler(io.Discard, nil)))
		close(finished)
	}()
	select {
	case <-sink.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the consumer stopped after a failed store, or never stored")
	}
	cancel()
	<-finished
	if len(sink.rows) != 2 {
		t.Errorf("stored %+v, want both non-empty batches", sink.rows)
	}
}
