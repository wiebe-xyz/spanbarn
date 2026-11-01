package repository

import (
	"errors"
	"fmt"
	"regexp"
)

// ErrInvalidLabelKey is returned when a metric label (attribute) key contains
// characters outside the safe allowlist. The key is interpolated into the SQL
// JSON path (only the value is bound), so an unvalidated key — e.g. one
// containing a single quote — is a SQL-injection vector. Callers must reject it.
var ErrInvalidLabelKey = errors.New("invalid metric label key")

// validLabelKey matches legitimate OTLP attribute keys: dotted identifiers such
// as "service.name" or "http.status_code". Anything outside this set (notably
// quotes, spaces, parentheses) is rejected so the JSON path stays safe to
// interpolate.
var validLabelKey = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ValidLabelKey reports whether k is a safe metric label key. Exported so the
// API layer can reject bad keys with a 400 before they reach the query.
func ValidLabelKey(k string) bool {
	return validLabelKey.MatchString(k)
}

// attrTextSQL renders one attribute as the text a response label shows, because
// a filter value comes in as text and SQLite treats an integer and a text value
// as unequal. JSON_EXTRACT turns a boolean into 1 or 0, so booleans go by
// json_type: a filter on "1" must not match a true attribute.
const attrTextSQL = `CASE json_type(attributes, '$."%[1]s"') WHEN 'true' THEN 'true' WHEN 'false' THEN 'false'
	ELSE CAST(JSON_EXTRACT(attributes, '$."%[1]s"') AS TEXT) END`

// appendAttrFilters adds one equality predicate on attrTextSQL per attribute to
// where/args, binding each value as a parameter. Each key is validated with
// ValidLabelKey first; an invalid key returns ErrInvalidLabelKey rather than
// being interpolated, closing the JSON-path injection vector shared by the
// metric-series, rollup and log queries.
func appendAttrFilters(where []string, args []any, attrs map[string]string) ([]string, []any, error) {
	for k, v := range attrs {
		if !validLabelKey.MatchString(k) {
			return nil, nil, fmt.Errorf("%w: %q", ErrInvalidLabelKey, k)
		}
		where = append(where, fmt.Sprintf(attrTextSQL, k)+" = ?")
		args = append(args, v)
	}
	return where, args, nil
}
