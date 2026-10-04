package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/service"
)

// fakeSettingsRepo is an in-memory service.SettingsRepository.
type fakeSettingsRepo struct{ m map[string]string }

func (f *fakeSettingsRepo) GetAllSettings() (map[string]string, error) { return f.m, nil }
func (f *fakeSettingsRepo) SetSetting(k, v string) error               { f.m[k] = v; return nil }
func (f *fakeSettingsRepo) DeleteSetting(k string) error               { delete(f.m, k); return nil }
func (f *fakeSettingsRepo) GetDBSize(string, string) (*repository.DBSize, error) {
	return nil, nil
}
func (f *fakeSettingsRepo) GetDBCounts() (*repository.DBCounts, error) { return nil, nil }

func TestSettingsPutMinTracesPerHour(t *testing.T) {
	h := &settingsHandlers{svc: service.NewSettingsService(&fakeSettingsRepo{m: map[string]string{}})}

	body := `{"boring.min_traces_per_hour":"2","boring.min_traces_per_hour.project.7":"0"}`
	put := httptest.NewRecorder()
	h.ServeHTTP(put, httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(body)))
	if put.Code != http.StatusOK {
		t.Fatalf("PUT: want 200, got %d: %s", put.Code, put.Body.String())
	}

	get := httptest.NewRecorder()
	h.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil))
	var got map[string]string
	if err := json.NewDecoder(get.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["boring.min_traces_per_hour"] != "2" || got["boring.min_traces_per_hour.project.7"] != "0" {
		t.Fatalf("read back mismatch: %v", got)
	}
}
