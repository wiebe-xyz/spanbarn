package queue

import (
	"context"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

func TestKeyRejectionsTravelThroughTheQueue(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()
	want := []model.KeyRejection{{Fingerprint: "aaaaaaaa", Count: 3, LastSeen: 1795000000}, {Fingerprint: "bbbbbbbb", Count: 1, LastSeen: 1795000100}}
	if err := q.PublishKeyRejections(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := q.ConsumeKeyRejections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("consumed %+v, want %+v", got, want)
	}
}
