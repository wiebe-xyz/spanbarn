package rollup

import (
	"sort"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// TemporalityDelta marks a series whose stored value is already the change
// within its bucket. Anything else — including the empty string, which is what
// rows written before migration 033 carry — is treated as cumulative, matching
// the OTLP SDK default.
const TemporalityDelta = "delta"

// seriesKey identifies one source series within a compaction window.
type seriesKey struct {
	projectID   int64
	name        string
	fingerprint string
}

// Carry is the state a compaction pass needs from outside its own window: the
// last source bucket before it (so the first increase inside the window can be
// measured) and the previous target bucket's running total (so a counter
// continues rather than restarting at every tier bucket).
type Carry struct {
	// SourceTail is the newest source row before the window, keyed by SourceTailKey.
	SourceTail map[string]repository.MetricRollup
	// TargetLast is the `last` value of the previous target bucket, per reduced
	// attribute fingerprint.
	TargetLast map[string]float64
}

// SourceTailKey addresses a source row within Carry.SourceTail.
func SourceTailKey(m repository.MetricRollup) string {
	return m.Name + "\x00" + m.AttrFingerprint
}

// Merge folds the source rows of one target bucket into rows of the coarser
// tier. src may arrive in any order but must belong to a single target bucket
// and project.
//
// Counters are the delicate part. A cumulative counter is stored as its running
// total, so adding two series together only means something while both exist —
// when a deploy replaces one instance series with another, the sum falls and
// rate derivation reads that as a counter reset. So each source series is first
// reduced to the increase it contributed during the window (reset-aware), those
// increases are added across the series being merged, and the target row stores
// the previous target bucket's total plus that increase. The result is monotonic
// per reduced label set, which internal/metrics.Derive already rates correctly —
// no reader changes, and no false dip on deploy day.
func Merge(
	tier Tier,
	bucket time.Time,
	projectID int64,
	src []repository.MetricRollup,
	policy DropPolicy,
	carry Carry,
) []repository.MetricRollup {
	if len(src) == 0 {
		return nil
	}

	accs := map[string]*targetAcc{}
	var order []string
	for _, c := range contributionsBySeries(src, carry) {
		attrs, fp := policy.Reduce(c.row.Attributes, tier.Step)
		acc := accs[fp]
		if acc == nil {
			acc = newTargetAcc(c.row, projectID, attrs, fp, tier.Step, bucket)
			accs[fp] = acc
			order = append(order, fp)
		}
		acc.add(c)
	}

	out := make([]repository.MetricRollup, 0, len(order))
	for _, fp := range order {
		out = append(out, accs[fp].finish(carry.TargetLast[fp]))
	}
	return out
}

// targetAcc accumulates the contributions merging into one coarse row.
type targetAcc struct {
	row     repository.MetricRollup
	hist    *histogram
	haveVal bool
}

func newTargetAcc(from repository.MetricRollup, projectID int64, attrs, fingerprint string, step int64, bucket time.Time) *targetAcc {
	row := repository.MetricRollup{
		ProjectID:       projectID,
		Name:            from.Name,
		Type:            from.Type,
		Unit:            from.Unit,
		Temporality:     from.Temporality,
		AttrFingerprint: fingerprint,
		Attributes:      attrs,
		StepSeconds:     step,
		Bucket:          bucket,
	}
	return &targetAcc{row: row}
}

func (a *targetAcc) add(c contribution) {
	a.row.Count += c.count
	a.row.Sum += c.sum
	a.row.ObsCount += c.obsCount
	if c.haveVal {
		if !a.haveVal || c.min < a.row.Min {
			a.row.Min = c.min
		}
		if !a.haveVal || c.max > a.row.Max {
			a.row.Max = c.max
		}
		a.haveVal = true
	}
	switch a.row.Type {
	case "histogram":
		a.hist = addHistogram(a.hist, c.hist)
	case "exp_histogram", "summary":
		// These carry an opaque snapshot that cannot be merged; the newest one
		// in the window stands in for the bucket, as the accumulator does.
		if c.extra != "" {
			a.row.Extra = c.extra
		}
	default:
		a.row.Last += c.last
	}
}

// finish produces the stored row. For counters the running total continues from
// the previous target bucket, so the tier stays monotonic across bucket
// boundaries and across deploys.
func (a *targetAcc) finish(prevTargetLast float64) repository.MetricRollup {
	row := a.row
	if row.Type == "sum" {
		row.Last = prevTargetLast + row.Sum
	}
	if row.Type == "histogram" {
		row.Extra = a.hist.marshal()
		if row.ObsCount == 0 {
			row.ObsCount = int64(a.hist.total())
		}
	}
	return row
}

// contribution is what one source series adds to the target bucket.
type contribution struct {
	row      repository.MetricRollup // newest source row: carries type, unit, attributes
	count    int64
	sum      float64
	obsCount int64
	last     float64
	min      float64
	max      float64
	haveVal  bool
	hist     *histogram // observations during the window, already de-cumulated
	extra    string     // passed through for exp_histogram and summary
}

// contributionsBySeries reduces the window's rows to one contribution per source
// series, ordered by bucket so cumulative values can be differenced.
func contributionsBySeries(src []repository.MetricRollup, carry Carry) []contribution {
	bySeries := map[seriesKey][]repository.MetricRollup{}
	var order []seriesKey
	for _, m := range src {
		k := seriesKey{m.ProjectID, m.Name, m.AttrFingerprint}
		if _, seen := bySeries[k]; !seen {
			order = append(order, k)
		}
		bySeries[k] = append(bySeries[k], m)
	}

	out := make([]contribution, 0, len(order))
	for _, k := range order {
		rows := bySeries[k]
		sort.Slice(rows, func(i, j int) bool { return rows[i].Bucket.Before(rows[j].Bucket) })
		out = append(out, seriesContribution(rows, carry.SourceTail[SourceTailKey(rows[0])]))
	}
	return out
}

// seriesContribution reduces one source series to a single contribution. prev is
// the row immediately before the window, zero-valued when there is none.
func seriesContribution(rows []repository.MetricRollup, prev repository.MetricRollup) contribution {
	newest := rows[len(rows)-1]
	c := contribution{row: newest, last: newest.Last, extra: newest.Extra}
	cumulative := newest.Temporality != TemporalityDelta

	state := &cumulativeState{cumulative: cumulative}
	if !prev.Bucket.IsZero() {
		state.start(prev)
	}

	for _, r := range rows {
		c.count += r.Count
		c.observe(r)

		switch r.Type {
		case "sum":
			c.sum += state.increase(r)
			c.obsCount += r.ObsCount
		case "histogram":
			// A cumulative histogram's sum and count are snapshots as much as
			// its bucket counts are, so all three are differenced together.
			sum, obs := state.distribution(r)
			c.sum += sum
			c.obsCount += obs
			c.hist = addHistogram(c.hist, state.histogram(r))
		default:
			c.sum += r.Sum
			c.obsCount += r.ObsCount
		}
	}
	return c
}

// observe extends the contribution's min/max with a source row's.
func (c *contribution) observe(r repository.MetricRollup) {
	if !c.haveVal || r.Min < c.min {
		c.min = r.Min
	}
	if !c.haveVal || r.Max > c.max {
		c.max = r.Max
	}
	c.haveVal = true
}

// cumulativeState walks one series in bucket order, turning running totals into
// per-bucket increases. A delta series needs no state and passes straight
// through.
type cumulativeState struct {
	cumulative bool
	haveLast   bool
	last       float64
	hist       *histogram
	haveDist   bool
	sum        float64
	obsCount   int64
}

func (s *cumulativeState) start(prev repository.MetricRollup) {
	if !s.cumulative {
		return
	}
	s.haveLast = true
	s.last = prev.Last
	s.hist = histogramOf(prev)
	s.haveDist = true
	s.sum = prev.Sum
	s.obsCount = prev.ObsCount
}

// distribution returns the sum and observation count a histogram row added
// during its own bucket. Cumulative rows carry totals since process start, so
// the answer is the step up from the previous row; a step down is a restart and
// the row stands on its own.
func (s *cumulativeState) distribution(r repository.MetricRollup) (float64, int64) {
	if !s.cumulative {
		return r.Sum, r.ObsCount
	}
	sum, obs := r.Sum, r.ObsCount
	if s.haveDist && r.Sum >= s.sum && r.ObsCount >= s.obsCount {
		sum, obs = r.Sum-s.sum, r.ObsCount-s.obsCount
	} else if !s.haveDist {
		// No earlier snapshot: the movement within this bucket is unknowable,
		// and counting the whole running total would inflate it. Same choice as
		// the counter path.
		sum, obs = 0, 0
	}
	s.haveDist = true
	s.sum, s.obsCount = r.Sum, r.ObsCount
	return sum, obs
}

// increase returns the counter movement this row contributed. A value below the
// previous one means the process restarted, so the row's own value is the
// increase since that restart. With no previous sample nothing is contributed:
// the alternative, counting the first value in full, would inflate every sparse
// series — and most series here report only a few times a day.
func (s *cumulativeState) increase(r repository.MetricRollup) float64 {
	if !s.cumulative {
		return r.Sum
	}
	var inc float64
	switch {
	case !s.haveLast:
		inc = 0
	case r.Last >= s.last:
		inc = r.Last - s.last
	default:
		inc = r.Last
	}
	s.haveLast = true
	s.last = r.Last
	return inc
}

// histogram returns the observations this row contributed during its own bucket,
// differencing the cumulative snapshot against the previous one.
func (s *cumulativeState) histogram(r repository.MetricRollup) *histogram {
	cur := histogramOf(r)
	if !s.cumulative {
		return cur
	}
	delta := cur.sub(s.hist)
	if cur != nil {
		s.hist = cur
	}
	return delta
}
