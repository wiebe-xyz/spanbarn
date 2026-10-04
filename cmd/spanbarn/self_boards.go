package main

import (
	"database/sql"
	"errors"
	"log/slog"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
	"github.com/wiebe-xyz/spanbarn/internal/config"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/service"
)

func metricPanel(title, name string, groupBy ...string) service.PanelRequest {
	return service.PanelRequest{
		Title:      title,
		View:       service.PanelViewMetric,
		Definition: service.QueryDefinition{Metric: &service.MetricPanel{Name: name, GroupBy: groupBy}},
	}
}

// selfBoards are the boards over SpanBarn's own self-metrics: storage (#240)
// and the ingest path.
func selfBoards() []service.BoardSpec {
	return []service.BoardSpec{
		{
			Name:      "SpanBarn storage",
			TimeRange: "7d",
			Refresh:   300,
			Panels: []service.PanelRequest{
				metricPanel("Volume used (%)", "spanbarn.disk.used_pct"),
				metricPanel("Disk pressure tier (0 normal, 1 elevated, 2 critical)", "spanbarn.disk.tier"),
				metricPanel("Database file size (bytes)", "spanbarn.db.bytes"),
				metricPanel("WAL size (bytes)", "spanbarn.db.wal_bytes"),
				metricPanel("Free pages inside the database (bytes)", "spanbarn.db.freelist_bytes"),
				metricPanel("Rows deleted by retention per second, per table", "spanbarn.retention.deleted", "table"),
				metricPanel("Retention backlog left after a cycle (1 = rows left), per table", "spanbarn.retention.backlog", "table"),
			},
		},
		{
			Name:      "SpanBarn ingest",
			TimeRange: "24h",
			Refresh:   60,
			Panels: []service.PanelRequest{
				metricPanel("Write queue depth, per queue", "spanbarn.queue.depth", "queue"),
				metricPanel("Spans held in the trace buffer", "spanbarn.trace_buffer.spans"),
				metricPanel("Traces held in the trace buffer", "spanbarn.trace_buffer.traces"),
				metricPanel("Spans lost from the trace buffer (total since pod start)", "spanbarn.trace_buffer.spans_lost"),
				metricPanel("Spool size (bytes)", "spanbarn.spool.bytes"),
				metricPanel("Metric rollups persisted per second", "spanbarn.rollups.persisted"),
				metricPanel("HTTP requests per second, per status class", "spanbarn.http.requests", "status"),
				metricPanel("HTTP request latency", "spanbarn.http.server.duration"),
			},
		},
	}
}

// selfProjectID returns the project SpanBarn's self-metrics land in: the
// project of the API key the self-metrics reporter sends with. It reports
// false when there is no such key in the database (self-metrics off, or sent
// with the static admin key, which belongs to no project).
func selfProjectID(cfg config.Config, repo *repository.Repository) (int64, bool, error) {
	if cfg.Self.MetricsDisabled {
		return 0, false, nil
	}
	key := cfg.Self.APIKey
	if key == "" {
		key = cfg.APIKey
	}
	if key == "" {
		return 0, false, nil
	}
	k, err := repo.GetAPIKeyByHash(auth.HashKey(key))
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return k.ProjectID, k.ProjectID > 0, nil
}

// ensureSelfBoards creates the self-metrics boards on the self-reporting
// project when they are missing. A failure costs a board, never the startup.
func ensureSelfBoards(cfg config.Config, repo *repository.Repository, logger *slog.Logger) {
	projectID, ok, err := selfProjectID(cfg, repo)
	if err != nil {
		logger.Warn("self boards: cannot resolve the self-metrics project", "error", err)
		return
	}
	if !ok {
		logger.Info("self boards skipped: self-metrics do not go to a project in this database")
		return
	}
	svc := service.NewBoardService(repo, logger)
	for _, spec := range selfBoards() {
		id, created, err := svc.EnsureBoard(projectID, spec)
		if err != nil {
			logger.Warn("self boards: cannot create board", "board", spec.Name, "error", err)
			continue
		}
		if created {
			logger.Info("self boards: created board", "board", spec.Name, "board_id", id, "project_id", projectID)
		}
	}
}
