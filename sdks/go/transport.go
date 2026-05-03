package spanbarn

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Transport handles HTTP communication with the SpanBarn server.
type Transport struct {
	endpoint string
	apiKey   string
	client   *http.Client
}

type sendPayload struct {
	Spans []*SpanData `json:"spans"`
}

func newTransport(endpoint, apiKey string) *Transport {
	return &Transport{
		endpoint: endpoint,
		apiKey:   apiKey,
		client:   &http.Client{Timeout: 5 * time.Second},
	}
}

// Send transmits a batch of spans to the SpanBarn server.
func (t *Transport) Send(spans []*SpanData) error {
	if len(spans) == 0 {
		return nil
	}
	payload := sendPayload{Spans: spans}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("spanbarn: marshal: %w", err)
	}

	url := t.endpoint + "/api/v1/spans"
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("spanbarn: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-SpanBarn-Api-Key", t.apiKey)

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("spanbarn: send: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("spanbarn: server returned %d", resp.StatusCode)
	}
	return nil
}
