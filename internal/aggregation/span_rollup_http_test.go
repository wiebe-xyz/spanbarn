package aggregation

import (
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

func TestHTTPStatusOf(t *testing.T) {
	for attrs, want := range map[string]int64{
		`{"http.response.status_code":503}`:                          503,
		`{"http.response.status_code": "404", "x": 1}`:               404,
		`{"http.status_code":200}`:                                   200,
		`{"http.status_code":200,"http.response.status_code":502}`:   502,
		`{"http.response.status_code":"abc","http.status_code":201}`: 201,
		`{"http.response.status_code":12345}`:                        0,
		`{"http.method":"GET"}`:                                      0,
		``:                                                           0,
		`not json`:                                                   0,
	} {
		if got := httpStatusOf(attrs); got != want {
			t.Errorf("httpStatusOf(%q) = %d, want %d", attrs, got, want)
		}
	}
}

// Root spans split by response code, child spans never do, and the flag off
// writes 0 for every span.
func TestSpanRollupsSplitRootsByHTTPStatus(t *testing.T) {
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	spans := []repository.Span{
		{ProjectID: 1, Service: "api", Name: "GET /", Kind: "server", Attributes: `{"http.response.status_code":200}`, DurationUs: 1, IngestedAt: at},
		{ProjectID: 1, Service: "api", Name: "GET /", Kind: "server", Attributes: `{"http.response.status_code":500}`, DurationUs: 1, IngestedAt: at},
		{ProjectID: 1, Service: "api", Name: "GET /", Kind: "server", Attributes: `{"http.response.status_code":500}`, DurationUs: 1, IngestedAt: at},
		{ProjectID: 1, Service: "api", Name: "GET /", Kind: "client", ParentSpanID: "p", Attributes: `{"http.response.status_code":404}`, DurationUs: 1, IngestedAt: at},
	}
	for _, on := range []bool{true, false} {
		w := &fakeSpanRollupWriter{}
		r := newTestRollups(w, SpanRollupConfig{HTTPStatus: on}, at)
		for _, s := range spans {
			r.Add(s)
		}
		if err := r.Flush(); err != nil {
			t.Fatal(err)
		}
		codes := map[int64]int64{}
		for _, row := range w.rows {
			if !row.IsRoot && row.HTTPStatus != 0 {
				t.Errorf("child row carries code %d", row.HTTPStatus)
			}
			if row.IsRoot {
				codes[row.HTTPStatus] += row.Count
			}
		}
		want := map[int64]int64{200: 1, 500: 2}
		if !on {
			want = map[int64]int64{0: 3}
		}
		if len(codes) != len(want) {
			t.Fatalf("on=%v: root codes %v, want %v", on, codes, want)
		}
		for c, n := range want {
			if codes[c] != n {
				t.Errorf("on=%v: code %d count %d, want %d", on, c, codes[c], n)
			}
		}
	}
}
