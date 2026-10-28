package service

import (
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// spanReadLimit is how many of the range's newest spans the service map, the
// dependencies and the database queries read. A busy range holds far more, so
// those views then cover only its last minutes and say so through Coverage.
var spanReadLimit = 10000

// Coverage says whether a view read every span in its range. Truncated is set
// when the range held more spans than the view reads, so it covers only the
// newest of them. CoveredFrom is then the ingest time of the oldest span read:
// the view covers CoveredFrom to the range end.
type Coverage struct {
	Truncated   bool       `json:"truncated"`
	CoveredFrom *time.Time `json:"coveredFrom,omitempty"`
}

// spanCoverage is the Coverage of a read of up to spanReadLimit spans.
func spanCoverage(spans []repository.Span) Coverage {
	if len(spans) < spanReadLimit {
		return Coverage{}
	}
	oldest := spans[0].IngestedAt
	for _, sp := range spans[1:] {
		if sp.IngestedAt.Before(oldest) {
			oldest = sp.IngestedAt
		}
	}
	return Coverage{Truncated: true, CoveredFrom: &oldest}
}

// coveredList is a cached list together with the coverage of the read behind
// it. The read cache stores values as JSON, so its fields are exported:
// unexported ones would come back empty on every cache hit.
type coveredList[T any] struct {
	Items    []T      `json:"items"`
	Coverage Coverage `json:"coverage"`
}
