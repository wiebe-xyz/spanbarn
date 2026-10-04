package main

import (
	"fmt"
	"math"
	"time"
)

// profile sizes the synthetic dataset. The prod profile reproduces the table
// mix of the production database at the 2026-10-04 check (4.07 GB): prompt
// bodies ~1.5 GB, metrics ~1.4 GB, logs ~0.8 GB, error samples ~0.5 GB. Spans
// were under 2% of the file at the "elevated" disk tier (4h trace retention);
// the profile carries more of them than that so the span queries have a
// working set closer to the default 168h window. Override with -spans.
type profile struct {
	Days            int
	Projects        int
	Spans           int
	SpansPerTrace   int
	Logs            int
	Metrics         int
	Prompts         int
	ErrorSamples    int
	PromptBodyBytes int
	LogBodyBytes    int
}

func prodProfile() profile {
	return profile{
		Days:            7,
		Projects:        8,
		Spans:           500_000,
		SpansPerTrace:   8,
		Logs:            1_000_000,
		Metrics:         2_800_000,
		Prompts:         258_000,
		ErrorSamples:    400_000,
		PromptBodyBytes: 2_700,
		LogBodyBytes:    300,
	}
}

// scaled shrinks or grows every row count by f, keeping at least one trace and
// one row per table so a tiny test dataset still exercises every path.
func (p profile) scaled(f float64) profile {
	s := func(n int) int { return int(math.Max(1, math.Round(float64(n)*f))) }
	p.Spans = max(s(p.Spans), p.SpansPerTrace)
	p.Logs = s(p.Logs)
	p.Metrics = s(p.Metrics)
	p.Prompts = s(p.Prompts)
	p.ErrorSamples = s(p.ErrorSamples)
	return p
}

func (p profile) String() string {
	return fmt.Sprintf("days=%d projects=%d spans=%d logs=%d metrics=%d prompts=%d error_samples=%d",
		p.Days, p.Projects, p.Spans, p.Logs, p.Metrics, p.Prompts, p.ErrorSamples)
}

// window is the time range the dataset covers: Days whole UTC days ending at
// End, so day boundaries line up with the daily shard files.
type window struct {
	Start time.Time
	End   time.Time
}

func newWindow(end time.Time, days int) window {
	end = end.UTC().Truncate(24 * time.Hour)
	return window{Start: end.AddDate(0, 0, -days), End: end}
}

// days lists the UTC midnight that starts each day in the window.
func (w window) days() []time.Time {
	var out []time.Time
	for d := w.Start; d.Before(w.End); d = d.Add(24 * time.Hour) {
		out = append(out, d)
	}
	return out
}

// sqlTime formats t the way CURRENT_TIMESTAMP stores ingested_at in
// production, so range predicates compare the same strings they do there.
func sqlTime(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05")
}
