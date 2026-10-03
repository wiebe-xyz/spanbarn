package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/cache"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// ListDatabaseQueries extracts database query patterns from client spans
// and returns aggregated performance metrics per normalized query.
func (s *QueryService) ListDatabaseQueries(ctx context.Context, projectID int64, from, to time.Time, svcFilter string) ([]DatabaseQuerySummary, error) {
	_, span := tracer.Start(ctx, "query.list_database_queries")
	defer span.End()

	cacheKey := fmt.Sprintf("dbq:%d:%s:%d:%d", projectID, svcFilter, from.Truncate(time.Minute).Unix(), to.Truncate(time.Minute).Unix())
	if cached, ok := cache.Get[[]DatabaseQuerySummary](s.cache, ctx, cacheKey); ok {
		return cached, nil
	}

	sf := repository.SpanFilter{
		ProjectID: projectID,
		Service:   svcFilter,
		From:      from,
		To:        to,
		Limit:     10000,
	}

	spans, err := s.repo.QuerySpans(sf)
	if err != nil {
		return nil, err
	}

	type queryStats struct {
		operation  string
		dbSystem   string
		dbName     string
		count      int64
		errorCount int64
		totalTime  int64
		durations  []int64
	}
	byPattern := make(map[string]*queryStats)

	for _, sp := range spans {
		if sp.Kind != "client" && sp.Kind != "CLIENT" {
			continue
		}

		attrs := parseAttrs(sp.Attributes)
		if attrs == nil {
			continue
		}

		dbSystem, _ := getStringAttr(attrs, "db.system")
		if dbSystem == "" {
			continue
		}

		statement, _ := getStringAttr(attrs, "db.statement")
		var pattern string
		if statement != "" {
			pattern = NormalizeSQL(statement)
		} else {
			pattern = strings.ToLower(sp.Name)
		}
		if pattern == "" {
			continue
		}

		operation, _ := getStringAttr(attrs, "db.operation")
		if operation == "" {
			operation = extractSQLOperation(pattern)
		}
		dbName, _ := getStringAttr(attrs, "db.name")

		st, ok := byPattern[pattern]
		if !ok {
			st = &queryStats{
				operation: strings.ToUpper(operation),
				dbSystem:  dbSystem,
				dbName:    dbName,
			}
			byPattern[pattern] = st
		}
		st.count++
		if sp.Status == "error" {
			st.errorCount++
		}
		st.totalTime += sp.DurationUs
		st.durations = append(st.durations, sp.DurationUs)
	}

	sr := s.projectSampleRate(ctx, projectID)
	result := make([]DatabaseQuerySummary, 0, len(byPattern))
	for pattern, st := range byPattern {
		effective := inflateCount(st.count, st.errorCount, sr)
		var errorRate float64
		if effective > 0 {
			errorRate = float64(st.errorCount) / float64(effective)
		}
		p50, p95, p99 := computePercentiles(st.durations)
		result = append(result, DatabaseQuerySummary{
			Pattern:     pattern,
			Operation:   st.operation,
			DBSystem:    st.dbSystem,
			DBName:      st.dbName,
			CallCount:   effective,
			ErrorCount:  st.errorCount,
			ErrorRate:   errorRate,
			P50Us:       p50,
			P95Us:       p95,
			P99Us:       p99,
			TotalTimeUs: st.totalTime,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].TotalTimeUs > result[j].TotalTimeUs
	})

	cache.Set(s.cache, ctx, cacheKey, result)
	return result, nil
}

