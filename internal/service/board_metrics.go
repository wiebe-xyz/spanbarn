package service

import "strings"

// PanelViewMetric draws an OTLP metric series instead of a span query.
const PanelViewMetric = "metric"

const maxMetricName = 200

// MetricPanel is the query of a metric panel: one metric, optionally split
// into one line per value of the group-by attributes. It matches the
// parameters of GET /api/v1/metrics/series.
type MetricPanel struct {
	Name    string   `json:"name"`
	GroupBy []string `json:"groupBy,omitempty"`
}

func (m *MetricPanel) validate() error {
	if m == nil {
		return analyzeInvalid("a metric panel needs a metric")
	}
	name := strings.TrimSpace(m.Name)
	if name == "" || len(name) > maxMetricName {
		return analyzeInvalid("metric name is required and limited to %d characters", maxMetricName)
	}
	for _, k := range m.GroupBy {
		if !ValidLabelKey(k) {
			return analyzeInvalid("invalid group-by attribute %q", k)
		}
	}
	return nil
}

// validatePanelDefinition checks the query a panel of the given view draws.
// Metric panels carry a metric and no span calculations; the other views carry
// a span query and no metric.
func validatePanelDefinition(view string, d QueryDefinition) error {
	if view == PanelViewMetric {
		return d.Metric.validate()
	}
	if d.Metric != nil {
		return analyzeInvalid("only a metric panel takes a metric")
	}
	return d.validate()
}

// BoardSpec describes a board EnsureBoard builds: its settings and panels in
// grid order.
type BoardSpec struct {
	Name      string
	TimeRange string
	Refresh   int
	Panels    []PanelRequest
}

// EnsureBoard creates the board of spec on a project unless the project
// already has a board of that name, which it then leaves as it is, so edits
// made in the UI survive. It reports the board id and whether it created it.
func (s *BoardService) EnsureBoard(projectID int64, spec BoardSpec) (int64, bool, error) {
	for _, p := range spec.Panels {
		if err := p.withDefaults().validate(); err != nil {
			return 0, false, err
		}
	}
	boards, err := s.ListBoards(projectID)
	if err != nil {
		return 0, false, err
	}
	for _, b := range boards {
		if b.Name == strings.TrimSpace(spec.Name) {
			return b.ID, false, nil
		}
	}
	id, err := s.CreateBoard(projectID, spec.Name, spec.TimeRange, spec.Refresh)
	if err != nil {
		return 0, false, err
	}
	for _, p := range spec.Panels {
		if _, err := s.AddPanel(id, p); err != nil {
			// A half-built board would be found by name next time and never
			// completed, so take it down and let the next call start over.
			if delErr := s.DeleteBoard(id); delErr != nil {
				return 0, false, delErr
			}
			return 0, false, err
		}
	}
	return id, true, nil
}
