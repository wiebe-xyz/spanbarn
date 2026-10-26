package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Both tiers read their (project, bucket) index, so the scan covers the
// window and never the project's whole history.
func TestScanProjectRollupsPinsTheBucketIndex(t *testing.T) {
	repo := setupTestDB(t)
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for step, want := range map[int64]string{
		300:  "USING INDEX idx_metric_rollups_bucket (project_id=? AND bucket>? AND bucket<?)",
		3600: "USING INDEX idx_metric_rollups_coarse_bucket (project_id=? AND step_seconds=? AND bucket>? AND bucket<?)",
	} {
		q, args := projectRollupScan(1, step, from, from.Add(24*time.Hour))
		rows, err := repo.DB().Query(`EXPLAIN QUERY PLAN `+q, args...)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		rows.Close()
		if joined := strings.Join(plan, " | "); !strings.Contains(joined, want) {
			t.Errorf("step %d plan %q lacks %q", step, joined, want)
		}
	}
}

// The scan streams every series in the window as contiguous runs in bucket
// order, from the tier asked for, and keeps extra only where it is read.
func TestScanProjectRollupsStreamsEverySeries(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var fine, coarse []MetricRollup
	for s := range 30 {
		typ := "gauge"
		if s%3 == 0 {
			typ = "histogram"
		}
		for b := range 4 {
			m := MetricRollup{ProjectID: 1, Name: fmt.Sprintf("m%02d", s%10), Type: typ, AttrFingerprint: fmt.Sprintf("fp%02d", s),
				Attributes: "{}", Bucket: base.Add(time.Duration(3-b) * time.Hour), Count: 1, Sum: 1, Min: 1, Max: 1, Last: 1,
				ObsCount: 1, Extra: `{"k":1}`}
			fine = append(fine, m)
			m.StepSeconds = 3600
			coarse = append(coarse, m)
		}
	}
	other := fine[0]
	other.ProjectID = 2
	late := fine[1]
	late.Bucket = base.Add(5 * time.Hour)
	if err := repo.UpsertMetricRollups(append(fine, other, late)); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertCoarseRollups(ctx, coarse); err != nil {
		t.Fatal(err)
	}

	for _, step := range []int64{300, 3600} {
		var got []MetricRollup
		if err := repo.ScanProjectRollups(ctx, 1, step, base, base.Add(3*time.Hour), func(m MetricRollup) error {
			got = append(got, m)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if len(got) != 120 {
			t.Fatalf("step %d: %d rows, want 120", step, len(got))
		}
		for i, m := range got {
			if (m.Type == "histogram") != (m.Extra != "") {
				t.Fatalf("step %d: %s row has extra %q", step, m.Type, m.Extra)
			}
			if i == 0 {
				continue
			}
			prev := got[i-1]
			key, prevKey := m.Name+"/"+m.AttrFingerprint, prev.Name+"/"+prev.AttrFingerprint
			if key < prevKey || key == prevKey && !m.Bucket.After(prev.Bucket) {
				t.Fatalf("step %d: row %d (%s %s) after (%s %s)", step, i, key, m.Bucket, prevKey, prev.Bucket)
			}
		}
	}

	stop := errors.New("stop")
	n := 0
	err := repo.ScanProjectRollups(ctx, 1, 300, base, base.Add(3*time.Hour), func(MetricRollup) error {
		n++
		return stop
	})
	if !errors.Is(err, stop) || n != 1 {
		t.Fatalf("callback error: got %v after %d rows", err, n)
	}
}
