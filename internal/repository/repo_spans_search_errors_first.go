package repository

import "fmt"

// traceSummaryColumns are the columns a trace list row is scanned from.
const traceSummaryColumns = `trace_id, start_time_us, span_count, has_error, root_name, root_service, root_duration_us, has_root, orphan_count`

// errorsFirstSummaryQuery lists error traces before the rest, each group
// newest first (#457). ORDER BY has_error DESC, ingested_at DESC has no index,
// so SQLite read every summary in the window and sorted them in a temp B-tree
// to return one page. The page lies inside the first offset+limit rows of each
// group, so each group is read in index order up to that bound: the error rows
// from the partial error indexes, the others from idx_trace_summaries_list or
// idx_trace_summaries_clean. The union has at most 2*(offset+limit) rows to
// sort. The caller passes the filter's arguments twice, once per group.
func errorsFirstSummaryQuery(whereSQL string, limit, offset int) string {
	bound := limit + offset
	group := func(hasError int) string {
		cond := fmt.Sprintf("has_error = %d", hasError)
		if whereSQL == "" {
			cond = " WHERE " + cond
		} else {
			cond = whereSQL + " AND " + cond
		}
		return fmt.Sprintf(`SELECT * FROM (SELECT %s, ingested_at FROM trace_summaries%s ORDER BY ingested_at DESC LIMIT %d)`,
			traceSummaryColumns, cond, bound)
	}
	return fmt.Sprintf(`SELECT %s FROM (%s UNION ALL %s) ORDER BY has_error DESC, ingested_at DESC LIMIT %d OFFSET %d`,
		traceSummaryColumns, group(1), group(0), limit, offset)
}
