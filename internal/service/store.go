package service

// Store is every storage capability the API layer's services draw on. The
// API server holds one Store and builds its services from it.
type Store interface {
	AlertRepository
	SavedQueryRepository
	TraceExclusionRepository
	SpanStreamRepository
	SettingsRepository
	ProjectRepository
	LogsRepository
	MetricsRepository
	BoardRepository
	SLORepository
	CalculatedFieldRepository
}

// WarmRepository is the storage the cache warmers need.
type WarmRepository interface {
	ProjectRepository
	SettingsRepository
}
