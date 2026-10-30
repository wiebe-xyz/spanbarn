package model

// SpanLedgerBucketSeconds is the width of a span ledger bucket.
const SpanLedgerBucketSeconds = 300

// SpanLedgerRow counts one project's spans of one bucket at each stage between
// acceptance and storage. Bucket is the Unix second the 5-minute bucket of the
// span's start time begins at, so every stage counts a span in the same bucket
// however long it took to get there.
type SpanLedgerRow struct {
	Bucket    int64 `json:"bucket"`
	ProjectID int64 `json:"projectId"`
	// Accepted is spans the trace buffer took in, where ingest answers 200.
	Accepted int64 `json:"accepted"`
	// SampledOut is spans of traces sampling dropped. sample_weight accounts
	// for them, so they are intended.
	SampledOut int64 `json:"sampledOut"`
	// ShedUnsampled is spans the cap evicted from traces the ratio skips.
	ShedUnsampled int64 `json:"shedUnsampled"`
	// LostInBuffer is spans the buffer refused, evicted from traces it would
	// have kept, or could not hand on.
	LostInBuffer int64 `json:"lostInBuffer"`
	// Forwarded is spans that left the buffer towards the writer.
	Forwarded int64 `json:"forwarded"`
	// Processed is spans the writer stored or aggregated.
	Processed int64 `json:"processed"`
	// Failed is spans the writer gave up on.
	Failed int64 `json:"failed"`
}

// SpanLedgerBucket returns the bucket of a span that started at startUs.
func SpanLedgerBucket(startUs int64) int64 {
	sec := startUs / 1_000_000
	return sec - sec%SpanLedgerBucketSeconds
}
