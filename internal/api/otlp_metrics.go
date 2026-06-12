package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/wiebe-xyz/spanbarn/internal/model"

	collectormetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func (s *Server) handleOTLPMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		if strings.Contains(err.Error(), "http: request body too large") {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large", "")
			return
		}
		writeError(w, http.StatusBadRequest, "failed to read body", err.Error())
		return
	}

	var req collectormetricspb.ExportMetricsServiceRequest
	ct := r.Header.Get("Content-Type")
	switch {
	case strings.Contains(ct, "application/json"):
		if err := protojson.Unmarshal(body, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON", err.Error())
			return
		}
	default:
		if err := proto.Unmarshal(body, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid protobuf", err.Error())
			return
		}
	}

	projectID := GetProjectID(r.Context())
	recs := otlpToMetricRecords(&req, projectID)
	s.metricsIngest.Enqueue(recs)

	resp := &collectormetricspb.ExportMetricsServiceResponse{}
	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "application/json") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		data, _ := protojson.Marshal(resp)
		_, _ = w.Write(data)
	} else {
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
		data, _ := proto.Marshal(resp)
		_, _ = w.Write(data)
	}
}

// otlpToMetricRecords converts an ExportMetricsServiceRequest into MetricRecords.
// One record is produced per data point. Attributes are merged resource < scope < data-point.
func otlpToMetricRecords(req *collectormetricspb.ExportMetricsServiceRequest, projectID int64) []model.MetricRecord {
	var records []model.MetricRecord
	for _, rm := range req.GetResourceMetrics() {
		resourceAttrs := kvListToMap(rm.GetResource().GetAttributes())

		for _, sm := range rm.GetScopeMetrics() {
			scopeAttrs := kvListToMap(sm.GetScope().GetAttributes())

			for _, metric := range sm.GetMetrics() {
				base := metricBase{
					projectID:   projectID,
					name:        metric.GetName(),
					description: metric.GetDescription(),
					unit:        metric.GetUnit(),
					resourceAttrs: resourceAttrs,
					scopeAttrs:    scopeAttrs,
				}
				records = append(records, base.convertMetric(metric)...)
			}
		}
	}
	return records
}

// metricBase carries fields shared by all data points within a single Metric.
type metricBase struct {
	projectID     int64
	name          string
	description   string
	unit          string
	resourceAttrs map[string]any
	scopeAttrs    map[string]any
}

func (b metricBase) mergeAttrs(dpAttrs map[string]any) json.RawMessage {
	merged := make(map[string]any, len(b.resourceAttrs)+len(b.scopeAttrs)+len(dpAttrs))
	for k, v := range b.resourceAttrs {
		merged[k] = v
	}
	for k, v := range b.scopeAttrs {
		merged[k] = v
	}
	for k, v := range dpAttrs {
		merged[k] = v
	}
	if len(merged) == 0 {
		return nil
	}
	raw, _ := json.Marshal(merged)
	return raw
}

func (b metricBase) convertMetric(m *metricspb.Metric) []model.MetricRecord {
	switch d := m.GetData().(type) {
	case *metricspb.Metric_Gauge:
		return b.fromNumberDataPoints(model.MetricTypeGauge, d.Gauge.GetDataPoints())
	case *metricspb.Metric_Sum:
		return b.fromNumberDataPoints(model.MetricTypeSum, d.Sum.GetDataPoints())
	case *metricspb.Metric_Histogram:
		return b.fromHistogramDataPoints(d.Histogram.GetDataPoints())
	case *metricspb.Metric_ExponentialHistogram:
		return b.fromExpHistogramDataPoints(d.ExponentialHistogram.GetDataPoints())
	case *metricspb.Metric_Summary:
		return b.fromSummaryDataPoints(d.Summary.GetDataPoints())
	}
	return nil
}

func (b metricBase) fromNumberDataPoints(typ model.MetricType, dps []*metricspb.NumberDataPoint) []model.MetricRecord {
	recs := make([]model.MetricRecord, 0, len(dps))
	for _, dp := range dps {
		recs = append(recs, model.MetricRecord{
			ProjectID:         b.projectID,
			Name:              b.name,
			Description:       b.description,
			Unit:              b.unit,
			Type:              typ,
			TimeUnixNano:      dp.GetTimeUnixNano(),
			StartTimeUnixNano: dp.GetStartTimeUnixNano(),
			Value:             extractNumberValue(dp),
			Attributes:        b.mergeAttrs(kvListToMap(dp.GetAttributes())),
		})
	}
	return recs
}

