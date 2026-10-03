package service

import (
	"encoding/json"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// Data types the API layer reads and writes. They are aliases of the storage
// types so the API package never imports the repository package.
type (
	Alert              = repository.Alert
	Span               = repository.Span
	SpanFilter         = repository.SpanFilter
	LogFilter          = repository.LogFilter
	MetricFilter       = repository.MetricFilter
	MetricRollup       = repository.MetricRollup
	MetricRollupFilter = repository.MetricRollupFilter
	CoarseRollupFilter = repository.CoarseRollupFilter
	MetricCatalogEntry = repository.MetricCatalogEntry
	MetricRow          = repository.MetricRow
	SavedQuery         = repository.SavedQuery
	ProjectUsageStats  = repository.ProjectUsageStats
	DBSize             = repository.DBSize
	DBCounts           = repository.DBCounts
	WebSession         = repository.WebSession
)

// Rollup5mStep is the step in seconds of the finest metric rollup tier.
const Rollup5mStep = repository.Rollup5mStep

// E2EAccountTTL is how long an E2E account lives before it expires.
const E2EAccountTTL = repository.E2EAccountTTL

// ValidLabelKey reports whether k is an acceptable metric label key.
func ValidLabelKey(k string) bool { return repository.ValidLabelKey(k) }

// MarshalMetricExtra renders the non-indexed part of a metric row as JSON.
func MarshalMetricExtra(row MetricRow) json.RawMessage { return repository.MarshalMetricExtra(row) }
