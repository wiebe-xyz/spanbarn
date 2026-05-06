package service

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// ListDatabaseQueries extracts database query patterns from client spans
// and returns aggregated performance metrics per normalized query.
func (s *QueryService) ListDatabaseQueries(ctx context.Context, projectID int64, from, to time.Time, svcFilter string) ([]DatabaseQuerySummary, error) {
	_, span := tracer.Start(ctx, "query.list_database_queries")
	defer span.End()

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

	result := make([]DatabaseQuerySummary, 0, len(byPattern))
	for pattern, st := range byPattern {
		var errorRate float64
		if st.count > 0 {
			errorRate = float64(st.errorCount) / float64(st.count)
		}
		p50, p95, p99 := computePercentiles(st.durations)
		result = append(result, DatabaseQuerySummary{
			Pattern:     pattern,
			Operation:   st.operation,
			DBSystem:    st.dbSystem,
			DBName:      st.dbName,
			CallCount:   st.count,
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

	return result, nil
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