func (b metricBase) fromHistogramDataPoints(dps []*metricspb.HistogramDataPoint) []model.MetricRecord {
	recs := make([]model.MetricRecord, 0, len(dps))
	for _, dp := range dps {
		extra, _ := json.Marshal(map[string]any{
			"bounds": dp.GetExplicitBounds(),
			"counts": dp.GetBucketCounts(),
		})
		recs = append(recs, model.MetricRecord{
			ProjectID:         b.projectID,
			Name:              b.name,
			Description:       b.description,
			Unit:              b.unit,
			Type:              model.MetricTypeHistogram,
			TimeUnixNano:      dp.GetTimeUnixNano(),
			StartTimeUnixNano: dp.GetStartTimeUnixNano(),
			Value:             dp.GetSum(),
			Count:             dp.GetCount(),
			Attributes:        b.mergeAttrs(kvListToMap(dp.GetAttributes())),
			Extra:             extra,
		})
	}
	return recs
}

func (b metricBase) fromExpHistogramDataPoints(dps []*metricspb.ExponentialHistogramDataPoint) []model.MetricRecord {
	recs := make([]model.MetricRecord, 0, len(dps))
	for _, dp := range dps {
		extra, _ := json.Marshal(map[string]any{
			"scale":      dp.GetScale(),
			"zero_count": dp.GetZeroCount(),
			"positive":   expBuckets(dp.GetPositive()),
			"negative":   expBuckets(dp.GetNegative()),
		})
		recs = append(recs, model.MetricRecord{
			ProjectID:         b.projectID,
			Name:              b.name,
			Description:       b.description,
			Unit:              b.unit,
			Type:              model.MetricTypeExponentialHistogram,
			TimeUnixNano:      dp.GetTimeUnixNano(),
			StartTimeUnixNano: dp.GetStartTimeUnixNano(),
			Value:             dp.GetSum(),
			Count:             dp.GetCount(),
			Attributes:        b.mergeAttrs(kvListToMap(dp.GetAttributes())),
			Extra:             extra,
		})
	}
	return recs
}

func (b metricBase) fromSummaryDataPoints(dps []*metricspb.SummaryDataPoint) []model.MetricRecord {
	recs := make([]model.MetricRecord, 0, len(dps))
	for _, dp := range dps {
		type quantileVal struct {
			Quantile float64 `json:"quantile"`
			Value    float64 `json:"value"`
		}
		qvals := make([]quantileVal, 0, len(dp.GetQuantileValues()))
		for _, q := range dp.GetQuantileValues() {
			qvals = append(qvals, quantileVal{Quantile: q.GetQuantile(), Value: q.GetValue()})
		}
		extra, _ := json.Marshal(map[string]any{"quantiles": qvals})
		recs = append(recs, model.MetricRecord{
			ProjectID:         b.projectID,
			Name:              b.name,
			Description:       b.description,
			Unit:              b.unit,
			Type:              model.MetricTypeSummary,
			TimeUnixNano:      dp.GetTimeUnixNano(),
			StartTimeUnixNano: dp.GetStartTimeUnixNano(),
			Value:             dp.GetSum(),
			Count:             dp.GetCount(),
			Attributes:        b.mergeAttrs(kvListToMap(dp.GetAttributes())),
			Extra:             extra,
		})
	}
	return recs
}

// extractNumberValue returns the float64 value from a NumberDataPoint regardless of oneof type.
func extractNumberValue(dp *metricspb.NumberDataPoint) float64 {
	switch v := dp.GetValue().(type) {
	case *metricspb.NumberDataPoint_AsDouble:
		return v.AsDouble
	case *metricspb.NumberDataPoint_AsInt:
		return float64(v.AsInt)
	}
	return 0
}

// expBuckets marshals an ExponentialHistogram bucket into a compact map.
func expBuckets(b *metricspb.ExponentialHistogramDataPoint_Buckets) map[string]any {
	if b == nil {
		return nil
	}
	return map[string]any{
		"offset":        b.GetOffset(),
		"bucket_counts": b.GetBucketCounts(),
	}
}
