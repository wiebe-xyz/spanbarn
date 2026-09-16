package retention

import "time"

// Config controls the retention worker's behaviour.
type Config struct {
	InterestingRetentionHours int           // hours to keep spans (default 48 = 2d); this is the cutoff RunOnce deletes spans on, so it sizes the database
	BoringRetentionMinutes    int           // minutes to keep sampled boring spans (default 30); 0 disables boring cleanup
	ErrorRetentionDays        int           // days to keep error samples (default 30)
	AggregateRetentionDays    int           // days to keep aggregates (default 365)
	MetricsRetentionDays      int           // days to keep raw metric data points (default 7). Raw points are only ever read for ranges <= rollupQueryThreshold (6h); longer ranges read the rollup tiers.
	LogRetentionHours         int           // hours to keep log records (default 24)
	ErrorLogRetentionDays     int           // days to keep logs for error-sampled traces (default 30)
	SlowThresholdUS           int64         // microseconds above which a span is "slow"
	Interval                  time.Duration // how often to run (default 5m)

	// Rollup tier windows. Each tier is deleted only up to the point the
	// compactor has rolled it into the tier above (internal/rollup writes that
	// watermark), so shortening a window never destroys history that has not
	// been summarised yet.
	MetricRollupRetentionDays int // days to keep 5-minute rollups (default 2)
	HourlyRollupDays          int // days to keep hourly rollups (default 30)
	DailyRollupDays           int // days to keep daily rollups (default 365)
	WeeklyRollupDays          int // days to keep weekly rollups (default 730)
	MonthlyRollupDays         int // days to keep monthly rollups (default 0 = keep indefinitely)

	// BatchYield is how long the worker voluntarily releases the write lock
	// between deletion batches so the span-insert worker can drain the Redis
	// queue. 0 disables yielding (old behaviour). Default 30s.
	BatchYield time.Duration
	// DBPath is the on-disk database file, used to measure how full the volume
	// is. Empty disables disk-pressure tiering (windows stay purely time-based).
	DBPath string
	// Watermarks are the volume-used fractions at which retention starts
	// shortening its raw-telemetry windows. Zero values take the defaults.
	Watermarks Watermarks
	// TargetFraction is the volume-used level the emergency loop evicts back
	// down to once the critical watermark is crossed. Default 0.70.
	TargetFraction float64
	// BallastBytes is the reserved space held so that a full volume can always
	// delete its way out. 0 disables the reserve — which means a volume that
	// does reach 100% stays wedged until a human intervenes.
	BallastBytes int64
}

func (c Config) withDefaults() Config {
	for _, d := range []struct {
		dst *int
		val int
	}{
		{&c.InterestingRetentionHours, 48},
		{&c.BoringRetentionMinutes, 30},
		{&c.ErrorRetentionDays, 30},
		{&c.AggregateRetentionDays, 365},
		{&c.MetricsRetentionDays, 7},
		{&c.LogRetentionHours, 24},
		{&c.ErrorLogRetentionDays, 30},
		{&c.MetricRollupRetentionDays, 2},
		{&c.HourlyRollupDays, 30},
		{&c.DailyRollupDays, 365},
		{&c.WeeklyRollupDays, 730},
	} {
		if *d.dst <= 0 {
			*d.dst = d.val
		}
	}
	// MonthlyRollupDays is deliberately absent: 0 means keep indefinitely, which
	// is the point of a monthly tier. At ~1,500 series a month it costs about a
	// megabyte a year.
	if c.SlowThresholdUS <= 0 {
		c.SlowThresholdUS = 1_000_000 // 1 second
	}
	if c.Interval <= 0 {
		c.Interval = 5 * time.Minute
	}
	if c.BatchYield == 0 {
		c.BatchYield = 30 * time.Second
	}
	return c
}

// effectiveConfig returns the retention config, overriding with DB settings
// where present. Every window is one row in the table below, so adding a knob
// cannot silently skip the wiring — which is exactly how the rollup window came
// to sit at its hardcoded default for a year while the database filled.
func (w *RetentionWorker) effectiveConfig() Config {
	cfg := w.cfg

	// retention_full_hours is obsolete: the "drop uninteresting spans early" tier
	// it used to name is now the boring-span classifier (expires_at +
	// boring_retention_minutes). It was still readable but wired to nothing, and
	// the README advertised it as "hours to keep all spans" — so it read back
	// fine while doing nothing, and an operator who set it believed spans were
	// capped when they were not. That is what filled production's disk. Say so
	// rather than ignoring it silently.
	if w.settingInt("retention_full_hours") > 0 {
		w.warnObsoleteFullHours.Do(func() {
			w.logger.Warn("setting 'retention_full_hours' is obsolete and does nothing — "+
				"span retention is governed by 'retention_interesting_hours'; uninteresting spans "+
				"expire via 'boring_retention_minutes'. Remove the setting.",
				"retention_interesting_hours", cfg.InterestingRetentionHours,
				"boring_retention_minutes", cfg.BoringRetentionMinutes)
		})
	}

	for _, o := range []struct {
		key string
		dst *int
	}{
		{"retention_interesting_hours", &cfg.InterestingRetentionHours},
		{"retention_aggregated_days", &cfg.AggregateRetentionDays},
		{"retention_error_days", &cfg.ErrorRetentionDays},
		{"boring_retention_minutes", &cfg.BoringRetentionMinutes},
		{"metrics_retention_days", &cfg.MetricsRetentionDays},
		{"log_retention_hours", &cfg.LogRetentionHours},
		{"error_log_retention_days", &cfg.ErrorLogRetentionDays},
		{"metric_rollup_retention_days", &cfg.MetricRollupRetentionDays},
		{"metric_rollup_hourly_days", &cfg.HourlyRollupDays},
		{"metric_rollup_daily_days", &cfg.DailyRollupDays},
		{"metric_rollup_weekly_days", &cfg.WeeklyRollupDays},
		{"metric_rollup_monthly_days", &cfg.MonthlyRollupDays},
	} {
		if n := w.settingInt(o.key); n > 0 {
			*o.dst = n
		}
	}
	return cfg
}
