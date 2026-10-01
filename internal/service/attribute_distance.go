package service

import (
	"math"
	"sort"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// AttributeValueShare is one value of an attribute with its count and share in
// the selection and in the baseline.
type AttributeValueShare struct {
	// Value is the attribute value. Missing marks the bucket of spans that do
	// not populate the attribute, whose Value is empty.
	Value          string  `json:"value"`
	Missing        bool    `json:"missing,omitempty"`
	SelectionCount int64   `json:"selectionCount"`
	SelectionShare float64 `json:"selectionShare"`
	BaselineCount  int64   `json:"baselineCount"`
	BaselineShare  float64 `json:"baselineShare"`
	magnitude      float64
}

// AttributeDifference ranks one attribute by how far its value distribution in
// the selection sits from the baseline.
type AttributeDifference struct {
	Key string `json:"key"`
	// Score is the total variation distance between the two distributions, 0
	// (identical) to 1 (no value in common). Spans that do not set the key count
	// as a value of their own.
	Score float64 `json:"score"`
	// SelectionCoverage and BaselineCoverage are the shares of spans that set the key.
	SelectionCoverage float64 `json:"selectionCoverage"`
	BaselineCoverage  float64 `json:"baselineCoverage"`
	// Values are the values that differ most, largest difference first.
	Values []AttributeValueShare `json:"values"`
}

// missingCount is the number of spans of a set that do not set a key. An
// absent key has no entry, so every span of the set is missing it.
func missingCount(scanned int64, sv *repository.AttributeSetValues) int64 {
	if sv == nil {
		return scanned
	}
	present := sv.Other
	for _, c := range sv.Counts {
		present += c
	}
	return max(scanned-present, 0)
}

func share(count, total int64) float64 {
	if total == 0 {
		return 0
	}
	return float64(count) / float64(total)
}

// compareKey builds the difference of one key. The total variation distance
// sums the absolute share differences over every value, the missing bucket and
// the lumped remainder, then halves it.
func compareKey(key string, sel, base *repository.AttributeSetScan) AttributeDifference {
	sv, bv := sel.Keys[key], base.Keys[key]
	selMissing, baseMissing := missingCount(sel.Scanned, sv), missingCount(base.Scanned, bv)

	values := map[string]*AttributeValueShare{}
	get := func(v string) *AttributeValueShare {
		if e, ok := values[v]; ok {
			return e
		}
		e := &AttributeValueShare{Value: v}
		values[v] = e
		return e
	}
	var selOther, baseOther int64
	if sv != nil {
		selOther = sv.Other
		for v, c := range sv.Counts {
			get(v).SelectionCount = c
		}
	}
	if bv != nil {
		baseOther = bv.Other
		for v, c := range bv.Counts {
			get(v).BaselineCount = c
		}
	}
	if selMissing > 0 || baseMissing > 0 {
		m := get("\x00missing")
		m.Value, m.Missing = "", true
		m.SelectionCount, m.BaselineCount = selMissing, baseMissing
	}

	diff := AttributeDifference{
		Key:               key,
		SelectionCoverage: share(sel.Scanned-selMissing, sel.Scanned),
		BaselineCoverage:  share(base.Scanned-baseMissing, base.Scanned),
	}
	var sum float64
	for _, e := range values {
		e.SelectionShare = share(e.SelectionCount, sel.Scanned)
		e.BaselineShare = share(e.BaselineCount, base.Scanned)
		e.magnitude = math.Abs(e.SelectionShare - e.BaselineShare)
		sum += e.magnitude
		diff.Values = append(diff.Values, *e)
	}
	sum += math.Abs(share(selOther, sel.Scanned) - share(baseOther, base.Scanned))
	diff.Score = math.Min(sum/2, 1)
	sort.Slice(diff.Values, func(i, j int) bool {
		a, b := diff.Values[i], diff.Values[j]
		if a.magnitude != b.magnitude {
			return a.magnitude > b.magnitude
		}
		if ta, tb := a.SelectionCount+a.BaselineCount, b.SelectionCount+b.BaselineCount; ta != tb {
			return ta > tb
		}
		return a.Value < b.Value
	})
	return diff
}

// rankAttributes compares every key seen in either set and returns the keys with
// a non-zero distance, most different first, at most limit of them, each with
// its top values. Keys tie-break alphabetically so the output is stable.
func rankAttributes(sel, base *repository.AttributeSetScan, limit, topValues int) []AttributeDifference {
	if sel.Scanned == 0 || base.Scanned == 0 {
		return []AttributeDifference{}
	}
	keys := map[string]struct{}{}
	for k := range sel.Keys {
		keys[k] = struct{}{}
	}
	for k := range base.Keys {
		keys[k] = struct{}{}
	}
	out := make([]AttributeDifference, 0, len(keys))
	for k := range keys {
		d := compareKey(k, sel, base)
		if d.Score > 1e-9 {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Key < out[j].Key
	})
	if len(out) > limit {
		out = out[:limit]
	}
	for i := range out {
		if len(out[i].Values) > topValues {
			out[i].Values = out[i].Values[:topValues]
		}
	}
	return out
}
