package repository

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type SpanBucket struct {
	Bucket     time.Time
	Count      int64
	ErrorCount int64
	P50Us      int64
	P95Us      int64
	P99Us      int64
}

func (r *Repository) QuerySpanTimeseries(projectID int64, service, operation string, from, to time.Time, intervalSec int64) ([]SpanBucket, error) {
	var where []string
	var args []any

	where = append(where, "service = ?", "name = ?")
	args = append(args, service, operation)

	if projectID != 0 {
		where = append(where, "project_id = ?")
		args = append(args, projectID)
	}
	if !from.IsZero() {
		where = append(where, "ingested_at >= ?")
		args = append(args, from)
	}
	if !to.IsZero() {
		where = append(where, "ingested_at <= ?")
		args = append(args, to)
	}

	whereClause := strings.Join(where, " AND ")
	bucketExpr := fmt.Sprintf("datetime((%s / %d) * %d, 'unixepoch')", ingestedEpochSQL, intervalSec, intervalSec)

	q := fmt.Sprintf(`SELECT %s as bucket, duration_us, status FROM spans WHERE %s ORDER BY bucket, duration_us`, bucketExpr, whereClause)

	ctx, cancel := r.queryContext()
	defer cancel()
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type rawBucket struct {
		durations  []int64
		errorCount int64
	}
	byBucket := make(map[string]*rawBucket)
	var bucketOrder []string
	for rows.Next() {
		var bucketStr, status string
		var durationUs int64
		if err := rows.Scan(&bucketStr, &durationUs, &status); err != nil {
			return nil, err
		}
		b, ok := byBucket[bucketStr]
		if !ok {
			b = &rawBucket{}
			byBucket[bucketStr] = b
			bucketOrder = append(bucketOrder, bucketStr)
		}
		b.durations = append(b.durations, durationUs)
		if status == "error" || status == "ERROR" || status == "Error" {
			b.errorCount++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := make([]SpanBucket, 0, len(byBucket))
	for _, bucketStr := range bucketOrder {
		b := byBucket[bucketStr]
		t, _ := time.Parse("2006-01-02 15:04:05", bucketStr)
		result = append(result, SpanBucket{
			Bucket:     t,
			Count:      int64(len(b.durations)),
			ErrorCount: b.errorCount,
			P50Us:      percentileFromSorted(b.durations, 50),
			P95Us:      percentileFromSorted(b.durations, 95),
			P99Us:      percentileFromSorted(b.durations, 99),
		})
	}
	return result, nil
}

type WebVitalRow struct {
	Service    string
	Page       string
	Metric     string
	ValueUs    int64
	Rating     string
	IngestedAt time.Time
}

func (r *Repository) QueryWebVitals(service string, from, to time.Time) ([]WebVitalRow, error) {
	var where []string
	var args []any

	where = append(where, "name LIKE 'webvital.%'")
	if service != "" {
		where = append(where, "service = ?")
		args = append(args, service)
	}
	if !from.IsZero() {
		where = append(where, "ingested_at >= ?")
		args = append(args, from)
	}
	if !to.IsZero() {
		where = append(where, "ingested_at <= ?")
		args = append(args, to)
	}

	q := `SELECT service, name, duration_us, attributes, ingested_at FROM spans WHERE ` + strings.Join(where, " AND ") + ` ORDER BY ingested_at DESC LIMIT 10000`

	ctx, cancel := r.queryContext()
	defer cancel()
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []WebVitalRow
	for rows.Next() {
		var svc, name, attrsJSON string
		var durUs int64
		var ingestedAt time.Time
		if err := rows.Scan(&svc, &name, &durUs, &attrsJSON, &ingestedAt); err != nil {
			return nil, err
		}
		metric := strings.TrimPrefix(name, "webvital.")

		var attrs map[string]any
		_ = json.Unmarshal([]byte(attrsJSON), &attrs)

		page, _ := attrs["webvital.page"].(string)
		rating, _ := attrs["webvital.rating"].(string)
		if page == "" {
			page = "/"
		}

		result = append(result, WebVitalRow{
			Service:    svc,
			Page:       page,
			Metric:     metric,
			ValueUs:    durUs,
			Rating:     rating,
			IngestedAt: ingestedAt,
		})
	}
	return result, rows.Err()
}

// WebVitalBucket holds bucketed web vital metrics for timeseries display.
type WebVitalBucket struct {
	Bucket  time.Time
	Page    string
	Metric  string
	P50Us   int64
	P95Us   int64
	Samples int64
	Good    int64
	NI      int64
	Poor    int64
}

// QueryWebVitalsTimeseries returns time-bucketed web vital data for a specific page and metric.
func (r *Repository) QueryWebVitalsTimeseries(service, page, metric string, from, to time.Time, intervalSec int64) ([]WebVitalBucket, error) {
	var where []string
	var args []any

	where = append(where, "name = ?")
	args = append(args, "webvital."+metric)

	if service != "" {
		where = append(where, "service = ?")
		args = append(args, service)
	}

	if !from.IsZero() {
		where = append(where, "ingested_at >= ?")
		args = append(args, from)
	}
	if !to.IsZero() {
		where = append(where, "ingested_at <= ?")
		args = append(args, to)
	}

	whereClause := strings.Join(where, " AND ")
	bucketExpr := fmt.Sprintf("datetime((%s / %d) * %d, 'unixepoch')", ingestedEpochSQL, intervalSec, intervalSec)

	q := fmt.Sprintf(`SELECT %s as bucket, duration_us, attributes FROM spans WHERE %s ORDER BY bucket, duration_us`, bucketExpr, whereClause)

	ctx, cancel := r.queryContext()
	defer cancel()
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type rawBucket struct {
		values         []int64
		good, ni, poor int64
	}
	byBucket := make(map[string]*rawBucket)
	var bucketOrder []string

	for rows.Next() {
		var bucketStr, attrsJSON string
		var durationUs int64
		if err := rows.Scan(&bucketStr, &durationUs, &attrsJSON); err != nil {
			return nil, err
		}

		var attrs map[string]any
		_ = json.Unmarshal([]byte(attrsJSON), &attrs)

		spanPage, _ := attrs["webvital.page"].(string)
		if spanPage == "" {
			spanPage = "/"
		}
		if page != "" && spanPage != page {
			continue
		}

		rating, _ := attrs["webvital.rating"].(string)

		b, ok := byBucket[bucketStr]
		if !ok {
			b = &rawBucket{}
			byBucket[bucketStr] = b
			bucketOrder = append(bucketOrder, bucketStr)
		}
		b.values = append(b.values, durationUs)
		switch rating {
		case "good":
			b.good++
		case "needs-improvement":
			b.ni++
		default:
			b.poor++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := make([]WebVitalBucket, 0, len(byBucket))
	for _, bucketStr := range bucketOrder {
		b := byBucket[bucketStr]
		t, _ := time.Parse("2006-01-02 15:04:05", bucketStr)
		result = append(result, WebVitalBucket{
			Bucket:  t,
			Page:    page,
			Metric:  metric,
			P50Us:   percentileFromSorted(b.values, 50),
			P95Us:   percentileFromSorted(b.values, 95),
			Samples: int64(len(b.values)),
			Good:    b.good,
			NI:      b.ni,
			Poor:    b.poor,
		})
	}
	return result, nil
}