// GetDatabaseQuerySpans returns individual span executions matching a specific
// normalized SQL pattern, enriched with caller context (parent span name +
// service) so you can see which operation triggered each query.
func (s *QueryService) GetDatabaseQuerySpans(ctx context.Context, projectID int64, from, to time.Time, pattern, svcFilter string) ([]DatabaseQuerySpan, error) {
	_, span := tracer.Start(ctx, "query.get_database_query_spans")
	defer span.End()

	sf := repository.SpanFilter{
		ProjectID: projectID,
		Service:   svcFilter,
		From:      from,
		To:        to,
		Limit:     1000,
	}

	spans, err := s.repo.QuerySpans(sf)
	if err != nil {
		return nil, err
	}

	// First pass: collect matching spans and parent IDs to look up.
	type match struct {
		sp           repository.Span
		errorMessage string
	}
	var matches []match
	var parentIDs []string

	for _, sp := range spans {
		errMsg, ok := matchDatabaseQuerySpan(sp, pattern)
		if !ok {
			continue
		}
		matches = append(matches, match{sp: sp, errorMessage: errMsg})
		if sp.ParentSpanID != "" {
			parentIDs = append(parentIDs, sp.ParentSpanID)
		}
	}

	// Second pass: batch-fetch parent spans to get caller name + service.
	callerBySpanID := s.callerSpans(parentIDs)

	result := make([]DatabaseQuerySpan, 0, len(matches))
	for _, m := range matches {
		dqs := DatabaseQuerySpan{
			SpanID:       m.sp.SpanID,
			TraceID:      m.sp.TraceID,
			ParentSpanID: m.sp.ParentSpanID,
			Service:      m.sp.Service,
			DurationUs:   m.sp.DurationUs,
			Status:       m.sp.Status,
			ErrorMessage: m.errorMessage,
			StartTimeUs:  m.sp.StartTimeUs,
			IngestedAt:   m.sp.IngestedAt.Format(time.RFC3339),
		}
		if caller, ok := callerBySpanID[m.sp.ParentSpanID]; ok {
			dqs.CallerName = caller.Name
			dqs.CallerService = caller.Service
		}
		result = append(result, dqs)
	}
	return result, nil
}

// matchDatabaseQuerySpan reports whether sp is a database client span whose
// normalized statement (or lowercased name without a statement) equals pattern,
// and returns the error message the span carries, if any.
func matchDatabaseQuerySpan(sp repository.Span, pattern string) (errorMessage string, ok bool) {
	if !isClientKind(sp.Kind) {
		return "", false
	}
	attrs := parseAttrs(sp.Attributes)
	if attrs == nil {
		return "", false
	}
	if dbSystem, _ := getStringAttr(attrs, "db.system"); dbSystem == "" {
		return "", false
	}
	p := strings.ToLower(sp.Name)
	if statement, _ := getStringAttr(attrs, "db.statement"); statement != "" {
		p = NormalizeSQL(statement)
	}
	if p != pattern {
		return "", false
	}
	return firstStringAttr(attrs, "exception.message", "error.message", "db.error.message"), true
}

// firstStringAttr returns the first non-empty string attribute among keys.
func firstStringAttr(attrs map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, _ := getStringAttr(attrs, k); v != "" {
			return v
		}
	}
	return ""
}

// callerSpans fetches the spans with the given ids by span id. A failing lookup
// leaves the callers empty.
func (s *QueryService) callerSpans(spanIDs []string) map[string]repository.Span {
	callers := make(map[string]repository.Span)
	if len(spanIDs) == 0 {
		return callers
	}
	parents, err := s.repo.GetSpansBySpanIDs(spanIDs)
	if err != nil {
		return callers
	}
	for _, p := range parents {
		callers[p.SpanID] = p
	}
	return callers
}

func parseAttrs(attrJSON string) map[string]any {
	if attrJSON == "" || attrJSON == "{}" {
		return nil
	}
	var attrs map[string]any
	if err := json.Unmarshal([]byte(attrJSON), &attrs); err != nil {
		return nil
	}
	return attrs
}

func extractSQLOperation(pattern string) string {
	pattern = strings.TrimSpace(pattern)
	if i := strings.IndexByte(pattern, ' '); i > 0 {
		return pattern[:i]
	}
	return pattern
}
