package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/wiebe-xyz/spanbarn/internal/model"

	collectorlogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func (s *Server) handleOTLPLogs(w http.ResponseWriter, r *http.Request) {
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

	var req collectorlogspb.ExportLogsServiceRequest
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
	recs := otlpToLogRecords(&req, projectID)
	s.logsIngest.Enqueue(recs)

	resp := &collectorlogspb.ExportLogsServiceResponse{}
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

// otlpToLogRecords converts an ExportLogsServiceRequest into LogRecords.
// Attributes are merged resource < scope < log-record (log-record wins).
func otlpToLogRecords(req *collectorlogspb.ExportLogsServiceRequest, projectID int64) []model.LogRecord {
	var records []model.LogRecord
	for _, rl := range req.GetResourceLogs() {
		resourceAttrs := kvListToMap(rl.GetResource().GetAttributes())

		for _, sl := range rl.GetScopeLogs() {
			scopeAttrs := kvListToMap(sl.GetScope().GetAttributes())

			for _, lr := range sl.GetLogRecords() {
				logAttrs := kvListToMap(lr.GetAttributes())

				merged := make(map[string]any, len(resourceAttrs)+len(scopeAttrs)+len(logAttrs))
				for k, v := range resourceAttrs {
					merged[k] = v
				}
				for k, v := range scopeAttrs {
					merged[k] = v
				}
				for k, v := range logAttrs {
					merged[k] = v
				}

				var attrsJSON json.RawMessage
				if len(merged) > 0 {
					attrsJSON, _ = json.Marshal(merged)
				}

				rec := model.LogRecord{
					ProjectID:            projectID,
					TraceID:              hexIfNonEmpty(lr.GetTraceId()),
					SpanID:               hexIfNonEmpty(lr.GetSpanId()),
					SeverityNumber:       int32(lr.GetSeverityNumber()),
					SeverityText:         lr.GetSeverityText(),
					TimeUnixNano:         lr.GetTimeUnixNano(),
					ObservedTimeUnixNano: lr.GetObservedTimeUnixNano(),
					Body:                 logBodyToString(lr.GetBody()),
					Attributes:           attrsJSON,
				}
				records = append(records, rec)
			}
		}
	}
	return records
}

// logBodyToString converts an OTLP log body AnyValue to a string.
// StringValue is used directly; KvlistValue is JSON-encoded; everything else uses anyValueToGo.
func logBodyToString(body *commonpb.AnyValue) string {
	if body == nil {
		return ""
	}
	switch v := body.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		return v.StringValue
	case *commonpb.AnyValue_KvlistValue:
		m := kvListToMap(v.KvlistValue.GetValues())
		data, _ := json.Marshal(m)
		return string(data)
	default:
		val := anyValueToGo(body)
		if val == nil {
			return ""
		}
		data, _ := json.Marshal(val)
		return string(data)
	}
}

